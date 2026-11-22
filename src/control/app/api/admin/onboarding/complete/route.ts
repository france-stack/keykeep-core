import type { NextRequest } from "next/server";
import { NextResponse } from "next/server";

import { requireRoles, requireWritable } from "@/lib/api-auth";
import { auditRequestId, recordAudit } from "@/lib/audit";
import { prisma } from "@/lib/db";
import { apiError, json } from "@/lib/http";
import { onboardingComplete, onboardingStatus } from "@/lib/onboarding";

export const runtime = "nodejs";

/**
 * 完成首次部署向导：复查状态、写审计。
 *
 * 跳过供应商分支允许在没有真实凭据时完成（桌面版有内置演示上游可用）；
 * 审计里如实记录 skippedProvider 与 testOk，不粉饰成「全配置完成」。
 *
 * 「完成」的状态就是这条审计事件本身：lib/onboarding.onboardingComplete 与
 * proxy 的 needed 判定都会回读 onboarding.complete 的 skippedProvider，
 * 所以跳过分支不会在下次刷新时被重新拉回向导。
 */
export async function POST(request: NextRequest) {
  const auth = await requireRoles(request, ["admin"]);
  if (auth instanceof NextResponse) return auth;
  const readOnly = await requireWritable(request, auth.sub);
  if (readOnly) return readOnly;

  let body: { skippedProvider?: boolean; keyIssued?: boolean; testOk?: boolean };
  try {
    body = (await request.json()) as typeof body;
  } catch {
    return apiError("invalid_request", 400);
  }

  const status = await onboardingStatus();
  const skippedProvider = body.skippedProvider === true && !status.hasRoutableProvider;
  if (!onboardingComplete({ passwordChanged: status.passwordChanged, hasRoutableProvider: status.hasRoutableProvider, skippedProvider })) {
    return apiError("onboarding_incomplete", 409, { status });
  }

  await recordAudit({
    actorId: auth.sub,
    action: "onboarding.complete",
    detail: {
      passwordChanged: status.passwordChanged,
      providerConnected: status.hasRoutableProvider,
      skippedProvider,
      keyIssued: body.keyIssued === true,
      testOk: body.testOk === true,
      demoAvailable: status.demoAvailable,
    },
    requestId: auditRequestId(request),
  });

  return json({ ok: true, skippedProvider });
}
