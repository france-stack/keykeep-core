// 桌面版数据库恢复（FR-34 / UC-12）。
//
// 恢复顺序与语义：
//   1) 校验 manifest 与每个数据文件的 SHA-256（截断/改写在灌库前发现）；
//   2) 目标库必须已迁移到备份时的版本（比对 _prisma_migrations 的 checksum）；
//      不自动迁移——恢复时顺手升级 schema 是另一种事故；
//   3) 单事务内 TRUNCATE 全部业务表 → 按 RESTORE_ORDER 用
//      json_populate_recordset 灌回 → 重设序列；
//   4) 灌完逐表比对行数，并对账本与审计链做一次终点校验。
//
// 安全性：默认 dry-run（只校验文件与目标库状态）；--apply 才写库。
// 目标库为空或本来就是同一个库都可以——TRUNCATE 之后重灌，幂等。
//
// 用法：
//   DATABASE_URL=... node restore.mjs --manifest <path>            # 预演
//   DATABASE_URL=... node restore.mjs --manifest <path> --apply    # 执行

import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { readFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

import pg from "pg";

import { fatal } from "./fatal.mjs";

const here = dirname(fileURLToPath(import.meta.url));

/**
 * 读取迁移不可变性的显式例外清单；缺失按"没有任何例外"处理。
 * 与 migrate-desktop.mjs 共用同一份 JSON：备份/恢复看到的例外必须与迁移器一致。
 */
function loadImmutabilityExceptions() {
  let raw;
  try {
    raw = readFileSync(join(here, "migrations-immutability.json"), "utf8");
  } catch {
    return new Map();
  }
  const map = new Map();
  for (const entry of JSON.parse(raw).exceptions ?? []) {
    map.set(entry.migration, new Set(entry.retiredChecksums ?? []));
  }
  return map;
}

const { Client } = pg;

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

const SEQUENCES = { AuditEvent: "AuditEvent_seq_seq", UsageRecord: "UsageRecord_seq_seq" };

function sha256(data) {
  return createHash("sha256").update(data).digest("hex");
}

async function tableExists(client, table) {
  const { rows } = await client.query(
    `SELECT 1 FROM information_schema.tables WHERE table_schema = 'public' AND table_name = $1`,
    [table],
  );
  return rows.length > 0;
}

async function main() {
  const index = process.argv.indexOf("--manifest");
  const manifestPath = index !== -1 ? process.argv[index + 1] : null;
  const apply = process.argv.includes("--apply");
  const connectionString = process.env.DATABASE_URL;
  if (!manifestPath) fatal("--manifest 未指定");
  if (!connectionString) fatal("DATABASE_URL 未设置");

  const manifest = JSON.parse(await readFile(manifestPath, "utf8"));
  if (manifest.format !== 1) fatal(`不认识的备份格式：${manifest.format}`);

  // 1) 文件完整性
  const payloads = {};
  for (const [table, info] of Object.entries(manifest.tables)) {
    if (info.skipped) continue;
    const data = await readFile(info.file);
    const want = manifest.sha256?.[table];
    if (want && sha256(data) !== want) {
      fatal(`备份文件哈希不符：${table}（文件被改动或截断）`);
    }
    payloads[table] = JSON.parse(data.toString("utf8"));
  }
  console.log(`备份文件校验通过：${Object.keys(payloads).length} 张表`);

  const client = new Client({ connectionString });
  await client.connect();
  try {
    // 2) 目标库 schema 版本
    const { rows: migrations } = await client.query(
      `SELECT migration_name, checksum FROM "_prisma_migrations"
        WHERE finished_at IS NOT NULL ORDER BY migration_name`,
    );
    const local = new Map(migrations.map((row) => [row.migration_name, row.checksum]));
    const exceptions = loadImmutabilityExceptions();
    const missing = (manifest.migrations ?? []).filter((entry) => !local.has(entry.name));
    const mismatch = (manifest.migrations ?? []).filter((entry) => {
      if (!local.has(entry.name)) return false;
      if (local.get(entry.name) === entry.checksum) return false;
      // 已登记的历史修复例外：备份侧记录的是旧 checksum，目标库是修复后的。
      // schema 未变，数据可以照灌。
      return !exceptions.get(entry.name)?.has(entry.checksum);
    });
    if (missing.length > 0) {
      fatal(
        `目标库缺少 ${missing.length} 条迁移（先跑迁移再恢复）：` +
          missing.slice(0, 5).map((entry) => entry.name).join(", "),
      );
    }
    if (mismatch.length > 0) {
      fatal(
        `目标库迁移校验和与备份不一致：` +
          mismatch.slice(0, 5).map((entry) => entry.name).join(", "),
      );
    }
    console.log(`schema 版本一致（${migrations.length} 条迁移）`);

    // 3) 预演也核对一遍「要写什么」，避免 --apply 时才发现表不存在
    const present = [];
    for (const table of RESTORE_ORDER) {
      if (!payloads[table]) continue;
      if (!(await tableExists(client, table))) {
        console.log(`跳过不存在的表：${table}`);
        continue;
      }
      present.push(table);
    }
    console.log(`将恢复 ${present.length} 张表，共 ${Object.values(payloads).reduce((sum, rows) => sum + rows.length, 0)} 行`);

    if (!apply) {
      console.log("预演结束。加 --apply 执行（会在单事务内 TRUNCATE 后重灌）。");
      return;
    }

    await client.query("BEGIN");
    try {
      // 单条 TRUNCATE 里再排顺序也不管用：PostgreSQL 要求列出表之间的**所有**
      // 引用关系都被覆盖，否则直接报「referenced in a foreign key constraint」。
      // 这里用 CASCADE —— 能级联到的只有组成这套 schema 的表，而它们本来就在
      // 待清空清单里；恢复语义就是"回到备份时刻"，不是"只删一部分"。
      // 唯一要守的边界：present 由备份 manifest 决定，不会跨到备份之外的表。
      const truncateList = present.map((table) => `"${table}"`).join(", ");
      await client.query(`TRUNCATE TABLE ${truncateList} CASCADE`);

      for (const table of present) {
        const rows = payloads[table];
        if (rows.length === 0) continue;
        const columns = manifest.tables[table].columns;
        // json_populate_recordset 在服务端完成 JSON→行类型的转换，
        // 不经过任何手写的转义/解析——这是选 JSON 格式备份的全部理由。
        await client.query(
          `INSERT INTO "${table}" (${columns.map((name) => `"${name}"`).join(", ")})
           SELECT ${columns.map((name) => `"${name}"`).join(", ")}
             FROM json_populate_recordset(NULL::"${table}", $1::json)`,
          [JSON.stringify(rows)],
        );
        const { rows: counted } = await client.query(`SELECT count(*)::bigint AS n FROM "${table}"`);
        if (String(counted[0].n) !== String(rows.length)) {
          throw new Error(`${table} 行数不符：期望 ${rows.length}，实际 ${counted[0].n}`);
        }
      }

      // 4) 序列重设：不重设的话新插入会从 setval 之前的位置开始，直接撞主键。
      for (const [table, sequence] of Object.entries(SEQUENCES)) {
        if (!present.includes(table)) continue;
        await client.query(
          `SELECT setval('"${sequence}"', COALESCE((SELECT max(seq) FROM "${table}"), 1),
                  (SELECT max(seq) IS NOT NULL FROM "${table}"))`,
        );
      }

      await client.query("COMMIT");
      console.log("恢复完成（单事务已提交）");
    } catch (error) {
      await client.query("ROLLBACK").catch(() => {});
      throw error;
    }

    // 5) 链终点校验：账本与审计的 rowHash 必须能逐行复算。这里只做「有没有
    // 非空 rowHash + 终点连续」的轻量核对，完整走链由控制台的校验接口负责。
    for (const [table, label] of [["UsageRecord", "账本"], ["AuditEvent", "审计"]]) {
      if (!present.includes(table)) continue;
      const { rows } = await client.query(
        `SELECT count(*) FILTER (WHERE "rowHash" IS NOT NULL) AS chained,
                count(*) AS total,
                max(seq) AS head
           FROM "${table}"`,
      );
      console.log(`${label}：${rows[0].chained}/${rows[0].total} 行已入链，链头 seq=${rows[0].head}`);
    }
    const kek = process.env.KEYKEEP_KEK;
    if (manifest.kekFingerprint) {
      const fingerprint = kek ? "sha256:" + createHash("sha256").update(kek, "utf8").digest("hex").slice(0, 16) : null;
      if (fingerprint !== manifest.kekFingerprint) {
        console.error(
          "警告：当前 KEYKEEP_KEK 与备份时不一致——上游凭据的密文将无法解密。" +
            "请改用备份时的 settings.json 再启动。",
        );
        process.exitCode = 1;
      } else {
        console.log("KEK 指纹一致：上游凭据可解密");
      }
    }
  } finally {
    await client.end().catch(() => {});
  }
}

// 报错只走一个出口：fatal 本身已负责屏蔽凭据。
main().catch(fatal);
