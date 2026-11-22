import type { NextRequest } from "next/server";
import { NextResponse } from "next/server";

import { auditRequestId, recordAudit } from "./audit";
import { checkCsrf } from "./csrf";
import { prisma } from "./db";
import { apiError } from "./http";
import { getLicenseStatus } from "./license";
import { SESSION_COOKIE, sessionRevoked, verifySession, type Session } from "./session";

function requestHost(request: NextRequest): string {
  // 只信 Host：X-Forwarded-Host 是客户端可设头，进白名单等于 CSRF 校验可被绕过
  // （直连部署没有可信反代来清洗它）。桌面版/容器都直连，无需 XFH 分支。
  return request.headers.get("host") ?? request.nextUrl.host;
}

/** 写请求的 CSRF 校验；失败时写审计并返回 403。GET 等安全方法直接放行。 */
export function assertSameOrigin(request: NextRequest): NextResponse | null {
  const decision = checkCsrf(request.method, request.headers, requestHost(request));
  if (decision.ok) return null;
  void recordAudit({
    action: "security.csrf_rejected",
    detail: { reason: decision.reason, origin: decision.origin, path: request.nextUrl.pathname },
    requestId: auditRequestId(request),
  });
  return apiError("csrf_rejected", 403);
}

export function readSession(request: NextRequest): Session | null {
  const token = request.cookies.get(SESSION_COOKIE)?.value;
  return token ? verifySession(token) : null;
}

/**
 * 服务端撤销的唯一判定处：把「cookie 签名有效」升级为「库里这个账号此刻仍然可用」。
 * 停用账号、重置口令后旧会话立即失效（此前是纯无状态 cookie，最长还能用到 8 小时过期）。
 *
 * API 鉴权（requireRoles）与页面路由（(console) layout、/login）都必须走这里，
 * 否则会出现「页面放行、接口 401」两套口径 —— 那正是 /me↔/login 死循环的根因。
 * 控制面是低流量管理面，这次主键查询的成本可以忽略，安全收益是模型级的。
 */
export async function activeSessionOrNull(session: Session | null): Promise<Session | null> {
  if (!session) return null;
  const user = await prisma.user.findUnique({
    where: { id: session.sub },
    select: { disabled: true, passwordChangedAt: true },
  });
  if (!user || sessionRevoked(session, user)) return null;
  return session;
}

/**
 * API 鉴权唯一入口：CSRF → 会话签名/过期 → 角色 → 服务端撤销。
 */
export async function requireRoles(request: NextRequest, roles: string[]): Promise<Session | NextResponse> {
  const csrf = assertSameOrigin(request);
  if (csrf) return csrf;
  const session = readSession(request);
  if (!session) return apiError("invalid_session", 401);
  if (roles.length > 0 && !roles.includes(session.role)) return apiError("forbidden", 403);
  if (!(await activeSessionOrNull(session))) return apiError("invalid_session", 401);
  return session;
}

/**
 * FR-35：授权不可写时只阻断控制面写操作；转发面不受影响。
 * 写 handler 在 requireRoles 之后调用；license 导入与连通性测试豁免。
 */
export async function requireWritable(
  request: NextRequest,
  actorId?: string | null,
): Promise<NextResponse | null> {
  if (request.method === "GET" || request.method === "HEAD") return null;
  const path = request.nextUrl.pathname;
  if (path.startsWith("/api/admin/license") || path.endsWith("/test")) return null;
  const status = await getLicenseStatus();
  if (status.writable) return null;
  void recordAudit({
    actorId: actorId ?? null,
    action: "license.read_only",
    detail: { path, status: status.status },
    requestId: auditRequestId(request),
  });
  return apiError("license_read_only", 403, { status: status.status });
}
