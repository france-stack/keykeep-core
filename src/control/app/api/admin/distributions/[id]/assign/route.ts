import type { NextRequest } from "next/server";
import { NextResponse } from "next/server";

import { requireRoles, requireWritable } from "@/lib/api-auth";
import { auditRequestId, recordAudit } from "@/lib/audit";
import { prisma } from "@/lib/db";
import {
  canAssign,
  clampAssignCount,
  MAX_LABEL,
  resolveAssignmentModels,
} from "@/lib/distribution";
import { apiError, json } from "@/lib/http";
import { issueKey } from "@/lib/keys";

export const runtime = "nodejs";

/**
 * 把这个分发 API 分配给某个员工：在员工名下签发 count 把虚拟 Key。
 *
 * 需求②「给各员工账号分发 API，并选定该员工可用模型」就落在这里：
 *   - allowedModels 不传 → 用分发上的默认名单（老板在分发上配过一次就够）；
 *   - allowedModels 传了（含空数组）→ 用本次指定的名单，只影响这个员工的这几把 Key。
 * 明文 Key 只在本次响应里出现一次，库里只留哈希与指纹（与 /api/admin/keys 一致）。
 */
export async function POST(request: NextRequest, context: { params: Promise<{ id: string }> }) {
  const auth = await requireRoles(request, ["admin"]);
  if (auth instanceof NextResponse) return auth;
  const readOnly = await requireWritable(request, auth.sub);
  if (readOnly) return readOnly;

  const { id } = await context.params;
  const distribution = await prisma.distribution.findUnique({ where: { id } });
  if (!distribution) return apiError("not_found", 404);
  if (!canAssign(distribution)) {
    return apiError("distribution_not_assignable", 403, {
      status: distribution.revokedAt ? "revoked" : "disabled",
    });
  }

  let body: { empId?: unknown; count?: unknown; allowedModels?: unknown; label?: unknown };
  try {
    body = (await request.json()) as typeof body;
  } catch {
    return apiError("invalid_request", 400);
  }

  const empId = typeof body.empId === "string" ? body.empId : "";
  if (!empId) return apiError("invalid_request", 400, { field: "empId" });

  const label = typeof body.label === "string" ? body.label.trim().slice(0, MAX_LABEL) : "";
  const count = clampAssignCount(body.count);
  const allowedModels = resolveAssignmentModels(body as Record<string, unknown>, distribution.allowedModels);

  const emp = await prisma.user.findUnique({ where: { id: empId } });
  // 与 /api/admin/keys 保持一致：目标不存在或已停用都拒绝，避免给停用账号发新凭据。
  if (!emp || emp.disabled) return apiError("invalid_request", 400, { field: "empId" });

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
        distributionId: distribution.id,
        label,
      },
    });
    issued.push({ id: row.id, plaintext: key.plaintext, prefix4: key.prefix4, fp6: key.fp6 });
  }

  await recordAudit({
    actorId: auth.sub,
    action: "distribution.assign",
    detail: {
      distributionId: distribution.id,
      empId: emp.id,
      count: issued.length,
      allowedModels,
      keyIds: issued.map((key) => key.id),
    },
    requestId: auditRequestId(request),
  });

  return json(
    {
      distribution: { id: distribution.id, name: distribution.name },
      emp: { id: emp.id, account: emp.account, displayName: emp.displayName },
      allowedModels,
      keys: issued,
    },
    201,
  );
}
