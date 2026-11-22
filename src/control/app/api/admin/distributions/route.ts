import type { NextRequest } from "next/server";
import { NextResponse } from "next/server";

import { requireRoles, requireWritable } from "@/lib/api-auth";
import { auditRequestId, recordAudit } from "@/lib/audit";
import { prisma } from "@/lib/db";
import {
  MAX_DESCRIPTION,
  normalizeAllowedModels,
  validateDistributionName,
} from "@/lib/distribution";
import { apiError, json } from "@/lib/http";

export const runtime = "nodejs";

/**
 * 老板侧：分发 API 的列表与新建。
 *
 * 「分发 API」= 老板定义的一个具名入口（名称 + 可用模型默认值），
 * 分配出去时在员工名下生成虚拟 Key。它本身不是凭据，不能直接拿去调 /v1。
 */
export async function GET(request: NextRequest) {
  const auth = await requireRoles(request, ["admin"]);
  if (auth instanceof NextResponse) return auth;

  const distributions = await prisma.distribution.findMany({
    orderBy: { createdAt: "desc" },
    take: 200,
    include: {
      keys: {
        select: { id: true, empId: true, revokedAt: true },
      },
      createdBy: { select: { id: true, account: true, displayName: true } },
    },
  });

  return json({
    distributions: distributions.map((row) => {
      const activeKeys = row.keys.filter((key) => key.revokedAt === null);
      return {
        id: row.id,
        name: row.name,
        description: row.description,
        allowedModels: row.allowedModels,
        disabled: row.disabled,
        revokedAt: row.revokedAt,
        createdBy: row.createdBy,
        createdAt: row.createdAt,
        updatedAt: row.updatedAt,
        // 已分配到多少个员工、共多少把仍在用的 Key。员工可能一人多把。
        assignedEmployees: new Set(activeKeys.map((key) => key.empId)).size,
        activeKeys: activeKeys.length,
      };
    }),
  });
}

export async function POST(request: NextRequest) {
  const auth = await requireRoles(request, ["admin"]);
  if (auth instanceof NextResponse) return auth;
  const readOnly = await requireWritable(request, auth.sub);
  if (readOnly) return readOnly;

  let body: { name?: unknown; description?: unknown; allowedModels?: unknown };
  try {
    body = (await request.json()) as typeof body;
  } catch {
    return apiError("invalid_request", 400);
  }

  const name = typeof body.name === "string" ? body.name.trim() : "";
  const nameError = validateDistributionName(name);
  if (nameError) return apiError(nameError, 400, { field: "name" });

  const description = typeof body.description === "string" ? body.description.trim() : "";
  if (description.length > MAX_DESCRIPTION) {
    return apiError("description_too_long", 400, { field: "description" });
  }
  const allowedModels = normalizeAllowedModels(body.allowedModels);

  const existing = await prisma.distribution.findUnique({ where: { name } });
  if (existing) return apiError("conflict", 409, { field: "name" });

  const created = await prisma.distribution.create({
    data: {
      name,
      description,
      allowedModels,
      createdById: auth.sub,
    },
  });

  await recordAudit({
    actorId: auth.sub,
    action: "distribution.create",
    detail: {
      distributionId: created.id,
      name: created.name,
      allowedModels: created.allowedModels,
    },
    requestId: auditRequestId(request),
  });

  return json(
    {
      distribution: {
        id: created.id,
        name: created.name,
        description: created.description,
        allowedModels: created.allowedModels,
        disabled: created.disabled,
        revokedAt: created.revokedAt,
        createdAt: created.createdAt,
        updatedAt: created.updatedAt,
        assignedEmployees: 0,
        activeKeys: 0,
      },
    },
    201,
  );
}
