import type { NextRequest } from "next/server";
import { NextResponse } from "next/server";

import { requireRoles, requireWritable } from "@/lib/api-auth";
import { auditRequestId, recordAudit } from "@/lib/audit";
import { prisma } from "@/lib/db";
import { apiError, json } from "@/lib/http";

export const runtime = "nodejs";

const DIMS = ["rpm", "tpm", "itpm", "otpm"] as const;
type Dim = (typeof DIMS)[number];
const MAX_LIMIT = 1_000_000_000;

function readLimits(rows: Array<{ dim: string; limitValue: bigint }>) {
  const result: Record<string, number> = {};
  for (const row of rows) result[row.dim] = Number(row.limitValue);
  return result;
}

export async function GET(request: NextRequest) {
  const auth = await requireRoles(request, ["admin"]);
  if (auth instanceof NextResponse) return auth;

  const rows = await prisma.limit.findMany({
    where: { scope: "global", scopeId: "global" },
    select: { dim: true, limitValue: true },
  });
  return json({ limits: readLimits(rows) });
}

export async function PUT(request: NextRequest) {
  const auth = await requireRoles(request, ["admin"]);
  if (auth instanceof NextResponse) return auth;
  const readOnly = await requireWritable(request, auth.sub);
  if (readOnly) return readOnly;

  let body: Partial<Record<Dim, unknown>>;
  try {
    body = (await request.json()) as typeof body;
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
        where: { scope_scopeId_dim: { scope: "global", scopeId: "global", dim } },
        create: { scope: "global", scopeId: "global", dim, limitValue: BigInt(value), period: "1m" },
        update: { limitValue: BigInt(value), period: "1m" },
      });
    } else {
      await prisma.limit.deleteMany({ where: { scope: "global", scopeId: "global", dim } });
    }
  }

  const rows = await prisma.limit.findMany({
    where: { scope: "global", scopeId: "global" },
    select: { dim: true, limitValue: true },
  });
  await recordAudit({
    actorId: auth.sub,
    action: rows.length === 0 ? "limit.delete" : "limit.upsert",
    detail: {
      scope: "global",
      dims: Object.fromEntries(values.map(({ dim, value }) => [dim, value])),
    },
    requestId: auditRequestId(request),
  });
  return json({ limits: readLimits(rows) });
}
