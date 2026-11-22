import type { NextRequest } from "next/server";
import { NextResponse } from "next/server";

import { requireRoles, requireWritable } from "@/lib/api-auth";
import { auditRequestId, recordAudit } from "@/lib/audit";
import { prisma } from "@/lib/db";
import { apiError, json } from "@/lib/http";

export const runtime = "nodejs";

export async function POST(
  request: NextRequest,
  context: { params: Promise<{ id: string }> },
) {
  const auth = await requireRoles(request, ["admin"]);
  if (auth instanceof NextResponse) return auth;
  const readOnly = await requireWritable(request, auth.sub);
  if (readOnly) return readOnly;

  const { id } = await context.params;
  const key = await prisma.apiKey.findUnique({ where: { id }, include: { emp: true } });
  if (!key) return apiError("not_found", 404);

  await prisma.apiKey.update({ where: { id }, data: { revokedAt: new Date() } });
  await recordAudit({
    actorId: auth.sub,
    action: "key.revoke",
    detail: { keyId: id, empId: key.empId },
    requestId: auditRequestId(request),
  });
  return json({ ok: true });
}
