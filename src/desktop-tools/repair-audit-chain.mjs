// 修复 0.1.0-test.6 及更早版本留下的审计链时间偏移。
//
// 背景（2026-11-08 抓包确认）：
//   @prisma/adapter-pg 的 formatDateTime() 只拼 UTC 墙钟、**不带时区后缀**
//   （线上值形如 `2026-11-08 06:36:23.302`）。服务器 timezone=Asia/Shanghai 时
//   PostgreSQL 按会话时区解释这个裸 timestamp，写入被减 8 小时；读回来时后缀又被
//   改写成 +00:00 而不动钟面，读取被加 8 小时。控制面按「读回来的值」算摘要，
//   于是历史控制面行满足：stored_at + 8h == 摘要里用的时间。
//   网关行由 pgx 直连写入，at 正确、摘要按正确时间算，不受影响——但它们夹在
//   中间，让审计页同时出现「未链空洞」和「时间乱序」。
//
// 修复做两件事，顺序不能反：
//   1) 把受影响的 at 加回 8 小时；
//   2) 从受影响的第一行起重算到表尾的 prevHash/rowHash。链是前后耦合的，
//      只改中间一行会让它之后的每一行都 prev_mismatch，所以重算范围必须到表尾。
//
// 安全性：
//   - 默认 dry-run；--apply 才写库，且在同一事务内重新识别；
//   - 识别用「原值复算」与「加 8h 复算」两次比对。任何已链行两者都不成立就整体
//     中止——说明库里有第三种成因，脚本不做猜测；
//   - 只改 at / prevHash / rowHash / hashAlgo，不碰 detail、action、actorId；
//   - 输出不含任何 detail 内容与凭据。
//
// 用法（在 src/desktop-tools 目录下，依赖随安装包分发）：
//   DATABASE_URL=... node repair-audit-chain.mjs            # 预演
//   DATABASE_URL=... node repair-audit-chain.mjs --apply    # 执行

import { createHash } from "node:crypto";

import pg from "pg";

import { fatal } from "./fatal.mjs";

const { Client } = pg;

const GENESIS = "0".repeat(64);
const OFFSET_MS = 8 * 3600 * 1000;
const CHAIN_LOCK = 7411003;

/**
 * 与 src/control/lib/audit-chain.ts 的 canonicalJson 逐字对齐。
 * 本脚本只在旧版本遗留数据上跑；控制面那一份是规则的事实源。
 */
function canonicalJson(value) {
  if (value === null) return "null";
  switch (typeof value) {
    case "boolean":
      return value ? "true" : "false";
    case "number":
      return Object.is(value, -0) ? "0" : String(value);
    case "string":
      return JSON.stringify(value);
  }
  if (Array.isArray(value)) {
    return `[${value.map((item) => (item === undefined ? "null" : canonicalJson(item))).join(",")}]`;
  }
  const parts = [];
  for (const key of Object.keys(value).sort()) {
    if (value[key] === undefined) continue;
    parts.push(`${JSON.stringify(key)}:${canonicalJson(value[key])}`);
  }
  return `{${parts.join(",")}}`;
}

/** 摘要只认库里的值；at 用毫秒（与控制面 Date 的精度一致）。 */
function digest(prevHash, row) {
  return createHash("sha256")
    .update(
      [
        prevHash,
        String(row.seq),
        String(row.at.getTime()),
        row.actorId ?? "",
        row.action,
        canonicalJson(row.detail),
        row.requestId ?? "",
      ].join("\x1f"),
      "utf8",
    )
    .digest("hex");
}

/**
 * 把已链行分成三类：
 *   targetable —— rowHash 等于「at + 8h」算出来的摘要（旧版控制面写的）
 *   clean      —— rowHash 等于「at 原值」算出来的摘要（本来是对的）
 *   broken     —— 两者都不等（未知成因，必须中止）
 */
function classify(rows) {
  const targetable = [];
  const clean = [];
  const broken = [];
  for (const row of rows) {
    if (!row.rowHash) continue;
    if (digest(row.prevHash ?? "", row) === row.rowHash) {
      clean.push(row);
      continue;
    }
    const shifted = { ...row, at: new Date(row.at.getTime() + OFFSET_MS) };
    if (digest(row.prevHash ?? "", shifted) === row.rowHash) {
      targetable.push(row);
      continue;
    }
    broken.push(row);
  }
  return { targetable, clean, broken };
}

