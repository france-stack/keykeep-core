import type { NextRequest } from "next/server";
import { NextResponse } from "next/server";

import { requireRoles } from "@/lib/api-auth";
import { auditRequestId, recordAudit } from "@/lib/audit";
import { buildDiagnostics } from "@/lib/diagnostics";

export const runtime = "nodejs";

/** 只读诊断包：变量名/存在性、表计数、迁移、规则清单；不含任何秘密原值。 */
export async function GET(request: NextRequest) {
  const auth = await requireRoles(request, ["admin"]);
  if (auth instanceof NextResponse) return auth;

  try {
    const bundle = await buildDiagnostics();
    await recordAudit({
      actorId: auth.sub,
      action: "diagnostics.export",
      detail: { filename: bundle.filename, sha256: bundle.sha256, bytes: bundle.bytes.length },
      requestId: auditRequestId(request),
    });
    return new NextResponse(new Uint8Array(bundle.bytes), {
      status: 200,
      headers: {
        "Content-Type": "application/gzip",
        "Content-Disposition": `attachment; filename="${bundle.filename}"`,
        "Cache-Control": "no-store",
        "X-Diagnostics-SHA256": bundle.sha256,
      },
    });
  } catch (error) {
    console.error("diagnostics: build failed", error);
    return NextResponse.json({ error: { code: "diagnostics_failed" } }, { status: 500 });
  }
}
