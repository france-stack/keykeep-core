import type { NextRequest } from "next/server";
import { NextResponse } from "next/server";

import { requireRoles } from "@/lib/api-auth";
import { prisma } from "@/lib/db";
import { apiError, json } from "@/lib/http";
import { maintenanceEnabled } from "@/lib/maintenance";

export const runtime = "nodejs";

// 维护审计查询（FR-64 / RM-A07）：连接 / 审批人 / 命令 / 结果 / 撤销五类可关联。
// 数据落在统一 AuditEvent 表（detail 带 ticketId/sessionId），这里做关联投影，
// 一次查询把五类记录串成一个会话的时间线。

const MAINT_ACTION_PREFIX = "maintenance.";

type TimelineRow = {
  at: Date;
  action: string;
  actorId: string | null;
  detail: unknown;
  requestId: string | null;
};

export async function GET(request: NextRequest) {
  const auth = await requireRoles(request, ["admin"]);
  if (auth instanceof NextResponse) return auth;
  if (!maintenanceEnabled()) return apiError("maintenance_disabled", 403);

  const sessionId = request.nextUrl.searchParams.get("sessionId") ?? "";
  const ticketId = request.nextUrl.searchParams.get("ticketId") ?? "";

  const events = await prisma.auditEvent.findMany({
    where: { action: { startsWith: MAINT_ACTION_PREFIX } },
    orderBy: { seq: "asc" },
    take: 500,
  });

  // 关联投影：sessionId / ticketId 过滤在内存做（500 条上限内足够；
  // detail 是 Json 列，跨库过滤需要数据库方言支持，P0 用进程内过滤换可移植）。
  const filtered = events.filter((event) => {
    const detail = event.detail as Record<string, unknown> | null;
    if (sessionId && detail?.sessionId !== sessionId) return false;
    if (ticketId && detail?.ticketId !== ticketId) return false;
    return true;
  });

  const timeline: Array<Record<string, unknown>> = filtered.map((event) => {
    const detail = (event.detail ?? {}) as Record<string, unknown>;
    return {
      at: event.at,
      action: event.action,
      actorId: event.actorId,
      ticketId: detail.ticketId ?? null,
      sessionId: detail.sessionId ?? null,
      approvalId: detail.approvalId ?? null,
      host: detail.host ?? null,
      account: detail.account ?? null,
      credFp6: detail.credFp6 ?? null,
      commandKey: detail.commandKey ?? null,
      reason: detail.reason ?? null,
      decision: detail.decision ?? null,
      requestId: event.requestId,
    };
  });

  return json({ total: timeline.length, timeline });
}

export type { TimelineRow };
