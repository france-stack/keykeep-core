import type { NextRequest } from "next/server";

import { auditRequestId, recordAudit } from "@/lib/audit";
import { assertSameOrigin, readSession } from "@/lib/api-auth";
import { json } from "@/lib/http";
import { SESSION_COOKIE, cookieSecure } from "@/lib/session";

export async function POST(request: NextRequest) {
  const csrf = assertSameOrigin(request);
  if (csrf) return csrf;
  const session = readSession(request);
  if (session) {
    await recordAudit({
      actorId: session.sub,
      action: "auth.logout",
      detail: { account: session.account },
      requestId: auditRequestId(request),
    });
  }
  const response = json({ ok: true });
  response.cookies.set(SESSION_COOKIE, "", {
    httpOnly: true,
    sameSite: "lax",
    secure: cookieSecure(),
    path: "/",
    maxAge: 0,
  });
  return response;
}
