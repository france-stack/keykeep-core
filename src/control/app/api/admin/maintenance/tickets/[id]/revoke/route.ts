import type { NextRequest } from "next/server";
import { NextResponse } from "next/server";

import { requireRoles, requireWritable } from "@/lib/api-auth";
import { auditRequestId, recordAudit } from "@/lib/audit";
import { prisma } from "@/lib/db";
import { apiError, json } from "@/lib/http";
import { MAINT_AUDIT_ACTIONS, maintenanceEnabled } from "@/lib/maintenance";

export const runtime = "nodejs";

/**
 * 一键撤销工单（FR-65 / RM-A04）：撤销即立刻把工单与其下所有活跃会话打上 revokedAt。
 * 会话有效性判定在每次命令调用时查库（无缓存），所以撤销生效时延 = 下一次判定，≤1s。
 */
export async function POST(
  request: NextRequest,
  context: { params: Promise<{ id: string }> },
) {
  const auth = await requireRoles(request, ["admin"]);
  if (auth instanceof NextResponse) return auth;
  if (!maintenanceEnabled()) return apiError("maintenance_disabled", 403);
  const readOnly = await requireWritable(request, auth.sub);
  if (readOnly) return readOnly;

  const { id } = await context.params;
  const ticket = await prisma.maintenanceTicket.findUnique({ where: { id } });
  if (!ticket) return apiError("not_found", 404);

  const now = new Date();
  await prisma.$transaction(async (tx) => {
    await tx.maintenanceTicket.update({ where: { id }, data: { revokedAt: now } });
    // 工单撤销 = 一键撤销其下全部未撤销会话（RM-A04「客户可一键撤销」的工单级落点）。
    await tx.maintenanceSession.updateMany({
      where: { ticketId: id, revokedAt: null },
      data: { revokedAt: now, endReason: "ticket_revoked" },
    });
  });

  await recordAudit({
    actorId: auth.sub,
    action: MAINT_AUDIT_ACTIONS.ticketRevoke,
    detail: { ticketId: id, vendorName: ticket.vendorName },
    requestId: auditRequestId(request),
  });

  return json({ ok: true });
}
