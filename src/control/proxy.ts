import { NextResponse, type NextRequest } from "next/server";

import { verifySession, SESSION_COOKIE } from "@/lib/session";

/**
 * FR-01 首次部署向导的入口重定向。
 *
 * 为什么用 proxy.ts 而不是 (console)/layout：layout 拿不到当前 pathname
 * （Next 16 文档明确「Layouts do not re-render on navigation, so they do not
 * access pathname」），在里面重定向要么把所有 admin 页面都拦死、要么需要
 * 内部头 hack。proxy 在路由匹配前运行，request.nextUrl.pathname 是真实路径，
 * 且默认 Node.js 运行时，可以做 DB 查询（本仓库不用 edge runtime）。
 *
 * 只拦「登录着的 admin 打开控制台页面」这一种情况：
 * - 未登录放行 → /login 自己会处理；
 * - API 路由放行 → 接口有自己的鉴权与语义，向导页的 fetch 不能被重定向吞掉；
 * - 员工放行 → 向导是老板的流程，完成前员工本来就没有可用 Key。
 *
 * 判定失败（DB 不可达）不拦路：向导是引导，不是权限边界；
 * 判定不可得时放行，损失的是引导而不是可用性。
 */

const ONBOARDING_EXEMPT = [
  "/admin/onboarding", // 向导页本身
  "/api", // 接口自带鉴权；重定向会破坏 fetch 的 JSON 语义
  "/login",
  "/me/settings", // 授权只读态的详情跳转、会话状态查看
  "/license", // 授权页横幅的跳转目标
  "/admin/audit", // 向导完成后核对 onboarding.complete
  "/_next",
  "/provider-icons", // 静态资源
  "/downloads", // kkconnect 下载
  "/favicon",
  "/icon",
];

export async function proxy(request: NextRequest) {
  const { pathname } = request.nextUrl;

  // 静态资源与豁免路径先于一切判断放行，避免在每张图片上都查库。
  if (
    ONBOARDING_EXEMPT.some((prefix) => pathname === prefix || pathname.startsWith(prefix + "/"))
  ) {
    return NextResponse.next();
  }

  const token = request.cookies.get(SESSION_COOKIE)?.value;
  const session = token ? verifySession(token) : null;
  if (!session || session.role !== "admin") return NextResponse.next();

  let needed = false;
  try {
    const { onboardingStatus } = await import("@/lib/onboarding");
    needed = (await onboardingStatus()).needed;
  } catch {
    return NextResponse.next();
  }
  if (!needed) return NextResponse.next();

  const url = request.nextUrl.clone();
  url.pathname = "/admin/onboarding";
  url.search = "";
  return NextResponse.redirect(url);
}
