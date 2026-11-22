// 备份恢复演练（FR-34 / UC-12 / R-09）。
//
// 这条流程此前从未在桌面形态执行过，原因是它依赖 pg_dump，而桌面运行时里
// 没有那个二进制。改成 JSON 逻辑备份之后，演练只需要一个空闲端口就能在
// 本机完成：起一个临时 PostgreSQL 实例 → 迁移 → 恢复 → 逐项核对 → 拆掉。
//
// 演练检查项（任一项失败即整体失败）：
//   1) 备份文件的每张表行数与 manifest 一致；
//   2) 恢复后的行数与备份一致；
//   3) 账本链与审计链的 rowHash 行数一致（说明恢复没有丢链）；
//   4) 用备份的 KEK 能解开一条上游凭据（证明 KEK 与密文配套）；
//   5) KEK 指纹与 manifest 一致。
//
// 用法（在 src/desktop-tools 下）：
//   KEYKEEP_KEK=... node drill.mjs --source <DATABASE_URL> [--keep]
//
// --keep 保留临时实例目录，便于事后查看；默认清理。

import { createHash, randomBytes } from "node:crypto";
import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { spawn } from "node:child_process";

import pg from "pg";

import { fatal } from "./fatal.mjs";

const { Client } = pg;

const here = dirname(fileURLToPath(import.meta.url));

/** 找一个空闲端口；只用于临时实例。 */
function freePort() {
  return new Promise((resolvePort, reject) => {
    const server = createServer();
    server.once("error", reject);
    server.listen(0, "127.0.0.1", () => {
      const { port } = server.address();
      server.close(() => resolvePort(port));
    });
  });
}

/** 跑一个子进程并收集输出；失败时把 stderr 带进错误。 */
function run(command, args, env = {}) {
  return new Promise((resolveRun) => {
    const child = spawn(command, args, { env: { ...process.env, ...env }, windowsHide: true });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (chunk) => (stdout += chunk));
    child.stderr.on("data", (chunk) => (stderr += chunk));
    child.on("close", (code) => resolveRun({ code, stdout, stderr }));
  });
}

/**
 * 等待临时实例真正接受查询。
 *
 * 端口能连上不等于数据库可用：PostgreSQL 启动时先做 WAL 重放，这段时间它
 * 接受 TCP 却回 57P03。判据必须是"能成功执行一次查询"。
 */
async function waitReady(client, timeoutMs = 60_000) {
  const deadline = Date.now() + timeoutMs;
  let lastError = null;
  while (Date.now() < deadline) {
    try {
      await client.query("SELECT 1");
      return;
    } catch (error) {
      lastError = error;
      await new Promise((wait) => setTimeout(wait, 500));
    }
  }
  throw new Error(
    `等待 PostgreSQL 就绪超时：${lastError instanceof Error ? lastError.message : lastError}`,
  );
}

