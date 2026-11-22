import { cookies } from "next/headers";
import { redirect } from "next/navigation";

import { landingHref } from "@/app/components/nav";
import LoginForm from "../components/LoginForm";
import { activeSessionOrNull } from "@/lib/api-auth";
import { SESSION_COOKIE, verifySession, type Session } from "@/lib/session";

export default async function LoginPage({
  searchParams,
}: {
  searchParams: Promise<{ reason?: string }>;
}) {
  const revoked = (await searchParams).reason === "session_revoked";
  const token = (await cookies()).get(SESSION_COOKIE)?.value;
  const signed = token ? verifySession(token) : null;

  // 只按 cookie 签名就放行，正是 /me↔/login 死循环的最后一环：停用账号后
  // /me 的接口 401 → 客户端跳登录 → 登录页又把人送回 /me。所以这里必须和
  // (console) layout、API 鉴权做同一次库回查，三处口径统一。
  //
  // 回查失败（库不可达）按「不可信」处理：不能放行，但也绝不能把登录页打成 500 ——
  // 登录页是数据库故障时唯一的恢复入口。所以这里吞掉异常、退回登录表单。
  let session: Session | null = null;
  if (signed) {
    try {
      session = await activeSessionOrNull(signed);
    } catch {
      session = null;
    }
  }

  // 只有「签名有效 + 库里仍可用 + 不是被撤销踢出来的」才直接进控制台。
  // reason=session_revoked 时不立刻回跳：那一刻的库状态可能刚好又变了
  // （例如刚被重新启用），回跳会立刻绕回同一个跳转环。让用户自己重新登录。
  if (session && !revoked) redirect(landingHref(session.role));

  // 走到这里意味着：带着失效会话进来（或已失效 / 已过期 / 没有 cookie）。
  // 失效 cookie 必须清掉，但 RSC 里 cookies() 只读，写会抛错；
  // 由 LoginForm 挂载时最佳努力 POST /api/auth/logout 完成。
  const notice = revoked || signed ? "session_revoked" : undefined;
  return <LoginForm notice={notice} />;
}
