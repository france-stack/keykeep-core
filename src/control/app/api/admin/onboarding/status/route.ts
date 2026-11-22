import type { NextRequest } from "next/server";
import { NextResponse } from "next/server";

import { requireRoles } from "@/lib/api-auth";
import { json } from "@/lib/http";
import { onboardingStatus } from "@/lib/onboarding";

export const runtime = "nodejs";

/** 向导状态：页面按 step 初始化，刷新不丢进度。 */
export async function GET(request: NextRequest) {
  const auth = await requireRoles(request, ["admin"]);
  if (auth instanceof NextResponse) return auth;
  return json(await onboardingStatus());
}
