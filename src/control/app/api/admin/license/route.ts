import type { NextRequest } from "next/server";
import { NextResponse } from "next/server";

import { requireRoles } from "@/lib/api-auth";
import { auditRequestId, recordAudit } from "@/lib/audit";
import { apiError, json } from "@/lib/http";
import { getLicenseStatus, importLicense, LicenseError, statusLabel } from "@/lib/license";

export const runtime = "nodejs";

export async function GET(request: NextRequest) {
  const auth = await requireRoles(request, ["admin"]);
  if (auth instanceof NextResponse) return auth;
  const status = await getLicenseStatus();
  return json({ status, label: statusLabel(status.status) });
}

export async function POST(request: NextRequest) {
  const auth = await requireRoles(request, ["admin"]);
  if (auth instanceof NextResponse) return auth;

  let body: { license?: unknown };
  try {
    body = (await request.json()) as { license?: unknown };
  } catch {
    return apiError("invalid_request", 400);
  }
  if (body.license === undefined) return apiError("invalid_request", 400);

  try {
    const status = await getLicenseStatus(true).catch(() => null);
    const imported = await importLicense(body.license);
    await recordAudit({
      actorId: auth.sub,
      action: imported.status === "invalid_signature" ? "license.invalid_signature" : "license.import",
      detail: { licenseId: imported.licenseId, status: imported.status, previous: status?.status ?? null },
      requestId: auditRequestId(request),
    });
    if (imported.status === "invalid_signature") {
      return json({ error: { code: "invalid_signature" }, status: imported }, 400);
    }
    return json({ status: imported, label: statusLabel(imported.status) });
  } catch (error) {
    if (error instanceof LicenseError) return apiError("invalid_license", 400, { reason: error.message });
    return apiError("license_import_failed", 500);
  }
}
