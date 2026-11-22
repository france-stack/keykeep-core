import { Prisma } from "@prisma/client";
import type { NextRequest } from "next/server";
import { NextResponse } from "next/server";

import { requireRoles } from "@/lib/api-auth";
import { prisma } from "@/lib/db";
import { apiError, json } from "@/lib/http";
import { num } from "@/lib/usage";
import { isUuid } from "@/lib/uuid";

export const runtime = "nodejs";

const DEFAULT_PAGE_SIZE = 20;
const MAX_PAGE_SIZE = 100;

function parseDate(value: string | null, endOfDay = false): Date | null {
  if (!value) return null;
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return null;
  if (endOfDay && value.length === 10) date.setDate(date.getDate() + 1);
  return date;
}

type Row = {
  rid: string;
  ts_epoch: bigint;
  account: string;
  display_name: string;
  prefix4: string;
  fp6: string;
  provider: string;
  model_req: string;
  model_served: string;
  input_tokens: bigint;
  output_tokens: bigint;
  cache_read: bigint;
  cost_cents: bigint;
  status: string;
  stream: boolean;
  latency_ms: number | null;
  unpriced: boolean;
  price_version: string;
};

export async function GET(request: NextRequest) {
  const auth = await requireRoles(request, []);
  if (auth instanceof NextResponse) return auth;

  const params = request.nextUrl.searchParams;
  const clauses: Prisma.Sql[] = [];

  if (auth.role !== "admin") {
    clauses.push(Prisma.sql`r."empId" = ${auth.sub}::uuid`);
  }

  const from = parseDate(params.get("from"));
  const to = parseDate(params.get("to"), true);
  if (from) clauses.push(Prisma.sql`r.ts >= ${from}`);
  if (to) clauses.push(Prisma.sql`r.ts < ${to}`);

  // empId 过滤是老板的「下钻」能力；员工已经被上面钉死成自己，任何 empId 都无效。
  const empId = params.get("empId");
  if (empId && auth.role === "admin") {
    // 非法 UUID 直接进 ::uuid 会让 PG 抛错变 500：前置校验，400 拒绝。
    if (!isUuid(empId)) return apiError("invalid_request", 400, { reason: "empId" });
    clauses.push(Prisma.sql`r."empId" = ${empId}::uuid`);
  }

  const model = params.get("model");
  if (model) clauses.push(Prisma.sql`r."modelServed" ILIKE ${"%" + model + "%"}`);

  const status = params.get("status");
  if (status === "failed") clauses.push(Prisma.sql`r.failed = true`);
  else if (status === "success") clauses.push(Prisma.sql`r.failed = false`);

  const where = clauses.length > 0 ? Prisma.sql`WHERE ${Prisma.join(clauses, " AND ")}` : Prisma.empty;

  const page = Math.max(1, Math.floor(Number(params.get("page") ?? 1)) || 1);
  const pageSize = Math.min(
    MAX_PAGE_SIZE,
    Math.max(1, Math.floor(Number(params.get("pageSize") ?? DEFAULT_PAGE_SIZE)) || DEFAULT_PAGE_SIZE),
  );
  const offset = (page - 1) * pageSize;

  const [countRows, rows] = await Promise.all([
    prisma.$queryRaw<Array<{ total: bigint }>>(
      Prisma.sql`SELECT count(*)::bigint AS total FROM "UsageRecord" r ${where}`,
    ),
    prisma.$queryRaw<Row[]>(Prisma.sql`
      SELECT r.rid,
             (extract(epoch from r.ts))::bigint AS ts_epoch,
             u.account, u."displayName" AS display_name,
             k.prefix4, k.fp6,
             r.provider, r."modelReq" AS model_req, r."modelServed" AS model_served,
             r."inputTokens" AS input_tokens, r."outputTokens" AS output_tokens,
             r."cacheReadInputTokens" AS cache_read, r."costCents" AS cost_cents,
             r.status, r.stream, r."latencyMs" AS latency_ms, r.unpriced,
             r."priceVersion" AS price_version
        FROM "UsageRecord" r
        JOIN "User" u ON u.id = r."empId"
        JOIN "ApiKey" k ON k.id = r."keyId"
        ${where}
       ORDER BY r.seq DESC
       LIMIT ${pageSize} OFFSET ${offset}`),
  ]);

  return json({
    total: num(countRows[0]?.total),
    page,
    pageSize,
    requests: rows.map((row) => ({
      rid: row.rid,
      ts: new Date(Number(row.ts_epoch) * 1000).toISOString(),
      emp: { account: row.account, displayName: row.display_name },
      key: `${row.prefix4}…${row.fp6}`,
      provider: row.provider,
      modelReq: row.model_req,
      modelServed: row.model_served,
      inputTokens: num(row.input_tokens),
      outputTokens: num(row.output_tokens),
      cacheReadTokens: num(row.cache_read),
      costCents: num(row.cost_cents),
      status: row.status,
      stream: row.stream,
      latencyMs: row.latency_ms,
      unpriced: row.unpriced,
      priceVersion: row.price_version,
    })),
  });
}
