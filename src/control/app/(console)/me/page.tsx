import { cookies } from "next/headers";
import { redirect } from "next/navigation";

import MyEndpointClient from "@/app/components/MyEndpointClient";
import { SESSION_COOKIE, verifySession } from "@/lib/session";

/**
 * 员工侧唯一的功能页：把「你的 Base URL / 模型 / Key」一次讲清楚。
 *
 * 老板点进这个地址也看得见，但看到的是自己名下的 Key，不会串到别人。
 */
export default async function MyEndpointPage() {
  const token = (await cookies()).get(SESSION_COOKIE)?.value;
  const session = token ? verifySession(token) : null;
  if (!session) redirect("/login");

  // 地址按「员工浏览器能不能访问」来定：
  // 生产用 KEYKEEP_PUBLIC_GATEWAY_URL（对外可解析的域名/端口）；
  // 开发默认取浏览器访问控制台时的同主机 + 网关端口，否则员工会照抄 127.0.0.1 而连不上。
  const gatewayUrl = process.env.KEYKEEP_PUBLIC_GATEWAY_URL ?? "";
  // 控制台自身的对外地址：局域网模式下桌面版会注入局域网 IP，
  // 员工照抄 `kkconnect login --server …` 才不会拿到 127.0.0.1。
  const consoleUrl = process.env.KEYKEEP_PUBLIC_CONSOLE_URL ?? "";
  return (
    <MyEndpointClient
      session={{ account: session.account, displayName: session.displayName, role: session.role }}
      gatewayUrl={gatewayUrl}
      gatewayPort={process.env.GATEWAY_PORT ?? "8080"}
      consoleUrl={consoleUrl}
    />
  );
}
