import { Prisma } from "@prisma/client";
import type { NextRequest } from "next/server";
import { NextResponse } from "next/server";

import { requireRoles } from "@/lib/api-auth";
import { prisma } from "@/lib/db";
import { json } from "@/lib/http";
import { buildTeamTree, type UsageAggregate } from "@/lib/team-tree";
import { num } from "@/lib/usage";

export const runtime = "nodejs";

/**
 * 主页面拓扑树的数据：老板 + 员工 + 今日用量 + 预算 + Key 数。
 * 一次请求取全，避免页面上按人头发 N 个请求。
 *
 * 用量按「今天 00:00」起算（服务器本地时区），与概览页 Today 口径一致。
 * 聚合走原生 SQL：Prisma 适配器传 Date 会带时区偏移，把「今天」算成 8 小时前。
 */
export async function GET(request: NextRequest) {
  const auth = await requireRoles(request, ["admin"]);
  if (auth instanceof NextResponse) return auth;

  const todayStart = new Date();
  todayStart.setHours(0, 0, 0, 0);

  const [users, usageRows, budgets, keyGroups] = await Promise.all([
    prisma.user.findMany({
      include: { role: true },
      orderBy: { createdAt: "asc" },
      take: 500,
    }),
    prisma.$queryRaw<
      Array<{
        emp_id: string;
        calls: bigint;
        errors: bigint;
        cost: bigint;
        input: bigint;
        output: bigint;
        last_at: Date | null;
      }>
    >(Prisma.sql`
      SELECT "empId"::text AS emp_id,
             COUNT(*)::bigint AS calls,
             COUNT(*) FILTER (WHERE failed)::bigint AS errors,
             COALESCE(SUM("costCents"), 0)::bigint AS cost,
             COALESCE(SUM("inputTokens"), 0)::bigint AS input,
             COALESCE(SUM("outputTokens"), 0)::bigint AS output,
             MAX(ts) AS last_at
        FROM "UsageRecord"
       WHERE ts >= ${todayStart.toISOString()}::timestamptz
       GROUP BY 1`),
    prisma.budget.findMany({
      where: { period: "monthly", scope: { in: ["emp", "global"] } },
    }),
    prisma.apiKey.groupBy({
      by: ["empId"],
      where: { revokedAt: null },
      _count: { _all: true },
    }),
  ]);

  const usage: UsageAggregate[] = usageRows.map((row) => ({
    empId: row.emp_id,
    calls: Number(row.calls),
    errors: Number(row.errors),
    costCents: Number(row.cost),
    inputTokens: Number(row.input),
    outputTokens: Number(row.output),
    lastAt: row.last_at ? row.last_at.toISOString() : null,
  }));

  const keyCounts: Record<string, number> = {};
  for (const group of keyGroups) keyCounts[group.empId] = group._count._all;

  const tree = buildTeamTree({
    users: users.map((user) => ({
      id: user.id,
      account: user.account,
      displayName: user.displayName,
      role: user.role.name,
      disabled: user.disabled,
      createdAt: user.createdAt,
    })),
    usage,
    budgets: budgets.map((budget) => ({
      scope: budget.scope,
      scopeId: budget.scopeId,
      limitCents: num(budget.limitCents),
      usedCents: num(budget.usedCents),
    })),
    keyCounts,
  });

  return json({ ...tree, generatedAt: new Date().toISOString() });
}
