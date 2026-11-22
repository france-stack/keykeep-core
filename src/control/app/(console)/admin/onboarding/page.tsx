import { cookies } from "next/headers";
import { redirect } from "next/navigation";

import OnboardingWizard from "@/app/components/OnboardingWizard";
import { landingHref } from "@/app/components/nav";
import { onboardingStatus } from "@/lib/onboarding";
import { SESSION_COOKIE, verifySession } from "@/lib/session";

/**
 * FR-01 首次部署三步向导（FA-01 / T-01）：
 * 改管理员密码 → 接入首个供应商（可跳过，落内置演示上游）→
 * 签发第一把虚拟 Key 并看一次成功的测试请求。
 *
 * 判定「未完成」的 admin 由 proxy.ts 送进来；直接访问时按状态推导初始步骤，
 * 已完成的 admin 落回常规首页（不拦，向导页保留为可回看的引导）。
 */
export default async function OnboardingPage() {
  const token = (await cookies()).get(SESSION_COOKIE)?.value;
  const session = token ? verifySession(token) : null;
  if (!session) redirect("/login");
  if (session.role !== "admin") redirect(landingHref(session.role));

  const status = await onboardingStatus();
  return (
    <OnboardingWizard
      initialStep={status.step}
      demoAvailable={status.demoAvailable}
      passwordChanged={status.passwordChanged}
      hasProvider={status.hasRoutableProvider}
    />
  );
}
