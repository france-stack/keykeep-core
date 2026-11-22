import { cookies } from "next/headers";
import { redirect } from "next/navigation";

import UsageClient from "@/app/components/UsageClient";
import { SESSION_COOKIE, verifySession } from "@/lib/session";

/**
 * 用量与请求：老板看全局，员工只看自己（范围在 API 层按角色收敛）。
 * 两个角色共用同一张页面，所以它不属于 /admin/* —— 员工不该在地址栏里看到管理区前缀。
 * 旧地址 /admin/usage 保留为薄重定向（307），见 app/(console)/admin/usage/page.tsx。
 */
export default async function MeUsagePage({
  searchParams,
}: {
  searchParams: Promise<{ tab?: string }>;
}) {
  const token = (await cookies()).get(SESSION_COOKIE)?.value;
  const session = token ? verifySession(token) : null;
  if (!session) redirect("/login");

  const tab = (await searchParams).tab === "requests" ? "requests" : "overview";
  return <UsageClient role={session.role} initialTab={tab} />;
}
