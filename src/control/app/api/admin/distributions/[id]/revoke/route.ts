import type { NextRequest } from "next/server";
import { NextResponse } from "next/server";

import { requireRoles, requireWritable } from "@/lib/api-auth";
import { auditRequestId, recordAudit } from "@/lib/audit";
import { prisma } from "@/lib/db";
import { apiError, json } from "@/lib/http";

export const runtime = "nodejs";

/**
 * 撤销分发 API。
 *
 * 撤分发必须同时撤掉它下面的 Key，否则「撤销」只是页面上变灰、员工手里的
 * Key 还能继续用 —— 那是最坏的失败模式（老板以为收回了权限）。
 * 因此两步在同一个事务里做：先给仍活跃的 Key 打 revokedAt，再撤销分发本身。
 */
export async function POST(request: NextRequest, context: { params: Promise<{ id: string }> }) {
  const auth = await requireRoles(request, ["admin"]);
  if (auth instanceof NextResponse) return auth;
  const readOnly = await requireWritable(request, auth.sub);
  if (readOnly) return readOnly;

  const { id } = await context.params;
  const distribution = await prisma.distribution.findUnique({ where: { id } });
  if (!distribution) return apiError("not_found", 404);
  // 幂等：已撤销再点一次不报错，返回同样的结果（前端按钮可能被连点）。
  if (distribution.revokedAt) {
    return json({ revokedAt: distribution.revokedAt, revokedKeys: 0 });
  }

  const now = new Date();
  const [keys] = await prisma.$transaction([
    prisma.apiKey.updateMany({
      where: { distributionId: id, revokedAt: null },
      data: { revokedAt: now },
    }),
    prisma.distribution.update({
      where: { id },
      data: { revokedAt: now },
    }),
  ]);

  await recordAudit({
    actorId: auth.sub,
    action: "distribution.revoke",
    detail: { distributionId: id, name: distribution.name, revokedKeys: keys.count },
    requestId: auditRequestId(request),
  });

  return json({ revokedAt: now, revokedKeys: keys.count });
}
