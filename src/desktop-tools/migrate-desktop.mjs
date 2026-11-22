// 桌面版数据库迁移。
//
// 为什么不用 prisma migrate deploy：桌面版要把依赖树复制进安装包，
// 而 pnpm 的符号链接布局在 Windows 复制后会断（@prisma/engines 找不到、
// confbox 的 ./json 子路径未导出）。Prisma CLI 的依赖比它做的事重得多。
//
// 这里只复用 Prisma 的「迁移目录 + _prisma_migrations 记录表」约定：
//   - 迁移文件仍是 src/control/prisma/migrations/<时间戳>_<名称>/migration.sql；
//   - 记录表结构与 Prisma 完全一致，因此容器/开发环境继续用
//     prisma migrate deploy 管理同一套迁移，两边互认、不会互相打架。
//
// 用法（迁移目录由 kkdesk 通过 --dir 传入）：
//   DATABASE_URL=... node migrate-desktop.mjs --dir <migrations 目录>
//
// 退码：0 成功；1 失败（错误写到 stderr，由 kkdesk 转进运行日志）。

import { createHash, randomUUID } from "node:crypto";
import { readdirSync, readFileSync, statSync } from "node:fs";
import { dirname, join, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import pg from "pg";

import { fatal } from "./fatal.mjs";

const { Client } = pg;

const here = dirname(fileURLToPath(import.meta.url));

/** 解析迁移目录：--dir 优先，其次环境变量，最后是仓库内开发期路径。 */
function migrationsDir() {
  const flagIndex = process.argv.indexOf("--dir");
  if (flagIndex !== -1 && process.argv[flagIndex + 1]) {
    return resolve(process.argv[flagIndex + 1]);
  }
  if (process.env.KEYKEEP_MIGRATIONS_DIR) {
    return resolve(process.env.KEYKEEP_MIGRATIONS_DIR);
  }
  return join(here, "..", "control", "prisma", "migrations");
}

// 与 Prisma 一致的迁移目录名形态；同时挡住路径穿越。
const MIGRATION_NAME = /^[0-9]{14}_[A-Za-z0-9_]+$/;

const CREATE_TABLE = `
CREATE TABLE IF NOT EXISTS "_prisma_migrations" (
    "id"                  VARCHAR(36) PRIMARY KEY NOT NULL,
    "checksum"            VARCHAR(64) NOT NULL,
    "finished_at"         TIMESTAMPTZ,
    "migration_name"      VARCHAR(255) NOT NULL,
    "logs"                TEXT,
    "rolled_back_at"      TIMESTAMPTZ,
    "started_at"          TIMESTAMPTZ NOT NULL DEFAULT now(),
    "applied_steps_count" INTEGER NOT NULL DEFAULT 0
)`;

/** 按分号切分 SQL，跳过注释与字符串内的分号。 */
function splitStatements(sql) {
  const out = [];
  let current = "";
  let inSingle = false;
  let inDouble = false;
  let inLineComment = false;
  let inBlockComment = false;

  for (let i = 0; i < sql.length; i += 1) {
    const ch = sql[i];
    const next = sql[i + 1];

    if (inLineComment) {
      if (ch === "\n") {
        inLineComment = false;
        current += ch;
      }
      continue;
    }
    if (inBlockComment) {
      if (ch === "*" && next === "/") {
        inBlockComment = false;
        i += 1;
      }
      continue;
    }
    if (inSingle) {
      current += ch;
      if (ch === "'") inSingle = false;
      continue;
    }
    if (inDouble) {
      current += ch;
      if (ch === '"') inDouble = false;
      continue;
    }

    if (ch === "-" && next === "-") {
      inLineComment = true;
      i += 1;
    } else if (ch === "/" && next === "*") {
      inBlockComment = true;
      i += 1;
    } else if (ch === "'") {
      inSingle = true;
      current += ch;
    } else if (ch === '"') {
      inDouble = true;
      current += ch;
    } else if (ch === ";") {
      const text = current.trim();
      if (text) out.push(text);
      current = "";
    } else {
      current += ch;
    }
  }
  const tail = current.trim();
  if (tail) out.push(tail);
  return out;
}

/** 读入迁移目录，按名称排序（= 时间顺序）。 */
function loadMigrations(migrationsDir) {
  const entries = readdirSync(migrationsDir).filter((name) => {
    if (!MIGRATION_NAME.test(name)) return false;
    try {
      return statSync(join(migrationsDir, name)).isDirectory();
    } catch {
      return false;
    }
  });
  entries.sort();

  return entries.map((name) => {
    const file = join(migrationsDir, name, "migration.sql");
    const rel = relative(migrationsDir, file);
    if (rel.startsWith("..")) throw new Error(`迁移路径越界：${file}`);
    return { name, sql: readFileSync(file, "utf8") };
  });
}

async function main() {
  const connectionString = process.env.DATABASE_URL;
  if (!connectionString) {
    console.error("DATABASE_URL is required");
    process.exit(1);
  }
  const migrationsDirectory = migrationsDir();
  const migrations = loadMigrations(migrationsDirectory);
  if (migrations.length === 0) {
    console.error(`没有找到迁移文件：${migrationsDirectory}`);
    process.exit(1);
  }

  const client = new Client({ connectionString });
  await client.connect();
  try {
    await client.query(CREATE_TABLE);
    const { rows } = await client.query(
      `SELECT "migration_name", "checksum" FROM "_prisma_migrations" WHERE "rolled_back_at" IS NULL`,
    );
    const applied = new Map(rows.map((row) => [row.migration_name, row.checksum]));

    // 已应用迁移的文件**不许再改**：Prisma 的整条生态（migrate deploy / migrate
    // diff / CI 的 drift check）都以 checksum 为准，改了文件而不改库会让两边
    // 永久对不上——恢复演练正是在这里炸的（库里 20e9e547…，磁盘 19179d5d…）。
    //
    // 例外只有一种：旧内容让首装必然失败，且修复发生在产品正式发布之前。
    // 那种改动必须在 migrations-immutability.json 里逐条登记历史 checksum，
    // 这里据此把库里的旧 checksum 就地刷新成当前文件的值（schema 不变），
    // 而不是让每个已安装实例永远对不上。未登记的改动一律拦下。
    const exceptions = loadImmutabilityExceptions();
    const drifted = [];
    const refresh = [];
    for (const migration of migrations) {
      if (!applied.has(migration.name)) continue;
      const recorded = applied.get(migration.name);
      const current = checksumOf(migration.sql);
      if (recorded === current) continue;
      const allowed = exceptions.get(migration.name);
      if (allowed?.has(recorded)) {
        refresh.push({ name: migration.name, recorded, current });
        continue;
      }
      drifted.push({ name: migration.name, recorded, current });
    }
    if (drifted.length > 0) {
      console.error(
        "已应用的迁移文件被改动过（违反迁移不可变性）：\n" +
          drifted
            .map((item) => `  - ${item.name}（库 ${item.recorded.slice(0, 10)}… / 文件 ${item.current.slice(0, 10)}…）`)
            .join("\n") +
          "\n修法：把文件恢复到已应用时的内容，另建一个新迁移做修正。" +
          "\n若确实属于「旧内容会炸首装」的历史修复，在 src/desktop-tools/migrations-immutability.json 登记历史 checksum。",
      );
      process.exit(1);
    }
    for (const item of refresh) {
      console.warn(
        `迁移 ${item.name} 的 checksum 已按登记例外刷新（${item.recorded.slice(0, 10)}… → ${item.current.slice(0, 10)}…），schema 未变`,
      );
    }

    let count = 0;
    // 整批放在一个事务里：中途失败全部回滚，避免留下半套表。
    await client.query("BEGIN");
    try {
      for (const migration of migrations) {
        if (applied.has(migration.name)) continue;
        const started = Date.now();
        const statements = splitStatements(migration.sql);
        for (const [index, statement] of statements.entries()) {
          try {
            await client.query(statement);
          } catch (error) {
            throw new Error(`${migration.name} 第 ${index + 1} 条语句失败：${error.message}`);
          }
        }
        const checksum = checksumOf(migration.sql);
        const took = Date.now() - started;
        await client.query(
          `INSERT INTO "_prisma_migrations"
             ("id", "checksum", "finished_at", "migration_name", "logs", "started_at", "applied_steps_count")
           VALUES ($1, $2, now(), $3, $4, now(), $5)`,
          [randomUUID(), checksum, migration.name,
            `Applied ${statements.length} statement(s) in ${took}ms (keykeep desktop migrator)\n`,
            statements.length],
        );
        count += 1;
        console.log(`applied ${migration.name}`);
      }
      // 登记例外的 checksum 刷新与整批迁移在同一个事务里：迁移失败时
      // 库里的旧 checksum 也不会被改成"新文件的值"，两边要么一起前进要么都不动。
      for (const item of refresh) {
        await client.query(
          `UPDATE "_prisma_migrations" SET "checksum" = $1
            WHERE "migration_name" = $2 AND "checksum" = $3 AND "rolled_back_at" IS NULL`,
          [item.current, item.name, item.recorded],
        );
      }
      await client.query("COMMIT");
    } catch (error) {
      await client.query("ROLLBACK");
      throw error;
    }
    console.log(JSON.stringify({ applied: count, total: migrations.length }));
  } finally {
    await client.end();
  }
}

main().catch(fatal);

/** 迁移文件的 checksum：与 Prisma 一致（文件原文的 sha256 hex）。 */
function checksumOf(sql) {
  return createHash("sha256").update(sql, "utf8").digest("hex");
}

/**
 * 读取迁移不可变性的显式例外清单；文件缺失按"没有任何例外"处理。
 * 返回 Map<迁移名, Set<历史 checksum>>。
 */
function loadImmutabilityExceptions() {
  const path = join(here, "migrations-immutability.json");
  let raw;
  try {
    raw = readFileSync(path, "utf8");
  } catch {
    return new Map();
  }
  const parsed = JSON.parse(raw);
  const map = new Map();
  for (const entry of parsed.exceptions ?? []) {
    map.set(entry.migration, new Set(entry.retiredChecksums ?? []));
  }
  return map;
}
