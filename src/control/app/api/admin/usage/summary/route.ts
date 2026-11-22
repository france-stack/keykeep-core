import { Prisma } from "@prisma/client";
import type { NextRequest } from "next/server";
import { NextResponse } from "next/server";

import { requireRoles } from "@/lib/api-auth";
import { prisma } from "@/lib/db";
import { json } from "@/lib/http";
import { num, parseRange, rangeStart } from "@/lib/usage";

export const runtime = "nodejs";

const TREND_DAYS = 14;

export async function GET(request: NextRequest) {
  const auth = await requireRoles(request, []);
  if (auth instanceof NextResponse) return auth;

  const range = parseRange(request.nextUrl.searchParams.get("range"));
  const start = rangeStart(range);

  const trendStart = new Date();
  trendStart.setHours(0, 0, 0, 0);
  trendStart.setDate(trendStart.getDate() - (TREND_DAYS - 1));

  const scopeSql = auth.role === "admin" ? Prisma.empty : Prisma.sql`AND "empId" = ${auth.sub}::uuid`;
  // 拒绝事件记在 AuditEvent 上（actorId 是 TEXT，网关写的是员工 UUID 文本），
  // 不走 ::uuid 转换：值是同一串，但列类型不同，强转反而多一次失败面。
  const rejectSql = auth.role === "admin" ? Prisma.empty : Prisma.sql`AND "actorId" = ${auth.sub}`;

  // 聚合走原生 SQL：Prisma 适配器传 Date 时带时区偏移，会把「今天」算成 8 小时前。
  const [statRows, trendRows, topRows, rejectRows, budgets] = await Promise.all([
    prisma.$queryRaw<Array<{ calls: bigint; errors: bigint; input: bigint; output: bigint; cost: bigint }>>(Prisma.sql`
      SELECT COUNT(*)::bigint AS calls,
             COUNT(*) FILTER (WHERE failed)::bigint AS errors,
             COALESCE(SUM("inputTokens"), 0)::bigint AS input,
             COALESCE(SUM("outputTokens"), 0)::bigint AS output,
             COALESCE(SUM("costCents"), 0)::bigint AS cost
        FROM "UsageRecord"
       WHERE ts >= ${start.toISOString()}::timestamptz
       ${scopeSql}`),
    prisma.$queryRaw<Array<{ day: string; cost: bigint; calls: bigint }>>(Prisma.sql`
      SELECT to_char(date_trunc('day', ts), 'YYYY-MM-DD') AS day,
             COALESCE(SUM("costCents"), 0)::bigint AS cost,
             COUNT(*)::bigint AS calls
        FROM "UsageRecord"
       WHERE ts >= ${trendStart.toISOString()}::timestamptz
       ${scopeSql}
       GROUP BY 1
       ORDER BY 1`),
    prisma.$queryRaw<Array<{ model: string; calls: bigint; cost: bigint }>>(Prisma.sql`
      SELECT "modelServed" AS model,
             COALESCE(SUM("costCents"), 0)::bigint AS cost,
             COUNT(*)::bigint AS calls
        FROM "UsageRecord"
       WHERE ts >= ${start.toISOString()}::timestamptz
       ${scopeSql}
       GROUP BY 1
       ORDER BY cost DESC
       LIMIT 5`),
    // 被限流 / 被过载保护拒掉的请求在网关中间件就被拦下了，根本走不到记账那一步，
    // 所以 UsageRecord 里永远没有它们——「失败次数」只看 failed 会恒为 0。
    // 这两类拒绝只在审计流里留痕，这里按同一时间窗把它们补回来。
    // 按 action 分组而不是求和：员工要能分清「我撞了限流」和「网关过载」。
    prisma.$queryRaw<Array<{ action: string; count: bigint }>>(Prisma.sql`
      SELECT action, COUNT(*)::bigint AS count
        FROM "AuditEvent"
       WHERE at >= ${start.toISOString()}::timestamptz
         AND action IN ('key.rate_limited', 'key.overloaded')
       ${rejectSql}
       GROUP BY action`),
    auth.role === "admin"
      ? prisma.budget.findMany({
          where: { period: "monthly", scope: { in: ["emp", "global"] } },
          orderBy: [{ scope: "asc" }, { scopeId: "asc" }],
        })
      : Promise.resolve([]),
  ]);
  // 员工只看到「我自己的」与「全局」两条：预算拦截本来就是这两条取更严的一条，
  // 多给一条别人的行既没用，也泄露出同事的额度。
  const ownBudgets =
    auth.role === "admin"
      ? []
      : await prisma.budget.findMany({
          where: {
            period: "monthly",
            OR: [{ scope: "emp", scopeId: auth.sub }, { scope: "global" }],
          },
          orderBy: { scope: "asc" },
        });
  const stats = statRows[0] ?? { calls: 0n, errors: 0n, input: 0n, output: 0n, cost: 0n };
  // 两类「失败」分开给：rejected 是没进上游就被网关挡下的（不产生调用记录），
  // failed 是真正打出去但失败的。合成一个数会出现「失败数 > 调用数」的怪值。
  const rejectedBy: Record<string, number> = {};
  for (const row of rejectRows) rejectedBy[row.action] = num(row.count);
  const rateLimited = rejectedBy["key.rate_limited"] ?? 0;
  const overloaded = rejectedBy["key.overloaded"] ?? 0;

  return json({
    range,
    totals: {
      calls: num(stats.calls),
      errors: num(stats.errors),
      rejected: rateLimited + overloaded,
      rateLimited,
      overloaded,
      inputTokens: num(stats.input),
      outputTokens: num(stats.output),
      costCents: num(stats.cost),
    },
    trend: trendRows.map((row) => ({ day: row.day, costCents: num(row.cost), calls: num(row.calls) })),
    topModels: topRows.map((row) => ({
      model: row.model,
      calls: num(row.calls),
      costCents: num(row.cost),
    })),
    // 员工也要看自己的预算水位（概览页的「员工 vs 全局取更严」），
    // 但只给本人 + 全局两条，别人的预算行不出现。
    budgets:
      auth.role === "admin"
        ? budgets.map((budget) => ({
            id: budget.id,
            scope: budget.scope,
            scopeId: budget.scopeId,
            period: budget.period,
            limitCents: num(budget.limitCents),
            usedCents: num(budget.usedCents),
            reservedCents: num(budget.reservedCents),
          }))
        : ownBudgets.map((budget) => ({
            id: budget.id,
            scope: budget.scope,
            scopeId: budget.scopeId,
            period: budget.period,
            limitCents: num(budget.limitCents),
            usedCents: num(budget.usedCents),
            reservedCents: num(budget.reservedCents),
          })),
  });
}
