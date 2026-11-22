import { Prisma } from "@prisma/client";
import type { NextRequest } from "next/server";
import { NextResponse } from "next/server";

import { requireRoles } from "@/lib/api-auth";
import { prisma } from "@/lib/db";
import { json } from "@/lib/http";
import { chainHash, GENESIS_HASH, type LedgerRow } from "@/lib/ledger-hash";

export const runtime = "nodejs";

const BATCH = 1000;
const MAX_ROWS = 200_000;

type RawRow = LedgerRow & { seq: bigint; prevHash: string; rowHash: string };

// 时间戳直接取数据库 epoch 秒：Prisma 适配器返回的 Date 与 timestamptz 有时区偏移，
// 用它复算会与网关写入值不一致。
const COLUMNS = Prisma.sql`
  rid, (extract(epoch from ts))::bigint AS "tsSeconds", "keyId", "empId", "deptId",
  provider, alias, "modelReq", "modelServed", "authId", "priceVersion", unpriced,
  "inputTokens", "outputTokens", "cacheCreationInputTokens", "cacheReadInputTokens",
  "costCents", status, "requestId", stream, failed, "prevHash", "rowHash", seq`;

// GET /api/admin/usage/verify —— 从链首逐行复算，定位第一处断链。
export async function GET(request: NextRequest) {
  const auth = await requireRoles(request, ["admin"]);
  if (auth instanceof NextResponse) return auth;

  let prev = GENESIS_HASH;
  let cursor: bigint | null = null;
  let checked = 0;

  while (checked < MAX_ROWS) {
    const rows: RawRow[] = await prisma.$queryRaw<RawRow[]>(
      Prisma.sql`SELECT ${COLUMNS} FROM "UsageRecord"
                  ${cursor === null ? Prisma.empty : Prisma.sql`WHERE seq > ${cursor}`}
                  ORDER BY seq
                  LIMIT ${BATCH}`,
    );
    if (rows.length === 0) break;

    for (const row of rows) {
      if (row.prevHash !== prev) {
        return json({ ok: false, checked, brokenAt: { rid: row.rid, seq: Number(row.seq) }, reason: "prev_hash_mismatch" });
      }
      if (chainHash(prev, row) !== row.rowHash) {
        return json({ ok: false, checked, brokenAt: { rid: row.rid, seq: Number(row.seq) }, reason: "row_hash_mismatch" });
      }
      prev = row.rowHash;
      cursor = row.seq;
      checked += 1;
    }
    if (rows.length < BATCH) break;
  }

  return json({ ok: true, checked, truncated: checked >= MAX_ROWS, head: prev });
}
