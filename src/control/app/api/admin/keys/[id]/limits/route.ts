import type { NextRequest } from "next/server";
import { NextResponse } from "next/server";

import { requireRoles, requireWritable } from "@/lib/api-auth";
import { auditRequestId, recordAudit } from "@/lib/audit";
import { prisma } from "@/lib/db";
import { apiError, json } from "@/lib/http";

export const runtime = "nodejs";

// 限流维度：rpm=每分钟请求数；tpm/itpm/otpm=每分钟 token 总量/输入/输出；
// concurrency=同时进行的请求数（由网关 overload 中间件执行）。
// 值 0 或省略表示清除该维度的限制。
const DIMS = ["rpm", "tpm", "itpm", "otpm", "concurrency"] as const;
type Dim = (typeof DIMS)[number];

const MAX_LIMIT = 1_000_000_000;

type Body = Partial<Record<Dim, unknown>>;

export async function PUT(request: NextRequest, context: { params: Promise<{ id: string }> }) {
  const auth = await requireRoles(request, ["admin"]);
  if (auth instanceof NextResponse) return auth;
  const readOnly = await requireWritable(request, auth.sub);
  if (readOnly) return readOnly;

  const { id } = await context.params;
  const key = await prisma.apiKey.findUnique({
    where: { id },
    
  });
  if (!key) return apiError("not_found", 404);

  let body: Body;
  try {
    body = (await request.json()) as Body;
  } catch {
    return apiError("invalid_request", 400);
  }

  const values: Array<{ dim: Dim; value: number }> = [];
  for (const dim of DIMS) {
    if (!(dim in body)) continue;
    const raw = body[dim];
    if (raw === null || raw === "" || raw === undefined) {
      values.push({ dim, value: 0 });
      continue;
    }
    const value = Math.floor(Number(raw));
    if (!Number.isFinite(value) || value < 0 || value > MAX_LIMIT) {
      return apiError("invalid_request", 400, { field: dim });
    }
    values.push({ dim, value });
  }
  if (values.length === 0) return apiError("invalid_request", 400, { field: "dims" });

  for (const { dim, value } of values) {
    if (value > 0) {
      await prisma.limit.upsert({
        where: { scope_scopeId_dim: { scope: "key", scopeId: id, dim } },
        create: { scope: "key", scopeId: id, dim, limitValue: BigInt(value), period: "1m" },
        update: { limitValue: BigInt(value), period: "1m" },
      });
    } else {
      await prisma.limit.deleteMany({ where: { scope: "key", scopeId: id, dim } });
    }
  }

  const limits = await prisma.limit.findMany({ where: { scope: "key", scopeId: id } });
  // 全部维度清空时记"删除限流"，而不是"设置 0"。
  await recordAudit({
    actorId: auth.sub,
    action: limits.length === 0 ? "limit.delete" : "limit.upsert",
    detail: {
      keyId: id,
      dims: Object.fromEntries(values.map(({ dim, value }) => [dim, value])),
    },
    requestId: auditRequestId(request),
  });
  return json({
    limits: Object.fromEntries(limits.map((row) => [row.dim, Number(row.limitValue)])),
  });
}
