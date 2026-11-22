import type { NextRequest } from "next/server";
import { NextResponse } from "next/server";

import { requireRoles, requireWritable } from "@/lib/api-auth";
import { auditRequestId, recordAudit } from "@/lib/audit";
import { prisma } from "@/lib/db";
import { apiError, json } from "@/lib/http";
import { MAINT_AUDIT_ACTIONS, maintenanceEnabled } from "@/lib/maintenance";

export const runtime = "nodejs";

/** 待审批写命令列表（客户查看，RM-A05 的「审批记录」）。 */
export async function GET(request: NextRequest) {
  const auth = await requireRoles(request, ["admin"]);
  if (auth instanceof NextResponse) return auth;
  if (!maintenanceEnabled()) return apiError("maintenance_disabled", 403);

  const stateParam = request.nextUrl.searchParams.get("state") ?? "pending";
  const states = stateParam === "all"
    ? ["pending", "approved", "rejected", "executed", "denied_by_policy"]
    : [stateParam];

  const approvals = await prisma.maintenanceApproval.findMany({
    where: { state: { in: states } },
    orderBy: { requestedAt: "desc" },
    take: 100,
    include: {
      session: { select: { id: true, host: true, account: true, credFp6: true, ticketId: true } },
    },
  });

  return json({
    approvals: approvals.map((approval) => ({
      id: approval.id,
      sessionId: approval.sessionId,
      session: approval.session,
      command: approval.command,
      commandKey: approval.commandKey,
      kind: approval.kind,
      state: approval.state,
      result: approval.result,
      exitCode: approval.exitCode,
      requestedAt: approval.requestedAt,
      decidedAt: approval.decidedAt,
    })),
  });
}

/** 审批决定（FR-67）：approve / reject。P0 骨架里批准只落状态，不触发执行器。 */
export async function POST(request: NextRequest) {
  const auth = await requireRoles(request, ["admin"]);
  if (auth instanceof NextResponse) return auth;
  if (!maintenanceEnabled()) return apiError("maintenance_disabled", 403);
  const readOnly = await requireWritable(request, auth.sub);
  if (readOnly) return readOnly;

  let body: { approvalId?: unknown; decision?: unknown };
  try {
    body = (await request.json()) as typeof body;
  } catch {
    return apiError("invalid_request", 400);
  }
  const approvalId = typeof body.approvalId === "string" ? body.approvalId : "";
  const decision = body.decision === "approve" || body.decision === "reject" ? body.decision : "";
  if (!approvalId || !decision) return apiError("invalid_request", 400);

  const approval = await prisma.maintenanceApproval.findUnique({ where: { id: approvalId } });
  if (!approval) return apiError("not_found", 404);
  if (approval.state !== "pending") return apiError("already_decided", 409);

  const now = new Date();
  await prisma.maintenanceApproval.update({
    where: { id: approvalId },
    data: {
      state: decision === "approve" ? "approved" : "rejected",
      decidedAt: now,
      decidedById: auth.sub,
    },
  });

  await recordAudit({
    actorId: auth.sub,
    action: MAINT_AUDIT_ACTIONS.approvalDecide,
    detail: {
      approvalId,
      sessionId: approval.sessionId,
      commandKey: approval.commandKey,
      decision,
    },
    requestId: auditRequestId(request),
  });

  return json({ ok: true, state: decision === "approve" ? "approved" : "rejected" });
}
