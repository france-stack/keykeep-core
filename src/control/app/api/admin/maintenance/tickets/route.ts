import type { NextRequest } from "next/server";
import { NextResponse } from "next/server";

import { requireRoles, requireWritable } from "@/lib/api-auth";
import { auditRequestId, recordAudit } from "@/lib/audit";
import { prisma } from "@/lib/db";
import { apiError, json } from "@/lib/http";
import { MAINT_AUDIT_ACTIONS, maintenanceEnabled } from "@/lib/maintenance";

export const runtime = "nodejs";

/** 工单窗口默认 4 小时；请求可指定更短，超出按上限截断（设计文档「任务级短期凭据」）。 */
const TICKET_WINDOW_MAX_SECONDS = 4 * 60 * 60;
const TICKET_WINDOW_DEFAULT_SECONDS = 4 * 60 * 60;

export async function GET(request: NextRequest) {
  const auth = await requireRoles(request, ["admin"]);
  if (auth instanceof NextResponse) return auth;
  if (!maintenanceEnabled()) return apiError("maintenance_disabled", 403);

  const tickets = await prisma.maintenanceTicket.findMany({
    orderBy: { createdAt: "desc" },
    take: 100,
    include: {
      createdBy: { select: { id: true, account: true, displayName: true } },
      sessions: {
        select: { id: true, credFp6: true, host: true, account: true, revokedAt: true, endedAt: true, expiresAt: true },
      },
    },
  });
  return json({
    tickets: tickets.map((ticket) => ({
      id: ticket.id,
      vendorName: ticket.vendorName,
      reason: ticket.reason,
      approvedAt: ticket.approvedAt,
      expiresAt: ticket.expiresAt,
      revokedAt: ticket.revokedAt,
      createdBy: ticket.createdBy,
      sessions: ticket.sessions.map((session) => ({
        id: session.id,
        credFp6: session.credFp6,
        host: session.host,
        account: session.account,
        revokedAt: session.revokedAt,
        endedAt: session.endedAt,
        expiresAt: session.expiresAt,
      })),
    })),
  });
}

export async function POST(request: NextRequest) {
  const auth = await requireRoles(request, ["admin"]);
  if (auth instanceof NextResponse) return auth;
  if (!maintenanceEnabled()) return apiError("maintenance_disabled", 403);
  const readOnly = await requireWritable(request, auth.sub);
  if (readOnly) return readOnly;

  let body: { vendorName?: unknown; reason?: unknown; windowSeconds?: unknown };
  try {
    body = (await request.json()) as typeof body;
  } catch {
    return apiError("invalid_request", 400);
  }

  const vendorName = typeof body.vendorName === "string" ? body.vendorName.trim().slice(0, 120) : "";
  const reason = typeof body.reason === "string" ? body.reason.trim().slice(0, 500) : "";
  if (!vendorName) return apiError("invalid_request", 400);

  const windowSeconds = Number(body.windowSeconds ?? TICKET_WINDOW_DEFAULT_SECONDS);
  const effectiveWindow = Number.isFinite(windowSeconds)
    ? Math.min(Math.max(Math.floor(windowSeconds), 60), TICKET_WINDOW_MAX_SECONDS)
    : TICKET_WINDOW_DEFAULT_SECONDS;

  const now = new Date();
  const ticket = await prisma.maintenanceTicket.create({
    data: {
      vendorName,
      reason,
      approvedAt: now,
      expiresAt: new Date(now.getTime() + effectiveWindow * 1000),
      createdById: auth.sub,
    },
  });

  await recordAudit({
    actorId: auth.sub,
    action: MAINT_AUDIT_ACTIONS.ticketApprove,
    detail: {
      ticketId: ticket.id,
      vendorName,
      windowSeconds: effectiveWindow,
      expiresAt: ticket.expiresAt.toISOString(),
    },
    requestId: auditRequestId(request),
  });

  return json({ ticket: { id: ticket.id, vendorName: ticket.vendorName, expiresAt: ticket.expiresAt } }, 201);
}
