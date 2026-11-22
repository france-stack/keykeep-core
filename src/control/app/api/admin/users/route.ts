import type { NextRequest } from "next/server";
import { NextResponse } from "next/server";

import { requireRoles, requireWritable } from "@/lib/api-auth";
import { auditRequestId, recordAudit } from "@/lib/audit";
import { prisma } from "@/lib/db";
import { apiError, json } from "@/lib/http";
import { getLicenseStatus } from "@/lib/license";
import { hashPassword, validatePassword } from "@/lib/password";
import { isRole } from "@/lib/permissions";

export const runtime = "nodejs";

export async function GET(request: NextRequest) {
  const auth = await requireRoles(request, ["admin"]);
  if (auth instanceof NextResponse) return auth;

  const users = await prisma.user.findMany({
    include: { role: true },
    orderBy: { createdAt: "asc" },
    take: 500,
  });

  return json({
    users: users.map((user) => ({
      id: user.id,
      account: user.account,
      displayName: user.displayName,
      role: user.role.name,
      disabled: user.disabled,
      lockedUntil: user.lockedUntil,
      createdAt: user.createdAt,
    })),
  });
}

export async function POST(request: NextRequest) {
  const auth = await requireRoles(request, ["admin"]);
  if (auth instanceof NextResponse) return auth;
  const readOnly = await requireWritable(request, auth.sub);
  if (readOnly) return readOnly;

  let body: {
    account?: string;
    displayName?: string;
    role?: string;
    password?: string;
  };
  try {
    body = (await request.json()) as typeof body;
  } catch {
    return apiError("invalid_request", 400);
  }

  const account = (body.account ?? "").trim();
  const displayName = (body.displayName ?? "").trim();
  const roleName = body.role ?? "employee";
  const password = body.password ?? "";
  if (!account || !displayName || !isRole(roleName)) return apiError("invalid_request", 400);
  const passwordError = validatePassword(password);
  if (passwordError) return apiError("invalid_request", 400, { reason: passwordError });

  const role = await prisma.role.findUnique({ where: { name: roleName } });
  if (!role) return apiError("invalid_request", 400, { reason: "role_not_seeded" });

  if (roleName === "employee") {
    const license = await getLicenseStatus();
    if (license.writable && license.maxEmployees > 0) {
      const active = await prisma.user.count({ where: { roleId: role.id, disabled: false } });
      if (active >= license.maxEmployees) {
        return apiError("license_employee_limit", 403, { limit: license.maxEmployees });
      }
    }
  }

  try {
    const user = await prisma.user.create({
      data: {
        account,
        displayName,
        roleId: role.id,
        passwordHash: await hashPassword(password),
        passwordChangedAt: new Date(),
      },
    });
    await recordAudit({
      actorId: auth.sub,
      action: "user.create",
      detail: { userId: user.id, account: user.account, role: roleName },
      requestId: auditRequestId(request),
    });
    return json({ user: { id: user.id, account: user.account } }, 201);
  } catch {
    return apiError("conflict", 409);
  }
}
