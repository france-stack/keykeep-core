import type { Prisma } from "@prisma/client";
import type { NextRequest } from "next/server";
import { NextResponse } from "next/server";

import { actionLabel, normalizeActionFilter } from "@/lib/audit";
import { requireRoles } from "@/lib/api-auth";
import { prisma } from "@/lib/db";
import { json } from "@/lib/http";

export const runtime = "nodejs";

const DEFAULT_PAGE_SIZE = 30;
const MAX_PAGE_SIZE = 100;

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
  if (from || to) {
    where.at = { ...(from ? { gte: from } : {}), ...(to ? { lt: to } : {}) };
  }

  const page = Math.max(1, Math.floor(Number(params.get("page") ?? 1)) || 1);
  const pageSize = Math.min(
    MAX_PAGE_SIZE,
    Math.max(1, Math.floor(Number(params.get("pageSize") ?? DEFAULT_PAGE_SIZE)) || DEFAULT_PAGE_SIZE),
  );

  const [total, rows] = await Promise.all([
    prisma.auditEvent.count({ where }),
    // 按 seq（唯一）而不是 at（可重复）排序翻页：同一毫秒写入多行时，
    // orderBy at + skip/take 会让同一条记录在相邻两页各出现一次或干脆消失。
    prisma.auditEvent.findMany({
      where,
      orderBy: { seq: "desc" },
      skip: (page - 1) * pageSize,
      take: pageSize,
    }),
  ]);

  const actorIds = [...new Set(rows.map((row) => row.actorId).filter((id): id is string => Boolean(id)))];
  const actors = actorIds.length
    ? await prisma.user.findMany({
        where: { id: { in: actorIds } },
        select: { id: true, account: true, displayName: true },
      })
    : [];
  const actorMap = new Map(actors.map((actor) => [actor.id, actor]));

  return json({
    total,
    page,
    pageSize,
    events: rows.map((row) => ({
      id: row.id,
      seq: row.seq.toString(),
      at: row.at,
      actor: row.actorId ? (actorMap.get(row.actorId) ?? { id: row.actorId, account: "", displayName: "" }) : null,
      action: row.action,
      actionLabel: actionLabel(row.action),
      detail: row.detail,
      requestId: row.requestId,
      // 链三元组随行走：界面/脚本据此能看出这一行有没有进链（unchanged 时 rowHash 为 null）。
      prevHash: row.prevHash,
      rowHash: row.rowHash,
      hashAlgo: row.hashAlgo,
    })),
  });
}
