import { cookies } from "next/headers";
import { redirect } from "next/navigation";

import SettingsClient from "@/app/components/SettingsClient";
import { SESSION_COOKIE, verifySession } from "@/lib/session";

/**
 * 设置：外观、动效、只读运行信息；两个角色共用（老板多一行「界面参考」）。
 * 与用量页同理，它不属于 /admin/*，落在 /me 下。
 * 旧地址 /admin/settings 保留为薄重定向（307），见 app/(console)/admin/settings/page.tsx。
 */
export default async function MeSettingsPage() {
  const token = (await cookies()).get(SESSION_COOKIE)?.value;
  const session = token ? verifySession(token) : null;
  if (!session) redirect("/login");

  return (
    <SettingsClient
      role={session.role}
      gatewayUrl={process.env.KEYKEEP_GATEWAY_URL ?? "http://127.0.0.1:8080"}
      version={process.env.KEYKEEP_VERSION ?? "0.1.0"}
    />
  );
}
