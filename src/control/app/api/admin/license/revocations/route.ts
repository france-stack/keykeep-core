import type { NextRequest } from "next/server";
import { NextResponse } from "next/server";

import { requireRoles } from "@/lib/api-auth";
import { auditRequestId, recordAudit } from "@/lib/audit";
import { apiError, json } from "@/lib/http";
import { getLicenseStatus, importRevocations, LicenseError, statusLabel } from "@/lib/license";

export const runtime = "nodejs";

export async function POST(request: NextRequest) {
  const auth = await requireRoles(request, ["admin"]);
  if (auth instanceof NextResponse) return auth;

  let body: { revocations?: unknown };
  try {
    body = (await request.json()) as { revocations?: unknown };
  } catch {
    return apiError("invalid_request", 400);
  }
  if (body.revocations === undefined) return apiError("invalid_request", 400);

  try {
    const status = await importRevocations(body.revocations);
    await recordAudit({
      actorId: auth.sub,
      action: "license.revocation_import",
      detail: { version: status.revocationVersion, status: status.status },
      requestId: auditRequestId(request),
    });
    return json({ status, label: statusLabel(status.status) });
  } catch (error) {
    if (error instanceof LicenseError) return apiError("invalid_revocations", 400, { reason: error.message });
    console.error("license: revocation import failed", error);
    return apiError("revocations_import_failed", 500);
  }
}

export async function GET(request: NextRequest) {
  const auth = await requireRoles(request, ["admin"]);
  if (auth instanceof NextResponse) return auth;
  const status = await getLicenseStatus();
  return json({ revocationVersion: status.revocationVersion });
}
