import type { NextRequest } from "next/server";
import { NextResponse } from "next/server";

import { assertSameOrigin, requireRoles } from "@/lib/api-auth";
import { auditRequestId, recordAudit } from "@/lib/audit";
import { prisma } from "@/lib/db";
import { apiError, json } from "@/lib/http";
import { hashPassword, validatePassword, verifyPassword } from "@/lib/password";

export const runtime = "nodejs";

/**
 * 自助改密（FR-01 向导第 1 步 / 设置页通用）。
 *
 * 与 admin PATCH /api/admin/users/[id] 的分工：那是「管理员重置员工」，
 * 这里是「登录者本人改自己的密码」，必须验证旧口令——两者都要有，
 * 缺自助端点时向导只能走管理员重置语义，审计也说不清是谁发的动作。
 *
 * 改密后 passwordChangedAt > session.iat，本会话按 sessionRevoked 立即失效：
 * 这是安全模型的既定行为（改密即全端下线），向导 UI 把重新登录当成第 1 步
 * 的一部分来呈现。
 */
export async function PATCH(request: NextRequest) {
  const csrf = assertSameOrigin(request);
  if (csrf) return csrf;
  const auth = await requireRoles(request, []);
  if (auth instanceof NextResponse) return auth;

  let body: { currentPassword?: string; newPassword?: string };
  try {
    body = (await request.json()) as typeof body;
  } catch {
    return apiError("invalid_request", 400);
  }
  const currentPassword = body.currentPassword ?? "";
  const newPassword = body.newPassword ?? "";
  if (!currentPassword || !newPassword) return apiError("invalid_request", 400);
  const passwordError = validatePassword(newPassword);
  if (passwordError) return apiError("invalid_request", 400, { reason: passwordError });

  const user = await prisma.user.findUnique({
    where: { id: auth.sub },
    select: { id: true, passwordHash: true },
  });
  if (!user) return apiError("invalid_session", 401);
  if (!(await verifyPassword(user.passwordHash, currentPassword))) {
    await recordAudit({
      actorId: auth.sub,
      action: "user.password_self",
      detail: { ok: false },
      requestId: auditRequestId(request),
    });
    return apiError("invalid_credentials", 401);
  }

  await prisma.user.update({
    where: { id: user.id },
    data: {
      passwordHash: await hashPassword(newPassword),
      passwordChangedAt: new Date(),
      failedAttempts: 0,
      lockedUntil: null,
    },
  });

  await recordAudit({
    actorId: auth.sub,
    action: "user.password_self",
    detail: { ok: true },
    requestId: auditRequestId(request),
  });
  return json({ ok: true });
}
