import type { NextRequest } from "next/server";
import { NextResponse } from "next/server";

import { requireRoles, requireWritable } from "@/lib/api-auth";
import { auditRequestId, recordAudit } from "@/lib/audit";
import { prisma } from "@/lib/db";
import { MAX_LABEL } from "@/lib/distribution";
import { apiError, json } from "@/lib/http";
import { issueKey } from "@/lib/keys";

export const runtime = "nodejs";

export async function GET(request: NextRequest) {
  const auth = await requireRoles(request, ["admin"]);
  if (auth instanceof NextResponse) return auth;

  const keys = await prisma.apiKey.findMany({
    include: {
      emp: { select: { id: true, account: true, displayName: true } },
      distribution: { select: { id: true, name: true } },
    },
    orderBy: { createdAt: "desc" },
    take: 200,
  });

  const limits = await prisma.limit.findMany({
    where: { scope: "key", scopeId: { in: keys.map((key) => key.id) } },
  });
  const limitsByKey = new Map<string, Record<string, number>>();
  for (const row of limits) {
    const current = limitsByKey.get(row.scopeId) ?? {};
    current[row.dim] = Number(row.limitValue);
    limitsByKey.set(row.scopeId, current);
  }

  return json({
    keys: keys.map((key) => ({
      id: key.id,
      prefix4: key.prefix4,
      fp6: key.fp6,
      emp: key.emp,
      label: key.label,
      distribution: key.distribution,
      allowedModels: key.allowedModels,
      limits: limitsByKey.get(key.id) ?? {},
      revokedAt: key.revokedAt,
      createdAt: key.createdAt,
    })),
  });
}

export async function POST(request: NextRequest) {
  const auth = await requireRoles(request, ["admin"]);
  if (auth instanceof NextResponse) return auth;
  const readOnly = await requireWritable(request, auth.sub);
  if (readOnly) return readOnly;

  let body: { empId?: string; count?: number; allowedModels?: unknown; label?: unknown; distributionId?: unknown };
  try {
    body = (await request.json()) as typeof body;
  } catch {
    return apiError("invalid_request", 400);
  }
  if (!body.empId) return apiError("invalid_request", 400);

  const count = Math.min(Math.max(Math.floor(Number(body.count ?? 1)), 1), 100);
  const allowedModels = Array.isArray(body.allowedModels)
    ? body.allowedModels.filter((value): value is string => typeof value === "string").slice(0, 100)
    : [];
  const label = typeof body.label === "string" ? body.label.trim().slice(0, MAX_LABEL) : "";

  // 可选的归属：老板在「分发 API」页以外手工签发时，也可以挂到某个分发下便于归类。
  // 不传就是无归属的历史行为。
  let distributionId: string | null = null;
  if (typeof body.distributionId === "string" && body.distributionId) {
    const distribution = await prisma.distribution.findUnique({ where: { id: body.distributionId } });
    if (!distribution || distribution.revokedAt) {
      return apiError("invalid_request", 400, { field: "distributionId" });
    }
    distributionId = distribution.id;
  }

  const emp = await prisma.user.findUnique({ where: { id: body.empId } });
  if (!emp || emp.disabled) return apiError("invalid_request", 400);

  const issued: Array<{ id: string; plaintext: string; prefix4: string; fp6: string }> = [];
  for (let i = 0; i < count; i += 1) {
    const key = issueKey();
    const row = await prisma.apiKey.create({
      data: {
        keyHash: new Uint8Array(key.keyHash),
        prefix4: key.prefix4,
        fp6: key.fp6,
        empId: emp.id,
        allowedModels,
        distributionId,
        label,
      },
    });
    issued.push({ id: row.id, plaintext: key.plaintext, prefix4: key.prefix4, fp6: key.fp6 });
  }

  await recordAudit({
    actorId: auth.sub,
    action: "key.issue",
    detail: {
      empId: emp.id,
      count: issued.length,
      keyIds: issued.map((key) => key.id),
      distributionId,
    },
    requestId: auditRequestId(request),
  });

  return json({ keys: issued }, 201);
}