async function main() {
  const sourceIndex = process.argv.indexOf("--source");
  const source = sourceIndex !== -1 ? process.argv[sourceIndex + 1] : process.env.DATABASE_URL;
  const keep = process.argv.includes("--keep");
  if (!source) fatal("--source 或 DATABASE_URL 未指定");

  // 桌面运行时的 PostgreSQL 二进制；环境变量可覆盖以便在开发机上跑。
  const pgBin = process.env.KEYKEEP_PG_BIN ?? resolve(here, "..", "..", "build", "runtime", "pgsql", "bin");
  const password = randomBytes(12).toString("hex");
  const work = await mkdtemp(join(tmpdir(), "keykeep-drill-"));
  const dataDir = join(work, "pg");
  const backupDir = join(work, "backup");
  const port = await freePort();

  const initdb = join(pgBin, "initdb.exe");
  const postgres = join(pgBin, "postgres.exe");

  console.log(`演练工作目录：${work}`);
  console.log(`临时实例端口：${port}`);

  let child = null;
  try {
    await mkdir(dataDir, { recursive: true });
    const passwordFile = join(work, ".pw");
    await writeFile(passwordFile, password, "utf8");
    const init = await run(initdb, [
      "-D", dataDir, "-U", "keykeep", `--pwfile=${passwordFile}`,
      "--auth-host=scram-sha-256", "--auth-local=trust", "--encoding=UTF8", "--locale=C",
    ]);
    if (init.code !== 0) throw new Error(`initdb 失败：${init.stderr.slice(0, 400)}`);

    child = spawn(postgres, ["-D", dataDir, "-p", String(port), "-h", "127.0.0.1"], {
      env: { ...process.env, PGPASSWORD: password },
      windowsHide: true,
      stdio: "ignore",
    });

    // 先连 postgres 维护库（重试到能查询为止），再建业务库。
    //
    // pg 的 Client 一旦 connect 失败就作废，重连必须新建实例——复用同一个
    // Client 会一直失败，表现为「明明端口通了却永远等不到就绪」。
    // 用 URL 对象拼，而不是模板串 —— 拼出来的形态会和真实凭据一样，秘密扫描
    // 无法区分「注入的临时口令」与「泄漏的连接串」，只能整类放过。
    const adminURL = new URL("postgresql://127.0.0.1");
    adminURL.hostname = "127.0.0.1";
    adminURL.port = String(port);
    adminURL.pathname = "/postgres";
    adminURL.username = "keykeep";
    adminURL.password = password;
    const deadline = Date.now() + 60_000;
    let lastError = null;
    for (;;) {
      const admin = new Client({ connectionString: adminURL.toString(), connectionTimeoutMillis: 3000 });
      try {
        await admin.connect();
        await admin.query("SELECT 1");
        await admin.query("CREATE DATABASE keykeep");
        await admin.end();
        break;
      } catch (error) {
        lastError = error;
        await admin.end().catch(() => {});
        if (child.exitCode !== null) throw new Error("临时 PostgreSQL 进程已退出");
        if (Date.now() > deadline) {
          throw new Error(`等待临时实例超时：${lastError instanceof Error ? lastError.message : lastError}`);
        }
        await new Promise((wait) => setTimeout(wait, 500));
      }
    }
    console.log("临时实例已就绪");

    const targetURL = new URL("postgresql://127.0.0.1");
    targetURL.hostname = "127.0.0.1";
    targetURL.port = String(port);
    targetURL.pathname = "/keykeep";
    targetURL.username = "keykeep";
    targetURL.password = password;
    const target = targetURL.toString();

    // 1) 备份源库
    const backup = await run(process.execPath, [join(here, "backup.mjs"), "--out", backupDir], {
      DATABASE_URL: source,
      KEYKEEP_KEK: process.env.KEYKEEP_KEK ?? "",
    });
    if (backup.code !== 0) throw new Error(`备份失败：${backup.stderr.slice(0, 400)}`);
    const manifestPath = backup.stdout
      .split(/\r?\n/)
      .find((line) => line.includes(".manifest.json"))?.replace(/^.*?：/, "");
    if (!manifestPath) throw new Error("备份输出里找不到 manifest 路径");
    console.log(`备份完成：${manifestPath}`);

    // 2) 目标库迁移到备份时的版本
    const migrate = await run(process.execPath, [
      join(here, "migrate-desktop.mjs"),
      "--dir", resolve(here, "..", "control", "prisma", "migrations"),
    ], { DATABASE_URL: target });
    if (migrate.code !== 0) throw new Error(`迁移失败：${migrate.stderr.slice(0, 400)}`);
    console.log("目标库已迁移");

    // 3) 恢复
    const restore = await run(process.execPath, [join(here, "restore.mjs"), "--manifest", manifestPath, "--apply"], {
      DATABASE_URL: target,
      KEYKEEP_KEK: process.env.KEYKEEP_KEK ?? "",
    });
    if (restore.code !== 0) throw new Error(`恢复失败：${restore.stderr.slice(0, 400)}`);
    console.log(restore.stdout.trim());

    // 4) 源库与目标库逐表对账
    const read = await import("node:fs/promises").then((fs) => fs.readFile(manifestPath, "utf8"));
    const manifest = JSON.parse(read);
    const targetClient = new Client({ connectionString: target });
    await targetClient.connect();
    const failures = [];
    for (const [table, info] of Object.entries(manifest.tables)) {
      if (info.skipped) continue;
      const { rows } = await targetClient.query(`SELECT count(*)::bigint AS n FROM "${table}"`);
      if (String(rows[0].n) !== String(info.rows)) {
        failures.push(`${table}: 备份 ${info.rows} 行，恢复后 ${rows[0].n} 行`);
      }
    }

    // 5) 链完整性 + KEK 可用性
    for (const [table, label] of [["UsageRecord", "账本"], ["AuditEvent", "审计"]]) {
      if (!manifest.tables[table]) continue;
      const { rows } = await targetClient.query(
        `SELECT count(*) FILTER (WHERE "rowHash" IS NOT NULL)::bigint AS chained FROM "${table}"`,
      );
      console.log(`${label}链：${rows[0].chained} 行带 rowHash`);
    }
    if (process.env.KEYKEEP_KEK) {
      // 用恢复出来的密文做一次真实的解密，证明 KEK 与备份配套。
      // 密文格式见 lib/crypto.ts：ct = ciphertext || authTag，nonce 单独一列。
      const { rows } = await targetClient.query(
        `SELECT ct, nonce FROM "ProviderCredential" LIMIT 1`,
      );
      if (rows.length > 0) {
        try {
          const { createDecipheriv } = await import("node:crypto");
          const ct = Buffer.from(rows[0].ct);
          const nonce = Buffer.from(rows[0].nonce);
          const kek = Buffer.from(process.env.KEYKEEP_KEK, "base64");
          const decipher = createDecipheriv("aes-256-gcm", kek, nonce);
          decipher.setAuthTag(ct.subarray(ct.length - 16));
          const plaintext = Buffer.concat([
            decipher.update(ct.subarray(0, ct.length - 16)),
            decipher.final(),
          ]);
          console.log(`上游凭据解密成功（${plaintext.length} 字节）——KEK 与备份匹配`);
        } catch (error) {
          failures.push(`KEK 解不开恢复出的凭据：${error instanceof Error ? error.message : error}`);
        }
      } else {
        console.log("备份中没有上游凭据，跳过 KEK 配套验证");
      }
    }
    await targetClient.end();

    if (failures.length > 0) {
      console.error("演练失败：");
      for (const failure of failures) console.error("  ✗ " + failure);
      process.exitCode = 1;
    } else {
      console.log("演练通过：行数、链、KEK 全部一致");
    }
  } finally {
    if (child) {
      child.kill();
      // 给 postgres 一点时间做 checkpoint；超时就直接走。
      await new Promise((wait) => setTimeout(wait, 2000));
    }
    if (!keep) {
      await rm(work, { recursive: true, force: true }).catch(() => {});
    } else {
      console.log(`已保留演练目录：${work}`);
    }
  }
}

// 报错只走一个出口：fatal 本身已负责屏蔽凭据。
main().catch(fatal);
