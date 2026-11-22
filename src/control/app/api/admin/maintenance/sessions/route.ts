import type { NextRequest } from "next/server";
import { NextResponse } from "next/server";

import { requireRoles, requireWritable } from "@/lib/api-auth";
import { auditRequestId, recordAudit } from "@/lib/audit";
import { prisma } from "@/lib/db";
import { apiError, json } from "@/lib/http";
import {
  MAINT_AUDIT_ACTIONS,
  MAINT_CRED_TTL_MAX,
  checkTicket,
  issueMaintenanceCred,
  maintenanceEnabled,
} from "@/lib/maintenance";

export const runtime = "nodejs";

/**
 * 已批准工单 → 签发维护会话（短期凭据）。
 *
 * 这是「维护会话连接」的入口（RM-A02 的判定对象）。凭据明文只在响应里出现一次；
 * scope（主机/账号/命令范围）随请求绑定在会话行上。无效工单的三种情形
 * （无工单 / 过期 / 他人）在这里全部拒绝，并各写一条 connect_denied 审计。
 */
export async function POST(request: NextRequest) {
  const auth = await requireRoles(request, ["admin"]);
  if (auth instanceof NextResponse) return auth;
  if (!maintenanceEnabled()) return apiError("maintenance_disabled", 403);
  const readOnly = await requireWritable(request, auth.sub);
  if (readOnly) return readOnly;

  let body: {
    ticketId?: unknown;
    host?: unknown;
    account?: unknown;
    commandScope?: unknown;
    ttlSeconds?: unknown;
  };
  try {
    body = (await request.json()) as typeof body;
  } catch {
    return apiError("invalid_request", 400);
  }

  const ticketId = typeof body.ticketId === "string" ? body.ticketId : "";
  const host = typeof body.host === "string" ? body.host.trim().slice(0, 200) : "";
  const account = typeof body.account === "string" && body.account.trim() ? body.account.trim().slice(0, 120) : "maint";
  const commandScope = Array.isArray(body.commandScope)
    ? body.commandScope.filter(
        (value): value is string => typeof value === "string" && /^[\w.]{1,64}$/.test(value),
      )
    : [];
  const ttl = Number(body.ttlSeconds ?? MAINT_CRED_TTL_MAX);
  const effectiveTtl = Number.isFinite(ttl)
    ? Math.min(Math.max(Math.floor(ttl), 60), MAINT_CRED_TTL_MAX)
    : MAINT_CRED_TTL_MAX;

  if (!ticketId || !host) return apiError("invalid_request", 400);

  const now = new Date();
  const ticket = await prisma.maintenanceTicket.findUnique({ where: { id: ticketId } });
  const validity = checkTicket(ticket, now, auth.sub);
  if (!validity.ok) {
    // RM-A02：三种连接拒绝各产生一条审计；凭据明文不存在所以无泄露面。
    await recordAudit({
      actorId: auth.sub,
      action: MAINT_AUDIT_ACTIONS.sessionConnectDenied,
      detail: { ticketId, host, reason: validity.reason },
      requestId: auditRequestId(request),
    });
    return apiError(`ticket_${validity.reason}`, 403);
  }

  const cred = issueMaintenanceCred();
  if (!ticket) return apiError("ticket_not_found", 403);
  const session = await prisma.maintenanceSession.create({
    data: {
      ticketId,
      credHash: new Uint8Array(cred.credHash),
      credFp6: cred.fp6,
      host,
      account,
      commandScope,
      expiresAt: new Date(Math.min(now.getTime() + effectiveTtl * 1000, ticket.expiresAt.getTime())),
    },
  });

  await recordAudit({
    actorId: auth.sub,
    action: MAINT_AUDIT_ACTIONS.sessionConnect,
    detail: { ticketId, sessionId: session.id, host, account, credFp6: cred.fp6 },
    requestId: auditRequestId(request),
  });

  return json(
    {
      session: {
        id: session.id,
        ticketId,
        host,
        account,
        commandScope,
        expiresAt: session.expiresAt,
        credential: cred.plaintext,
      },
    },
    201,
  );
}
