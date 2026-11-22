import { Prisma } from "@prisma/client";
import type { NextRequest } from "next/server";
import { NextResponse } from "next/server";

import { requireRoles, requireWritable } from "@/lib/api-auth";
import { auditRequestId, recordAudit } from "@/lib/audit";
import { prisma } from "@/lib/db";
import { apiError, json } from "@/lib/http";
import { num } from "@/lib/usage";
import { isUuid } from "@/lib/uuid";

export const runtime = "nodejs";

const SCOPES = new Set(["emp", "global"]);
const PERIODS = new Set(["monthly"]);
const MAX_LIMIT_CENTS = 1_000_000_000_00;

export async function GET(request: NextRequest) {
  const auth = await requireRoles(request, ["admin"]);
  if (auth instanceof NextResponse) return auth;

  const [budgets, users] = await Promise.all([
    prisma.budget.findMany({ where: { scope: { in: ["emp", "global"] } }, orderBy: [{ scope: "asc" }, { scopeId: "asc" }] }),
    prisma.user.findMany({ select: { id: true, displayName: true, account: true } }),
  ]);
  const userName = new Map(users.map((user) => [user.id, `${user.displayName} (${user.account})`]));

  return json({
    budgets: budgets.map((budget) => ({
      id: budget.id,
      scope: budget.scope,
      scopeId: budget.scopeId,
      scopeName:
        budget.scope === "global" ? "Global" : (userName.get(budget.scopeId) ?? budget.scopeId),
      period: budget.period,
      limitCents: num(budget.limitCents),
      usedCents: num(budget.usedCents),
      reservedCents: num(budget.reservedCents),
      updatedAt: budget.updatedAt,
    })),
    users: users.map((user) => ({ id: user.id, name: userName.get(user.id) })),
  });
}

export async function POST(request: NextRequest) {
  const auth = await requireRoles(request, ["admin"]);
  if (auth instanceof NextResponse) return auth;
  const readOnly = await requireWritable(request, auth.sub);
  if (readOnly) return readOnly;

  let body: { scope?: unknown; scopeId?: unknown; period?: unknown; limitCents?: unknown };
  try {
    body = (await request.json()) as typeof body;
  } catch {
    return apiError("invalid_request", 400);
  }
  const scope = typeof body.scope === "string" ? body.scope : "";
  const period = typeof body.period === "string" ? body.period : "monthly";
  const limitCents = Math.floor(Number(body.limitCents));
  if (!SCOPES.has(scope) || !PERIODS.has(period)) return apiError("invalid_request", 400);
  if (!Number.isFinite(limitCents) || limitCents < 0 || limitCents > MAX_LIMIT_CENTS) {
    return apiError("invalid_request", 400);
  }

  let scopeId = scope === "global" ? "global" : typeof body.scopeId === "string" ? body.scopeId : "";
  if (!scopeId) return apiError("invalid_request", 400);
  if (scope === "emp" && !(await prisma.user.findUnique({ where: { id: scopeId } }))) {
    return apiError("invalid_request", 400);
  }

  const budget = await prisma.budget.upsert({
    where: { scope_scopeId_period: { scope, scopeId, period } },
    create: { scope, scopeId, period, limitCents },
    update: { limitCents },
  });

  await recordAudit({
    actorId: auth.sub,
    action: "budget.upsert",
    detail: { budgetId: budget.id, scope, scopeId, period, limitCents },
    requestId: auditRequestId(request),
  });

  return json(
    {
      budget: {
        id: budget.id,
        scope: budget.scope,
        scopeId: budget.scopeId,
        period: budget.period,
        limitCents: num(budget.limitCents),
        usedCents: num(budget.usedCents),
        reservedCents: num(budget.reservedCents),
      },
    },
    201,
  );
}

export async function DELETE(request: NextRequest) {
  const auth = await requireRoles(request, ["admin"]);
  if (auth instanceof NextResponse) return auth;
  const readOnly = await requireWritable(request, auth.sub);
  if (readOnly) return readOnly;

  const id = request.nextUrl.searchParams.get("id");
  if (!id) return apiError("invalid_request", 400);
  // 非法 UUID 先挡在 SQL 之外（C10：非法 cast 会让 PG 抛错，落到 500）。
  if (!isUuid(id)) return apiError("invalid_request", 400);
  try {
    await prisma.budget.delete({ where: { id } });
  } catch (error) {
    // 有流水时外键是 RESTRICT（BudgetLedger.budget 未指定 onDelete），删除必然
    // 违反约束（P2003）。记录仍然在，绝不能像过去那样一律谎称 not_found(404)。
    if (error instanceof Prisma.PrismaClientKnownRequestError) {
      if (error.code === "P2003") {
        let ledgerCount: number | undefined;
        try {
          ledgerCount = await prisma.budgetLedger.count({ where: { budgetId: id } });
        } catch {
          // 计数只是提示信息，失败不影响 409 的判定。
        }
        return apiError("budget_in_use", 409, ledgerCount === undefined ? {} : { ledgerCount });
      }
      if (error.code === "P2025") return apiError("not_found", 404);
    }
    // 其余异常（连接失败、约束之外的库错误）如实报 500，并留一行日志。
    const summary = error instanceof Error ? `${error.name}: ${error.message}` : String(error);
    console.error(`[budgets] delete failed id=${id} ${summary}`);
    return apiError("delete_failed", 500);
  }
  await recordAudit({
    actorId: auth.sub,
    action: "budget.delete",
    detail: { budgetId: id },
    requestId: auditRequestId(request),
  });
  return json({ deleted: id });
}
