import { cookies } from "next/headers";
import { redirect } from "next/navigation";

import { landingHref } from "@/app/components/nav";
import { SESSION_COOKIE, verifySession } from "@/lib/session";

/**
 * "/" 不再是一张页面（原来的「总览」已下线）：
 * 它只是登录后的入口，按角色跳到第一个能看见的导航项，避免落在一个空路由上。
 */
export default async function RootPage() {
  const token = (await cookies()).get(SESSION_COOKIE)?.value;
  const session = token ? verifySession(token) : null;
  if (!session) redirect("/login");

  redirect(landingHref(session.role));
}
