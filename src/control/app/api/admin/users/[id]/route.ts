import type { NextRequest } from "next/server";
import { NextResponse } from "next/server";

import { requireRoles, requireWritable } from "@/lib/api-auth";
import { auditRequestId, recordAudit } from "@/lib/audit";
import { prisma } from "@/lib/db";
import { apiError, json } from "@/lib/http";
import { hashPassword, validatePassword } from "@/lib/password";

export const runtime = "nodejs";

export async function PATCH(
  request: NextRequest,
  context: { params: Promise<{ id: string }> },
) {
  const auth = await requireRoles(request, ["admin"]);
  if (auth instanceof NextResponse) return auth;
  const readOnly = await requireWritable(request, auth.sub);
  if (readOnly) return readOnly;

  const { id } = await context.params;
  const target = await prisma.user.findUnique({ where: { id } });
  if (!target) return apiError("not_found", 404);

  let body: { disabled?: boolean; password?: string; displayName?: string };
  try {
    body = (await request.json()) as typeof body;
  } catch {
    return apiError("invalid_request", 400);
  }

  const data: {
    disabled?: boolean;
    displayName?: string;
    passwordHash?: string;
    passwordChangedAt?: Date;
    failedAttempts?: number;
    lockedUntil?: null;
  } = {};

  if (typeof body.disabled === "boolean") {
    if (body.disabled && id === auth.sub) return apiError("invalid_request", 400, { reason: "cannot_disable_self" });
    data.disabled = body.disabled;
  }
  if (typeof body.displayName === "string" && body.displayName.trim()) {
    data.displayName = body.displayName.trim();
  }
  if (typeof body.password === "string" && body.password.length > 0) {
    const passwordError = validatePassword(body.password);
    if (passwordError) return apiError("invalid_request", 400, { reason: passwordError });
    data.passwordHash = await hashPassword(body.password);
    data.passwordChangedAt = new Date();
    data.failedAttempts = 0;
    data.lockedUntil = null;
  }

  if (Object.keys(data).length === 0) return apiError("invalid_request", 400);

  const user = await prisma.user.update({ where: { id }, data });
  await recordAudit({
    actorId: auth.sub,
    action: "user.update",
    detail: {
      userId: id,
      disabled: typeof body.disabled === "boolean" ? body.disabled : undefined,
      passwordReset: Boolean(data.passwordHash),
      displayNameChanged: Boolean(data.displayName),
    },
    requestId: auditRequestId(request),
  });
  return json({ user: { id: user.id, account: user.account, disabled: user.disabled } });
}
