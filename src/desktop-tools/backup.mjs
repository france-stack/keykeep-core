// 桌面版数据库备份（FR-34 / UC-12）。
//
// 为什么不用 pg_dump：安装包里的 PostgreSQL 来自 zonky 的 embedded-postgres
// 二进制包，随包的只有 postgres / initdb / pg_ctl，没有 pg_dump / pg_restore /
// psql。scripts/ops/backup.py 走 pg_dump 在 compose 形态可用，在桌面形态从来
// 跑不起来——这正是「恢复演练从未执行」的直接原因。
//
// 做法：在**同一事务**里对每张表做 COPY ... TO STDOUT（text 格式），并顺带
// 记录每张表的列清单与行数。单个 REPEATABLE READ 事务保证所有表来自同一快照，
// 不会出现「账本有这一行、预算没有」的撕裂备份。
//
// 一致性边界（必须写清楚，否则恢复时会踩）：
//   - 这是**逻辑数据**备份，不含索引/约束/触发器定义——它们由 prisma migrations
//     重建；恢复流程是「先迁移到目标版本，再灌数据」。
//   - 不含序列的当前值：恢复后按 max(seq) 重设，见 restore.mjs。
//   - 凭据列是密文，没有 settings.json 的 KEK 仍然解不开。manifest 里只记
//     KEK 的 SHA-256 前 16 位，用于核对「是不是同一把钥匙」，不泄露密钥。
//
// 用法（在 src/desktop-tools 目录下，依赖随安装包分发）：
//   DATABASE_URL=... KEYKEEP_KEK=... node backup.mjs --out <目录>

import { createHash } from "node:crypto";
import { createWriteStream } from "node:fs";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";

import pg from "pg";

import { fatal } from "./fatal.mjs";

const { Client } = pg;

/** 需要备份的业务表。顺序无关（恢复时按外键拓扑排序），但清单必须显式。 */
export const TABLES = [
  "Role",
  "User",
  "ProviderCredential",
  "ModelMap",
  "ModelPrice",
  "PriceVersion",
  "ApiKey",
  "Limit",
  "Budget",
  "UsageRecord",
  "UsageDaily",
  "AuditEvent",
  "RedactRule",
  "RequestClaim",
  "RateWindow",
  "LicenseState",
  "LicenseRevocation",
  "ModelCatalogEntry",
  "_prisma_migrations",
];

/**
 * 表的恢复顺序：被引用的先灌。RateWindow / RequestClaim 是运行期缓存表，
 * 内容可丢，但保留它们在备份里能让恢复后的现场更完整（例如排障时看窗口）。
 */
const RESTORE_ORDER = [
  "Role",
  "User",
  "ProviderCredential",
  "ModelMap",
  "PriceVersion",
  "ModelPrice",
  "ApiKey",
  "Limit",
  "Budget",
  "RedactRule",
  "ModelCatalogEntry",
  "LicenseState",
  "LicenseRevocation",
  "UsageRecord",
  "UsageDaily",
  "AuditEvent",
  "_prisma_migrations",
  "RequestClaim",
  "RateWindow",
];

/** 每张表的序列列（恢复后要重设，否则新插入会撞主键）。 */
export const SEQUENCES = {
  AuditEvent: "AuditEvent_seq_seq",
  UsageRecord: "UsageRecord_seq_seq",
};

export function kekFingerprint(kek) {
  if (!kek) return null;
  return "sha256:" + createHash("sha256").update(kek, "utf8").digest("hex").slice(0, 16);
}

function timestamp() {
  return new Date().toISOString().replace(/[:.]/g, "").replace("T", "-").replace("Z", "");
}

async function tableColumns(client, table) {
  const { rows } = await client.query(
    `SELECT column_name FROM information_schema.columns
      WHERE table_schema = 'public' AND table_name = $1
      ORDER BY ordinal_position`,
    [table],
  );
  return rows.map((row) => row.column_name);
}

