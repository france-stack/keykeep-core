import type { NextRequest } from "next/server";
import { NextResponse } from "next/server";

import { requireRoles } from "@/lib/api-auth";
import { prisma } from "@/lib/db";
import { describeModels } from "@/lib/distribution";
import { json } from "@/lib/http";

export const runtime = "nodejs";

/**
 * 员工自助：看自己名下还在用的虚拟 Key（只给指纹，明文永远不会再出现）。
 *
 * 老板也能调，但只返回自己的 Key——签发与管理走 /api/admin/keys。
 * 这里刻意不回 keyHash 与任何可以还原明文的字段。
 *
 * 带 distribution 名称：员工要能看出「这把是老板的『研发-DeepSeek』分发给的」，
 * 而不是一串无意义的指纹。分发被撤销时其下 Key 也已被同步撤销，故 revokedAt 已足够。
 */
export async function GET(request: NextRequest) {
  const auth = await requireRoles(request, []);
  if (auth instanceof NextResponse) return auth;

  const keys = await prisma.apiKey.findMany({
    where: { empId: auth.sub },
    orderBy: { createdAt: "desc" },
    take: 100,
    select: {
      id: true,
      prefix4: true,
      fp6: true,
      label: true,
      allowedModels: true,
      revokedAt: true,
      createdAt: true,
      distribution: { select: { id: true, name: true, revokedAt: true, disabled: true } },
    },
  });

  const limits = await prisma.limit.findMany({
    where: { scope: "key", scopeId: { in: keys.map((key) => key.id) } },
  });
  const limitsByKey = new Map<string, Record<string, number>>();
  for (const row of limits) {
    const current = limitsByKey.get(row.scopeId) ?? {};
    current[row.dim] = Number(row.limitValue);
    limitsByKey.set(row.scopeId, current);
  }

  return json({
    account: auth.account,
    gatewayPath: "/v1",
    keys: keys.map((key) => ({
      id: key.id,
      fingerprint: `${key.prefix4}…${key.fp6}`,
      label: key.label,
      distribution: key.distribution
        ? { id: key.distribution.id, name: key.distribution.name }
        : null,
      allowedModels: key.allowedModels,
      modelsText: describeModels(key.allowedModels),
      limits: limitsByKey.get(key.id) ?? {},
      revokedAt: key.revokedAt,
      createdAt: key.createdAt,
    })),
  });
}
