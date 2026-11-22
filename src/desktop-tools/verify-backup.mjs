// 离线校验一份备份是否完整可用（不连数据库）。
//
// 恢复流程里最贵的一步是「发现备份是坏的」——如果它发生在真的需要恢复的那天，
// 就没有第二次机会了。所以备份完整性必须能单独、频繁、离线地验：
// 定时任务每晚备份完顺手验一次，失败就报警，这比每月人工演练一次可靠得多。
//
// 校验项：
//   1) manifest 可解析、格式版本认识；
//   2) 每张表的数据文件存在，SHA-256 与 manifest 一致（截断/改写在本地就能发现）；
//   3) 每张表的数据文件是合法 JSON 数组，行数与 manifest 声明一致；
//   4) manifest 里声明的表清单覆盖了备份顺序里出现的每一张；
//   5) 迁移记录非空（空迁移列表意味着备份来自一个没跑过迁移的库）。
//
// 用法：
//   node verify-backup.mjs --manifest <path>
//
// 退码：0 全部通过；1 有问题（问题逐条写到 stderr）。

import { createHash } from "node:crypto";
import { readFile } from "node:fs/promises";

import { fatal } from "./fatal.mjs";

const SUPPORTED_FORMAT = 1;

const REQUIRED_TABLES = [
  "User",
  "ApiKey",
  "ProviderCredential",
  "UsageRecord",
  "UsageDaily",
  "Budget",
  "AuditEvent",
  "_prisma_migrations",
];

function sha256(data) {
  return createHash("sha256").update(data).digest("hex");
}

async function main() {
  const index = process.argv.indexOf("--manifest");
  const manifestPath = index !== -1 ? process.argv[index + 1] : null;
  if (!manifestPath) fatal("--manifest 未指定");

  let manifest;
  try {
    manifest = JSON.parse(await readFile(manifestPath, "utf8"));
  } catch (error) {
    fatal(`manifest 读不出来：${error instanceof Error ? error.message : error}`);
  }
  if (manifest.format !== SUPPORTED_FORMAT) {
    fatal(`不认识的备份格式 ${manifest.format}（本工具只认 ${SUPPORTED_FORMAT}）`);
  }

  const problems = [];
  let checkedTables = 0;
  let checkedRows = 0;

  for (const [table, info] of Object.entries(manifest.tables ?? {})) {
    if (info.skipped) {
      // 「表不存在」是合法状态（旧版本库没有新表），但核心业务表必须存在。
      if (REQUIRED_TABLES.includes(table)) {
        problems.push(`${table}: manifest 标记为跳过，但它是核心表`);
      }
      continue;
    }
    let raw;
    try {
      raw = await readFile(info.file);
    } catch {
      problems.push(`${table}: 数据文件缺失（${info.file}）`);
      continue;
    }
    const want = manifest.sha256?.[table];
    if (!want) {
      problems.push(`${table}: manifest 没有记录该表的哈希`);
    } else if (sha256(raw) !== want) {
      problems.push(`${table}: 文件哈希不符（被截断或改写）`);
      continue;
    }

    let rows;
    try {
      rows = JSON.parse(raw.toString("utf8"));
    } catch {
      problems.push(`${table}: 数据文件不是合法 JSON`);
      continue;
    }
    if (!Array.isArray(rows)) {
      problems.push(`${table}: 数据文件不是 JSON 数组`);
      continue;
    }
    if (String(rows.length) !== String(info.rows)) {
      problems.push(`${table}: 行数不符（manifest ${info.rows}，文件 ${rows.length}）`);
      continue;
    }
    // 列清单必须在文件里有对应字段：少一列说明备份时读到了旧 schema。
    const first = rows[0];
    if (first) {
      const missing = (info.columns ?? []).filter((name) => !(name in first));
      if (missing.length > 0) {
        problems.push(`${table}: 数据行缺少列 ${missing.join(", ")}`);
      }
    }
    checkedTables++;
    checkedRows += rows.length;
  }

  for (const table of REQUIRED_TABLES) {
    if (!manifest.tables?.[table]) {
      problems.push(`${table}: manifest 里完全没有这张表`);
    }
  }
  if (!Array.isArray(manifest.migrations) || manifest.migrations.length === 0) {
    problems.push("manifest 的迁移记录为空——备份来自一个没跑过迁移的库");
  }

  if (problems.length > 0) {
    console.error(`备份校验失败（${problems.length} 项）：`);
    for (const problem of problems) console.error(`  - ${problem}`);
    process.exitCode = 1;
    return;
  }

  const summary = {
    ok: true,
    manifest: manifestPath,
    createdAt: manifest.createdAt,
    tables: checkedTables,
    rows: checkedRows,
    migrations: manifest.migrations.length,
    kekFingerprint: manifest.kekFingerprint ?? null,
  };
  console.log(JSON.stringify(summary, null, 2));
  if (!manifest.kekFingerprint) {
    console.log("提示：manifest 不含 KEK 指纹，恢复后无法核对密钥是否配套。");
  }
}

// 报错只走一个出口：fatal 本身已负责屏蔽凭据。
main().catch(fatal);
