import { Prisma } from "@prisma/client";
import type { NextRequest } from "next/server";
import { NextResponse } from "next/server";

import { requireRoles } from "@/lib/api-auth";
import { toCsv, USAGE_EXPORT_FIELDS } from "@/lib/csv";
import { prisma } from "@/lib/db";
import { apiError } from "@/lib/http";
import { isUuid } from "@/lib/uuid";

export const runtime = "nodejs";

const MAX_ROWS = 50_000;

function parseDate(value: string | null, endOfDay = false): Date | null {
  if (!value) return null;
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return null;
  if (endOfDay && value.length === 10) date.setDate(date.getDate() + 1);
  return date;
}

type Row = {
  ts_epoch: bigint;
  account: string;
  display_name: string;
  dept_name: string | null;
  prefix4: string;
  fp6: string;
  provider: string;
  model_req: string;
  model_served: string;
  price_version: string;
  input_tokens: bigint;
  output_tokens: bigint;
  cache_read: bigint;
  cache_write: bigint;
  cost_cents: bigint;
  status: string;
  stream: boolean;
  latency_ms: number | null;
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

  const empId = params.get("empId");
  if (empId && auth.role !== "employee") {
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

  const rows = await prisma.$queryRaw<Row[]>(Prisma.sql`
    SELECT (extract(epoch from r.ts))::bigint AS ts_epoch,
           u.account, u."displayName" AS display_name,
           k.prefix4, k.fp6,
           r.provider, r."modelReq" AS model_req, r."modelServed" AS model_served,
           r."priceVersion" AS price_version,
           r."inputTokens" AS input_tokens, r."outputTokens" AS output_tokens,
           r."cacheReadInputTokens" AS cache_read, r."cacheCreationInputTokens" AS cache_write,
           r."costCents" AS cost_cents, r.status, r.stream, r."latencyMs" AS latency_ms
      FROM "UsageRecord" r
      JOIN "User" u ON u.id = r."empId"
      JOIN "ApiKey" k ON k.id = r."keyId"
      ${where}
     ORDER BY r.seq DESC
     LIMIT ${MAX_ROWS}`);

  if (rows.length >= MAX_ROWS) {
    return apiError("export_too_large", 413, { maxRows: MAX_ROWS });
  }

  const csv = toCsv(
    [...USAGE_EXPORT_FIELDS],
    rows.map((row) => [
      new Date(Number(row.ts_epoch) * 1000).toISOString(),
      row.account,
      row.display_name,
      `${row.prefix4}…${row.fp6}`, // 只导出指纹，绝不导出 Key 明文或哈希
      row.provider,
      row.model_req,
      row.model_served,
      row.price_version,
      row.input_tokens,
      row.output_tokens,
      row.cache_read,
      row.cache_write,
      row.cost_cents,
      row.status,
      row.stream,
      row.latency_ms,
    ]),
  );

  const stamp = new Date().toISOString().slice(0, 10);
  return new NextResponse(csv, {
    status: 200,
    headers: {
      "Content-Type": "text/csv; charset=utf-8",
      "Content-Disposition": `attachment; filename="usage-${stamp}.csv"`,
      "Cache-Control": "no-store",
    },
  });
}
