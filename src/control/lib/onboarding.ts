// FR-01 首次部署向导的判定逻辑。
//
// 「需要向导」必须能从数据库直接回答，不能靠前端记忆或环境变量——
// 否则换台机器、清个 cookie，状态就分叉了。两个判定条件：
//
//   passwordChanged      管理员改过初始口令（seed 建号与写 passwordChangedAt
//                        几乎同时；真实改密必然晚于建号，留 2 秒容差防时钟巧合）
//   hasRoutableProvider  存在启用中的、非演示来源（provider != "mock"）的供应商凭据。
//                        演示上游是「跳过」分支的落点，不该自己触发向导。

import { prisma } from "./db";

export type OnboardingStatus = {
  passwordChanged: boolean;
  hasRoutableProvider: boolean;
  /** 演示上游（provider='mock'）是否启用中；跳过分支的提示用。 */
  demoAvailable: boolean;
  /** 用户是否已在向导里显式确认「跳过接入供应商」（落库的审计事实）。 */
  providerSkipped: boolean;
  /** 由上述状态推导的向导步骤：1 改密 → 2 接供应商 → 3 签发 Key 并测试。 */
  step: 1 | 2 | 3;
  needed: boolean;
};

/** seed 建号与 passwordChangedAt 同刻写入的容差；超过 2 秒视为真实改密。 */
export const SEED_TIMESTAMP_TOLERANCE_MS = 2000;

export function isAdminPasswordChanged(user: {
  passwordChangedAt: Date | null;
  createdAt: Date;
}): boolean {
  if (!user.passwordChangedAt) return false;
  return user.passwordChangedAt.getTime() - user.createdAt.getTime() > SEED_TIMESTAMP_TOLERANCE_MS;
}

export function computeStep(status: Pick<OnboardingStatus, "passwordChanged" | "hasRoutableProvider">): 1 | 2 | 3 {
  if (!status.passwordChanged) return 1;
  if (!status.hasRoutableProvider) return 2;
  return 3;
}

export async function onboardingStatus(): Promise<OnboardingStatus> {
  const [admins, provider, demo] = await Promise.all([
    prisma.user.findMany({
      where: { role: { name: "admin" }, disabled: false },
      select: { passwordChangedAt: true, createdAt: true },
    }),
    prisma.providerCredential.findFirst({
      where: { disabled: false, provider: { not: "mock" } },
      select: { id: true },
    }),
    prisma.providerCredential.findFirst({
      where: { disabled: false, provider: "mock" },
      select: { id: true },
    }),
  ]);

  const passwordChanged = admins.some(isAdminPasswordChanged);
  const hasRoutableProvider = provider !== null;
  const demoAvailable = demo !== null;
  // 跳过供应商分支必须落库，否则「完成」只写了一条审计：needed 每次都由
  // hasRoutableProvider 重算，跳过路径的用户会永远被重定向回向导。
  const skipped = await onboardingProviderSkipped();
  const step = computeStep({ passwordChanged, hasRoutableProvider });
  return {
    passwordChanged,
    hasRoutableProvider,
    demoAvailable,
    providerSkipped: skipped,
    step,
    // 完成判定与完成接口共用 onboardingComplete，避免两处口径漂移。
    needed: !onboardingComplete({
      passwordChanged,
      hasRoutableProvider,
      skippedProvider: skipped,
    }),
  };
}

/**
 * 「跳过供应商接入」是否已被显式确认过。
 *
 * 用审计事件当持久化状态，而不是新建一张表：向导完成本来就是审计事件
 * （onboarding.complete），跳过也是一条明确的管理动作。查询按 action + 最近一条，
 * 数据量是「每实例一次」，不需要索引优化。
 */
export async function onboardingProviderSkipped(): Promise<boolean> {
  // 按 seq 排而不是 at：at 只有毫秒精度，同毫秒的多条事件分不出先后，
  // 而 seq 是数据库序列分配的全表单调递增值。
  const latest = await prisma.auditEvent.findFirst({
    where: { action: "onboarding.complete" },
    orderBy: { seq: "desc" },
    select: { detail: true },
  });
  if (!latest) return false;
  const detail = latest.detail as { skippedProvider?: unknown } | null;
  return detail?.skippedProvider === true;
}

/** 完成判定：改过密，且接了供应商（或显式走了跳过分支）。 */
export function onboardingComplete(args: {
  passwordChanged: boolean;
  hasRoutableProvider: boolean;
  skippedProvider: boolean;
}): boolean {
  return args.passwordChanged && (args.hasRoutableProvider || args.skippedProvider);
}
