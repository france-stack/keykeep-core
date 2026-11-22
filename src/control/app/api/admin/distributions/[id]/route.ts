import type { NextRequest } from "next/server";
import { NextResponse } from "next/server";

import { requireRoles, requireWritable } from "@/lib/api-auth";
import { auditRequestId, recordAudit } from "@/lib/audit";
import { prisma } from "@/lib/db";
import { MAX_DESCRIPTION, describeModels, normalizeAllowedModels, validateDistributionName } from "@/lib/distribution";
import { apiError, json } from "@/lib/http";

export const runtime = "nodejs";

/**
 * 分发详情：这个分发给过谁、每人拿到了哪些模型、哪些 Key 已撤销。
 * 老板点开一个分发时用这一个接口，避免前端再去翻全量 Key 列表。
 */
export async function GET(request: NextRequest, context: { params: Promise<{ id: string }> }) {
  const auth = await requireRoles(request, ["admin"]);
  if (auth instanceof NextResponse) return auth;

  const { id } = await context.params;
  const distribution = await prisma.distribution.findUnique({
    where: { id },
    include: {
      createdBy: { select: { id: true, account: true, displayName: true } },
      keys: {
        orderBy: { createdAt: "desc" },
        select: {
          id: true,
          prefix4: true,
          fp6: true,
          label: true,
          allowedModels: true,
          revokedAt: true,
          createdAt: true,
          emp: { select: { id: true, account: true, displayName: true, disabled: true } },
        },
      },
    },
  });
  if (!distribution) return apiError("not_found", 404);

  const keys = distribution.keys.map((key) => ({
    id: key.id,
    fingerprint: `${key.prefix4}…${key.fp6}`,
    label: key.label,
    allowedModels: key.allowedModels,
    modelsText: describeModels(key.allowedModels),
    emp: key.emp,
    revokedAt: key.revokedAt,
    createdAt: key.createdAt,
  }));

  return json({
    distribution: {
      id: distribution.id,
      name: distribution.name,
      description: distribution.description,
      allowedModels: distribution.allowedModels,
      modelsText: describeModels(distribution.allowedModels),
      disabled: distribution.disabled,
      revokedAt: distribution.revokedAt,
      createdBy: distribution.createdBy,
      createdAt: distribution.createdAt,
      updatedAt: distribution.updatedAt,
    },
    keys,
  });
}

/**
 * 改分发 API。
 *
 * 模型名单默认**只改模板、不回刷**已分配出去的 Key：那批 Key 已经连人带密码
 * 交给员工了，悄悄改掉白名单等于事后更改已交付的权限。要一起改必须显式传
 * syncModels: true，此时把仍在用的 Key 的 allowedModels 一并覆盖（响应里回
 * 同步了几把，便于老板确认影响面）。
 */
export async function PATCH(request: NextRequest, context: { params: Promise<{ id: string }> }) {
  const auth = await requireRoles(request, ["admin"]);
  if (auth instanceof NextResponse) return auth;
  const readOnly = await requireWritable(request, auth.sub);
  if (readOnly) return readOnly;

  const { id } = await context.params;
  const current = await prisma.distribution.findUnique({ where: { id } });
  if (!current) return apiError("not_found", 404);

  let body: {
    name?: unknown;
    description?: unknown;
    allowedModels?: unknown;
    disabled?: unknown;
    syncModels?: unknown;
  };
  try {
    body = (await request.json()) as typeof body;
  } catch {
    return apiError("invalid_request", 400);
  }

  const data: {
    name?: string;
    description?: string;
    allowedModels?: string[];
    disabled?: boolean;
  } = {};

  if (body.name !== undefined) {
    const name = typeof body.name === "string" ? body.name.trim() : "";
    const nameError = validateDistributionName(name);
    if (nameError) return apiError(nameError, 400, { field: "name" });
    if (name !== current.name) {
      const clash = await prisma.distribution.findUnique({ where: { name } });
      if (clash) return apiError("conflict", 409, { field: "name" });
    }
    data.name = name;
  }

  if (body.description !== undefined) {
    const description = typeof body.description === "string" ? body.description.trim() : "";
    if (description.length > MAX_DESCRIPTION) {
      return apiError("description_too_long", 400, { field: "description" });
    }
    data.description = description;
  }

  if (body.allowedModels !== undefined) {
    data.allowedModels = normalizeAllowedModels(body.allowedModels);
  }

  if (body.disabled !== undefined) {
    if (typeof body.disabled !== "boolean") return apiError("invalid_request", 400, { field: "disabled" });
    data.disabled = body.disabled;
  }

  if (Object.keys(data).length === 0) return apiError("invalid_request", 400);

  const updated = await prisma.distribution.update({ where: { id }, data });

  // 名单变更 + 显式要求同步：把仍在用的 Key 一起改成新名单。
  let syncedKeys = 0;
  if (data.allowedModels !== undefined && body.syncModels === true) {
    const result = await prisma.apiKey.updateMany({
      where: { distributionId: id, revokedAt: null },
      data: { allowedModels: data.allowedModels },
    });
    syncedKeys = result.count;
  }

  await recordAudit({
    actorId: auth.sub,
    action: "distribution.update",
    detail: {
      distributionId: id,
      fields: Object.keys(data),
      allowedModels: data.allowedModels,
      syncedKeys,
    },
    requestId: auditRequestId(request),
  });

  const activeKeys = await prisma.apiKey.findMany({
    where: { distributionId: id, revokedAt: null },
    select: { empId: true },
  });

  return json({
    distribution: {
      id: updated.id,
      name: updated.name,
      description: updated.description,
      allowedModels: updated.allowedModels,
      disabled: updated.disabled,
      revokedAt: updated.revokedAt,
      createdAt: updated.createdAt,
      updatedAt: updated.updatedAt,
      assignedEmployees: new Set(activeKeys.map((key) => key.empId)).size,
      activeKeys: activeKeys.length,
    },
    syncedKeys,
  });
}