const ROW_SQL = `SELECT seq, at, "actorId", action, detail, "requestId", "prevHash", "rowHash", "hashAlgo"
                   FROM "AuditEvent" WHERE "rowHash" IS NOT NULL ORDER BY seq`;

async function loadChained(executor) {
  const { rows } = await executor.query(ROW_SQL);
  return rows;
}

async function main() {
  const apply = process.argv.includes("--apply");
  const connectionString = process.env.DATABASE_URL;
  if (!connectionString) {
    fatal("DATABASE_URL 未设置");
  }

  const client = new Client({ connectionString });
  await client.connect();
  try {
    const before = await loadChained(client);
    const { targetable, clean, broken } = classify(before);
    console.log(
      `已链行 ${before.length} | 需校正 ${targetable.length} | 原值正确 ${clean.length} | 无法解释 ${broken.length}`,
    );

    if (broken.length > 0) {
      console.error(
        "存在既非原值、也非 8 小时偏移的行；脚本不做猜测，已中止。涉及 seq: " +
          broken.slice(0, 10).map((row) => String(row.seq)).join(", "),
      );
      process.exitCode = 1;
      return;
    }
    if (targetable.length === 0) {
      console.log("没有需要修复的行。");
      return;
    }
    const startSeq = String(targetable[0].seq);
    console.log(`受影响区间：seq ${startSeq} … 表尾（重算必须覆盖到表尾）`);

    if (!apply) {
      console.log("预演结束。加 --apply 执行（会在同一事务内重新识别）。");
      return;
    }

    await client.query("BEGIN");
    try {
      // 与网关/控制面同一把审计链锁：修复期间不允许有新事件插进链尾。
      await client.query("SELECT pg_advisory_xact_lock($1)", [CHAIN_LOCK]);

      const rows = await loadChained(client);
      const recheck = classify(rows);
      if (recheck.broken.length > 0) {
        throw new Error(`事务内复核发现 ${recheck.broken.length} 行无法解释，已回滚`);
      }
      if (recheck.targetable.length === 0) {
        console.log("事务内复核：不需要修复，已回滚");
        await client.query("ROLLBACK");
        return;
      }
      const fromSeq = String(recheck.targetable[0].seq);

      // 1) 校正 at。只动被识别为偏移的行。
      const shiftedSeqs = recheck.targetable.map((row) => String(row.seq));
      const shifted = await client.query(
        `UPDATE "AuditEvent" SET at = at + interval '8 hours' WHERE seq = ANY($1::bigint[])`,
        [shiftedSeqs],
      );

      // 2) 从受影响的第一行起重算到表尾。
      const { rows: chain } = await client.query(
        `SELECT seq, at, "actorId", action, detail, "requestId"
           FROM "AuditEvent" WHERE seq >= $1::bigint ORDER BY seq`,
        [fromSeq],
      );
      const { rows: previous } = await client.query(
        `SELECT "rowHash" FROM "AuditEvent"
          WHERE seq < $1::bigint AND "rowHash" IS NOT NULL ORDER BY seq DESC LIMIT 1`,
        [fromSeq],
      );
      let prevHash = previous[0]?.rowHash ?? GENESIS;
      for (const row of chain) {
        const rowHash = digest(prevHash, row);
        await client.query(
          `UPDATE "AuditEvent" SET "prevHash" = $1, "rowHash" = $2, "hashAlgo" = 'a1' WHERE seq = $3::bigint`,
          [prevHash, rowHash, String(row.seq)],
        );
        prevHash = rowHash;
      }
      await client.query("COMMIT");
      console.log(`已校正 ${shifted.rowCount} 行时间，重算 ${chain.length} 行链摘要`);
    } catch (error) {
      await client.query("ROLLBACK").catch(() => {});
      throw error;
    }

    const after = await loadChained(client);
    const result = classify(after);
    console.log(
      `复核：已链行 ${after.length} | 需校正 ${result.targetable.length} | 无法解释 ${result.broken.length}`,
    );
    if (result.targetable.length > 0 || result.broken.length > 0) {
      process.exitCode = 1;
    }
  } finally {
    await client.end().catch(() => {});
  }
}

// 报错只走一个出口：fatal 本身已负责屏蔽凭据。
main().catch(fatal);
