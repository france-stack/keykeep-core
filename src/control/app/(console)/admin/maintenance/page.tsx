import { cookies } from "next/headers";
import { redirect } from "next/navigation";

import MaintenanceClient from "@/app/components/MaintenanceClient";
import { landingHref } from "@/app/components/nav";
import { maintenanceEnabled } from "@/lib/maintenance";
import { SESSION_COOKIE, verifySession } from "@/lib/session";

export const runtime = "nodejs";

export default async function MaintenancePage() {
  const token = (await cookies()).get(SESSION_COOKIE)?.value;
  const session = token ? verifySession(token) : null;
  if (!session) redirect("/login");
  if (session.role !== "admin") redirect(landingHref(session.role));

  // 页面对 admin 常驻（侧栏不再有 disabled 占位），但功能体由维护开关驱动：
  // 关闭时页面只渲染「默认关闭」说明——这正是 RM-A01 要的面貌：没有入口、没有监听，
  // 管理员看到的也是「未开启」，而不是一个可进入的维护工作台。
  return <MaintenanceClient enabled={maintenanceEnabled()} />;
}
