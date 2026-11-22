import { cookies } from "next/headers";
import { redirect } from "next/navigation";

import AppShell from "@/app/components/AppShell";
import { activeSessionOrNull } from "@/lib/api-auth";
import { getLicenseStatus } from "@/lib/license";
import { SESSION_COOKIE, verifySession } from "@/lib/session";

export default async function ConsoleLayout({ children }: { children: React.ReactNode }) {
  const token = (await cookies()).get(SESSION_COOKIE)?.value;
  const signed = token ? verifySession(token) : null;
  if (!signed) redirect("/login");
  // 签名有效 ≠ 会话有效：停用账号、重置口令不改 JWT，必须回查库。
  // 不回查就会出现「页面放行 → 接口 401 → 客户端跳登录 → 登录页又按签名放行」的死循环。
  // RSC 里 cookies() 只读，不能在这里清 cookie；失效 cookie 由 /login 的客户端逻辑清理。
  const session = await activeSessionOrNull(signed);
  if (!session) redirect("/login?reason=session_revoked");

  const license = await getLicenseStatus();
  return (
    <AppShell
      session={{ account: session.account, displayName: session.displayName, role: session.role }}
      license={{ status: license.status, message: license.message, writable: license.writable }}
    >
      {children}
    </AppShell>
  );
}