async function tableExists(client, table) {
  const { rows } = await client.query(
    `SELECT 1 FROM information_schema.tables WHERE table_schema = 'public' AND table_name = $1`,
    [table],
  );
  return rows.length > 0;
}

/**
 * COPY 一张表到文件。
 *
 * 不用 pg 的 COPY TO STDOUT 流（那需要 pg-copy-streams 这个额外依赖，而
 * desktop-tools 的依赖清单越短越好）：逐列拼 TSV 的等价做法是
 * `SELECT ... ` + 自己编码，但那样要自己处理 NULL、制表符、换行、反斜杠转义，
 * 一处漏掉就是恢复时的静默数据损坏。
 *
 * 折中：用 jsonb 聚合把整表序列化成一列文本（PostgreSQL 负责全部转义），
 * 一行一个 JSON 对象。代价是体积比 COPY 大约 30%，换来的是「编码只存在于
 * 服务端一处」。恢复用 json_populate_recordset 反向灌回，不经过任何手写解析。
 */
async function copyTable(client, table, columns, path) {
  const columnList = columns.map((name) => `"${name}"`).join(", ");
  const { rows } = await client.query(
    `SELECT COALESCE(jsonb_agg(to_jsonb(t)), '[]'::jsonb)::text AS payload
       FROM (SELECT ${columnList} FROM "${table}") AS t`,
  );
  const stream = createWriteStream(path, { flags: "w" });
  await new Promise((resolve, reject) => {
    stream.on("error", reject);
    stream.on("finish", resolve);
    stream.end(rows[0].payload, "utf8");
  });
}

async function main() {
  const outIndex = process.argv.indexOf("--out");
  const outDir = outIndex !== -1 ? process.argv[outIndex + 1] : null;
  const retainIndex = process.argv.indexOf("--retain");
  const retain = retainIndex !== -1 ? Number(process.argv[retainIndex + 1]) : 7;
  const connectionString = process.env.DATABASE_URL;
  if (!outDir) fatal("--out 未指定");
  if (!connectionString) fatal("DATABASE_URL 未设置");
  if (!Number.isInteger(retain) || retain < 1) fatal("--retain 必须是 >=1 的整数");

  await mkdir(outDir, { recursive: true });
  const stamp = timestamp();
  const base = join(outDir, `keykeep-${stamp}`);

  const client = new Client({ connectionString });
  await client.connect();
  const manifest = {
    format: 1,
    createdAt: new Date().toISOString(),
    base: `keykeep-${stamp}`,
    kekFingerprint: kekFingerprint(process.env.KEYKEEP_KEK),
    restoreOrder: RESTORE_ORDER,
    sequences: SEQUENCES,
    tables: {},
  };

  try {
    // 单事务 + REPEATABLE READ：所有表来自同一快照。READ ONLY 是最后一道保险，
    // 备份脚本不该有写权限这件事，靠代码自觉不如靠事务属性。
    await client.query("BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY");
    for (const table of RESTORE_ORDER) {
      if (!(await tableExists(client, table))) {
        manifest.tables[table] = { skipped: true, reason: "missing" };
        continue;
      }
      const columns = await tableColumns(client, table);
      const file = `${base}.${table}.copy`;
      await copyTable(client, table, columns, file);
      const { rows } = await client.query(`SELECT count(*)::bigint AS n FROM "${table}"`);
      manifest.tables[table] = { columns, rows: String(rows[0].n), file };
      console.log(`${table.padEnd(22)} ${String(rows[0].n).padStart(8)} 行`);
    }
    // 迁移版本单独记一份，恢复前可先比对（schema 不匹配时灌数据会失败）。
    const migrations = await client.query(
      `SELECT migration_name, checksum FROM "_prisma_migrations" WHERE finished_at IS NOT NULL ORDER BY migration_name`,
    );
    manifest.migrations = migrations.rows.map((row) => ({
      name: row.migration_name,
      checksum: row.checksum,
    }));
    await client.query("COMMIT");
  } catch (error) {
    await client.query("ROLLBACK").catch(() => {});
    throw error;
  } finally {
    await client.end().catch(() => {});
  }

  const manifestPath = `${base}.manifest.json`;
  await writeFile(manifestPath, JSON.stringify(manifest, null, 2), "utf8");
  // 备份文件本身也记哈希：恢复前先核对文件没被截断/改写。
  const hashes = {};
  for (const [table, info] of Object.entries(manifest.tables)) {
    if (info.skipped) continue;
    const data = await readFile(info.file);
    hashes[table] = createHash("sha256").update(data).digest("hex");
  }
  manifest.sha256 = hashes;
  await writeFile(manifestPath, JSON.stringify(manifest, null, 2), "utf8");

  console.log(`备份完成：${manifestPath}`);
  if (!manifest.kekFingerprint) {
    console.log("提示：未提供 KEYKEEP_KEK，manifest 不含 KEK 指纹；恢复后无法核对密钥是否一致。");
  }
  console.log("注意：备份文件含加密后的上游凭据。恢复需要同一个 KEK（settings.json）。");

  // 保留策略放在备份成功之后：先确认这一份是好的，再删旧的。反过来做的话，
  // 一次「备份失败 + 清理成功」就会把最后一份可用备份一起删掉。
  const removed = await pruneOldBackups(outDir, retain);
  if (removed.length > 0) {
    console.log(`已清理 ${removed.length} 份超出保留数（${retain}）的旧备份`);
  }
}

