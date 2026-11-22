import type { NextRequest } from "next/server";
import { NextResponse } from "next/server";

import { requireRoles, requireWritable } from "@/lib/api-auth";
import { auditRequestId, recordAudit } from "@/lib/audit";
import { prisma } from "@/lib/db";
import { apiError, json } from "@/lib/http";
import { MAINT_AUDIT_ACTIONS, maintenanceEnabled } from "@/lib/maintenance";

export const runtime = "nodejs";

/**
 * 超时清理（FR-65 / T-43）：把过期未结束的会话标记为 ended，并汇总当前无残留状态。
 * 维护会话没有进程与端口——「无残留」在控制面内是数据判定（没有活跃会话），
 * 不涉及隧道进程（设计文档 M4「不做」自动隧道；连接模式 2 的反向隧道由客户自建并限时）。
 */
export async function POST(request: NextRequest) {
  const auth = await requireRoles(request, ["admin"]);
  if (auth instanceof NextResponse) return auth;
  if (!maintenanceEnabled()) return apiError("maintenance_disabled", 403);
  const readOnly = await requireWritable(request, auth.sub);
  if (readOnly) return readOnly;

  const now = new Date();
  const updated = await prisma.maintenanceSession.updateMany({
    where: {
      endedAt: null,
      revokedAt: null,
      expiresAt: { lte: now },
    },
    data: { endedAt: now, endReason: "expired" },
  });

  await recordAudit({
    actorId: auth.sub,
    action: MAINT_AUDIT_ACTIONS.cleanup,
    detail: { endedSessions: updated.count },
    requestId: auditRequestId(request),
  });

  const active = await prisma.maintenanceSession.count({
    where: { endedAt: null, revokedAt: null, expiresAt: { gt: now } },
  });

  return json({ endedSessions: updated.count, activeSessions: active });
}
