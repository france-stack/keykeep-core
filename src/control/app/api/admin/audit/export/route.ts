import type { Prisma } from "@prisma/client";
import type { NextRequest } from "next/server";
import { NextResponse } from "next/server";

import { actionLabel, normalizeActionFilter } from "@/lib/audit";
import { requireRoles } from "@/lib/api-auth";
import { AUDIT_EXPORT_FIELDS, toCsv } from "@/lib/csv";
import { prisma } from "@/lib/db";
import { apiError } from "@/lib/http";

export const runtime = "nodejs";

const MAX_ROWS = 20_000;

function parseDate(value: string | null, endOfDay = false): Date | null {
  if (!value) return null;
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return null;
  if (endOfDay && value.length === 10) date.setDate(date.getDate() + 1);
  return date;
}

export async function GET(request: NextRequest) {
  const auth = await requireRoles(request, ["admin"]);
  if (auth instanceof NextResponse) return auth;

  const params = request.nextUrl.searchParams;
  const where: Prisma.AuditEventWhereInput = {};

  const action = normalizeActionFilter(params.get("action"));
  if (action) where.action = action;
  const actorId = params.get("actorId");
  if (actorId) where.actorId = actorId;

  const from = parseDate(params.get("from"));
  const to = parseDate(params.get("to"), true);
  if (from || to) where.at = { ...(from ? { gte: from } : {}), ...(to ? { lt: to } : {}) };

  // 按 seq 取"最近 N 行"再翻回转录顺序：导出的语义是链的一段，而不是按时间排序的报表。
  // seq desc + take 拿到的是最新的 N 行，reverse 后自上而下就是 prevHash→rowHash 的走向，
  // 归档方拿着文件本身即可核对；带筛选条件时行不连续，Prev Hash 会指出断点，
  // 这也是可核对的一部分（不假装筛过的片段是一条完整链）。
  const rows = await prisma.auditEvent.findMany({
    where,
    orderBy: { seq: "desc" },
    take: MAX_ROWS,
  });
  if (rows.length >= MAX_ROWS) {
    return apiError("export_too_large", 413, { maxRows: MAX_ROWS });
  }
  rows.reverse();

  const actorIds = [...new Set(rows.map((row) => row.actorId).filter((id): id is string => Boolean(id)))];
  const actors = actorIds.length
    ? await prisma.user.findMany({
        where: { id: { in: actorIds } },
        select: { id: true, account: true, displayName: true },
      })
    : [];
  const actorMap = new Map(actors.map((actor) => [actor.id, actor]));

  const csv = toCsv(
    [...AUDIT_EXPORT_FIELDS],
    rows.map((row) => {
      const actor = row.actorId ? actorMap.get(row.actorId) : undefined;
      const detail = row.detail && typeof row.detail === "object" ? JSON.stringify(row.detail) : "";
      return [
        row.seq.toString(),
        row.at.toISOString(),
        actor ? `${actor.displayName} (${actor.account})` : (row.actorId ?? "Anonymous"),
        `${actionLabel(row.action)} / ${row.action}`,
        detail,
        row.requestId,
        row.prevHash ?? "",
        row.rowHash ?? "",
      ];
    }),
  );

  const stamp = new Date().toISOString().slice(0, 10);
  return new NextResponse(csv, {
    status: 200,
    headers: {
      "Content-Type": "text/csv; charset=utf-8",
      "Content-Disposition": `attachment; filename="audit-${stamp}.csv"`,
      "Cache-Control": "no-store",
    },
  });
}
