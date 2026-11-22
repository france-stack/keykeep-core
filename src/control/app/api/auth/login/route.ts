import type { NextRequest } from "next/server";

import { auditRequestId, recordAudit } from "@/lib/audit";
import { assertSameOrigin } from "@/lib/api-auth";
import { prisma } from "@/lib/db";
import { apiError, json } from "@/lib/http";
import { checkLoginAllowed } from "@/lib/login-limiter";
import { verifyDummyPassword, verifyPassword } from "@/lib/password";
import {
  SESSION_COOKIE,
  SESSION_TTL_SECONDS,
  cookieSecure,
  newSession,
  signSession,
} from "@/lib/session";

export const runtime = "nodejs";

const MAX_FAILURES = 5;
const LOCK_MINUTES = 15;
/** 登录口令长度上限：Argon2 吃 19MiB 内存，超长口令会放大单请求开销。 */
const MAX_LOGIN_PASSWORD = 1024;

export async function POST(request: NextRequest) {
  const csrf = assertSameOrigin(request);
  if (csrf) return csrf;
  const rateLimited = checkLoginAllowed(request.headers);
  if (rateLimited !== null) {
    return apiError("too_many_attempts", 429, { retry_after: rateLimited });
  }
  let body: { account?: string; password?: string };
  try {
    body = (await request.json()) as { account?: string; password?: string };
  } catch {
    return apiError("invalid_request", 400);
  }

  const account = (body.account ?? "").trim();
  const password = body.password ?? "";
  if (!account || !password) return apiError("invalid_request", 400);
  if (password.length > MAX_LOGIN_PASSWORD) return apiError("invalid_credentials", 401);

  const user = await prisma.user.findUnique({
    where: { account },
    include: { role: true },
  });
  if (!user) {
    // 账号不存在也跑一次等耗时的哑校验，抹平计时侧信道（远程账号枚举）。
    await verifyDummyPassword(password);
    await recordAudit({
      action: "auth.login_failed",
      detail: { account, reason: "invalid_credentials" },
      requestId: auditRequestId(request),
    });
    return apiError("invalid_credentials", 401);
  }
  if (user.disabled) {
    await recordAudit({
      actorId: user.id,
      action: "auth.login_failed",
      detail: { account, reason: "invalid_credentials" },
      requestId: auditRequestId(request),
    });
    return apiError("invalid_credentials", 401);
  }

  const now = new Date();
  if (user.lockedUntil && user.lockedUntil > now) {
    const retryAfter = Math.max(1, Math.ceil((user.lockedUntil.getTime() - now.getTime()) / 1000));
    await recordAudit({
      actorId: user.id,
      action: "auth.login_failed",
      detail: { account, reason: "account_locked" },
      requestId: auditRequestId(request),
    });
    return apiError("account_locked", 423, { retry_after: retryAfter });
  }

  const ok = await verifyPassword(user.passwordHash, password);
  if (!ok) {
    // 原子递增 + 条件更新：并发登录同一账号时读-改-写会把计数互相覆盖，
    // 实际尝试次数可以超过 5 次才锁；这里让第 5 次失败落在数据库端判定。
    let updated: { failedAttempts: number } | null = null;
    try {
      updated = await prisma.user.update({
        where: { id: user.id, failedAttempts: { lt: MAX_FAILURES } },
        data: { failedAttempts: { increment: 1 } },
        select: { failedAttempts: true },
      });
    } catch {
      // 计数已到上限（P2025）：走锁定分支。
    }
    const lock = updated !== null && updated.failedAttempts >= MAX_FAILURES;
    if (lock) {
      await prisma.user.update({
        where: { id: user.id },
        data: { lockedUntil: new Date(now.getTime() + LOCK_MINUTES * 60_000) },
      });
    }
    // 契约（FA-02）：前 5 次错误口令均返回 401 且计数递增，第 5 次失败后置锁；
    // 下一次（第 6 次）进入锁定分支返回 423，即使口令正确也不放行。
    await recordAudit({
      actorId: user.id,
      action: "auth.login_failed",
      detail: { account, reason: lock ? "account_locked" : "invalid_credentials" },
      requestId: auditRequestId(request),
    });
    return apiError("invalid_credentials", 401, lock ? { locked: true, retry_after: LOCK_MINUTES * 60 } : {});
  }

  await prisma.user.update({
    where: { id: user.id },
    data: { failedAttempts: 0, lockedUntil: null },
  });

  await recordAudit({
    actorId: user.id,
    action: "auth.login",
    detail: { account: user.account, role: user.role.name },
    requestId: auditRequestId(request),
  });

  // iat 避开改密秒（sessionRevoked 的 same-second 边界，见 newSession）。
  const session = newSession({ ...user, passwordChangedAt: user.passwordChangedAt });
  const response = json({
    ok: true,
    user: {
      id: user.id,
      account: user.account,
      displayName: user.displayName,
      role: user.role.name,
    },
  });
  response.cookies.set(SESSION_COOKIE, signSession(session), {
    httpOnly: true,
    sameSite: "lax",
    secure: cookieSecure(),
    path: "/",
    maxAge: SESSION_TTL_SECONDS,
  });
  return response;
}