/**
 * 保留策略：按 manifest 分组成套删除。
 *
 * 两点必须说清楚：
 *   - 删除发生在本份备份成功之后；顺序反了会出现「备份失败 + 清理成功 =
 *     最后一份可用备份也没了」。
 *   - 不按 mtime 排序。备份目录常被同步盘/杀软碰过，mtime 会漂；文件名里的
 *     时间戳才是备份时写下的真顺序。
 *
 * 孤儿文件（有 .copy 没有 manifest，来自某次中途失败的备份）单独清理，
 * 但只删超过一天的：正在进行的备份在写出 manifest 之前，它的 .copy 也是「孤儿」。
 */
async function pruneOldBackups(outDir, retain) {
  const { readdir, rm, stat } = await import("node:fs/promises");
  const entries = await readdir(outDir);
  // 与 timestamp() 同形：keykeep-2026-11-08-133029123.manifest.json。
  const pattern = /^keykeep-(\d{4})-(\d{2})-(\d{2})-(\d{2})(\d{2})(\d{2})\d*\.manifest\.json$/;
  const manifests = entries.filter((name) => pattern.test(name)).sort();
  const stale = manifests.slice(0, Math.max(0, manifests.length - retain));
  const removed = [];
  for (const manifestName of stale) {
    const base = manifestName.replace(/\.manifest\.json$/, "");
    for (const name of entries) {
      if (name === manifestName || name.startsWith(`${base}.`)) {
        await rm(join(outDir, name), { force: true });
      }
    }
    removed.push(base);
  }

  const keptBases = new Set(
    manifests.slice(Math.max(0, manifests.length - retain)).map((name) => name.replace(/\.manifest\.json$/, "")),
  );
  const orphanCutoff = Date.now() - 24 * 3600 * 1000;
  for (const name of entries) {
    if (!name.startsWith("keykeep-") || !name.includes(".")) continue;
    if (name.endsWith(".manifest.json")) continue;
    const base = name.split(".").slice(0, -2).join(".");
    if (keptBases.has(base)) continue;
    const info = await stat(join(outDir, name)).catch(() => null);
    if (!info || info.mtimeMs > orphanCutoff) continue;
    await rm(join(outDir, name), { force: true });
  }
  return removed;
}

// 报错只走一个出口：fatal 本身已负责屏蔽凭据。
main().catch(fatal);
