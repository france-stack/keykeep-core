import { Prisma } from "@prisma/client";
import type { NextRequest } from "next/server";
import { NextResponse } from "next/server";

import { requireRoles } from "@/lib/api-auth";
import { prisma } from "@/lib/db";
import { apiError, json } from "@/lib/http";
import { num } from "@/lib/usage";
import { isUuid } from "@/lib/uuid";

export const runtime = "nodejs";

const DEFAULT_PAGE_SIZE = 30;
const MAX_PAGE_SIZE = 100;
const STATES = new Set(["in_progress", "completed", "failed", "outcome_unknown"]);

type Row = {
  id: string;
  key_id: string;
  emp_id: string;
  request_id: string;
  path: string;
  model: string;
  stream: boolean;
  status: string;
  status_code: number | null;
  error_code: string;
  created_epoch: bigint;
  updated_epoch: bigint;
  expires_epoch: bigint;
};

// GET /api/admin/claims —— 查询请求幂等声明（T-21 的 outcome_unknown 可查询入口）。
export async function GET(request: NextRequest) {
  const auth = await requireRoles(request, ["admin"]);
  if (auth instanceof NextResponse) return auth;

  const params = request.nextUrl.searchParams;
  const clauses: Prisma.Sql[] = [];

  const requestId = params.get("requestId");
  if (requestId) clauses.push(Prisma.sql`"requestId" = ${requestId}`);

  const state = params.get("state");
  if (state) {
    if (!STATES.has(state)) return json({ total: 0, page: 1, pageSize: DEFAULT_PAGE_SIZE, claims: [] });
    clauses.push(Prisma.sql`status = ${state}`);
  }

  const keyId = params.get("keyId");
  if (keyId) {
    // 非法 UUID 直接进 ::uuid 会让 PG 抛错变 500：前置校验，400 拒绝。
    if (!isUuid(keyId)) return apiError("invalid_request", 400, { reason: "keyId" });
    clauses.push(Prisma.sql`"keyId" = ${keyId}::uuid`);
  }

  const where = clauses.length > 0 ? Prisma.sql`WHERE ${Prisma.join(clauses, " AND ")}` : Prisma.empty;
  const page = Math.max(1, Math.floor(Number(params.get("page") ?? 1)) || 1);
  const pageSize = Math.min(
    MAX_PAGE_SIZE,
    Math.max(1, Math.floor(Number(params.get("pageSize") ?? DEFAULT_PAGE_SIZE)) || DEFAULT_PAGE_SIZE),
  );

  const [countRows, rows] = await Promise.all([
    prisma.$queryRaw<Array<{ total: bigint }>>(
      Prisma.sql`SELECT count(*)::bigint AS total FROM "RequestClaim" ${where}`,
    ),
    prisma.$queryRaw<Row[]>(Prisma.sql`
      SELECT id::text, "keyId"::text AS key_id, "empId"::text AS emp_id, "requestId" AS request_id,
             path, model, stream, status, "statusCode" AS status_code, "errorCode" AS error_code,
             (extract(epoch from "createdAt"))::bigint AS created_epoch,
             (extract(epoch from "updatedAt"))::bigint AS updated_epoch,
             (extract(epoch from "expiresAt"))::bigint AS expires_epoch
        FROM "RequestClaim"
        ${where}
       ORDER BY "createdAt" DESC
       LIMIT ${pageSize} OFFSET ${(page - 1) * pageSize}`),
  ]);

  return json({
    total: num(countRows[0]?.total),
    page,
    pageSize,
    claims: rows.map((row) => ({
      id: row.id,
      keyId: row.key_id,
      empId: row.emp_id,
      requestId: row.request_id,
      path: row.path,
      model: row.model,
      stream: row.stream,
      state: row.status,
      statusCode: row.status_code,
      errorCode: row.error_code,
      createdAt: new Date(Number(row.created_epoch) * 1000).toISOString(),
      updatedAt: new Date(Number(row.updated_epoch) * 1000).toISOString(),
      expiresAt: new Date(Number(row.expires_epoch) * 1000).toISOString(),
    })),
  });
}
