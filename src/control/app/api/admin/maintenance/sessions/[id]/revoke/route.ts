import type { NextRequest } from "next/server";
import { NextResponse } from "next/server";

import { requireRoles, requireWritable } from "@/lib/api-auth";
import { auditRequestId, recordAudit } from "@/lib/audit";
import { prisma } from "@/lib/db";
import { apiError, json } from "@/lib/http";
import { MAINT_AUDIT_ACTIONS, maintenanceEnabled } from "@/lib/maintenance";

export const runtime = "nodejs";

/** 撤销单个维护会话（FR-65）：判定在每次命令调用时查库，撤销 ≤1s 生效。 */
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
  const session = await prisma.maintenanceSession.findUnique({ where: { id } });
  if (!session) return apiError("not_found", 404);

  const now = new Date();
  await prisma.maintenanceSession.update({
    where: { id },
    data: { revokedAt: now, endReason: "customer_revoked" },
  });

  await recordAudit({
    actorId: auth.sub,
    action: MAINT_AUDIT_ACTIONS.sessionRevoke,
    detail: { ticketId: session.ticketId, sessionId: id },
    requestId: auditRequestId(request),
  });

  return json({ ok: true });
}
