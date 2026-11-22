import type { NextRequest } from "next/server";
import { NextResponse } from "next/server";

import { requireRoles, requireWritable } from "@/lib/api-auth";
import { auditRequestId, recordAudit } from "@/lib/audit";
import { apiError, json } from "@/lib/http";
import { syncModelCatalog } from "@/lib/model-catalog";

export const runtime = "nodejs";
// 目录有 8 千多个模型，写库是分批事务；给足时间，别按默认 15 s 掐断。
export const maxDuration = 120;

/** FR-11：从 models.dev 拉取模型目录。价格只补版本、不覆盖既有版本。 */
export async function POST(request: NextRequest) {
  const auth = await requireRoles(request, ["admin"]);
  if (auth instanceof NextResponse) return auth;
  const readOnly = await requireWritable(request, auth.sub);
  if (readOnly) return readOnly;

  try {
    const summary = await syncModelCatalog();
    await recordAudit({
      actorId: auth.sub,
      action: "model.catalog.sync",
      detail: {
        version: summary.version,
        providers: summary.providers,
        models: summary.models,
        priced: summary.priced,
      },
      requestId: auditRequestId(request),
    });
    return json(summary);
  } catch (error) {
    const message = error instanceof Error ? error.message : "sync failed";
    await recordAudit({
      actorId: auth.sub,
      action: "model.catalog.sync_failed",
      detail: { reason: message.slice(0, 200) },
      requestId: auditRequestId(request),
    });
    return apiError("catalog_sync_failed", 502, { reason: message.slice(0, 200) });
  }
}

/** 只读探测：报告数据源与缓存状态，不写库。 */
export async function GET(request: NextRequest) {
  const auth = await requireRoles(request, ["admin"]);
  if (auth instanceof NextResponse) return auth;
  const models = await prismaCount();
  return json({ ...models, source: catalogSourceSafe() });
}

async function prismaCount() {
  const { prisma } = await import("@/lib/db");
  const [models, providers, versions] = await Promise.all([
    prisma.modelCatalog.count(),
    prisma.modelPrice.count(),
    prisma.$queryRaw<Array<{ version: string; models: bigint }>>`
      SELECT version, COUNT(*)::bigint AS models FROM "ModelPrice" GROUP BY version ORDER BY version DESC LIMIT 5`,
  ]);
  return {
    catalogRows: models,
    priceRows: providers,
    versions: versions.map((row) => ({ version: row.version, models: Number(row.models) })),
  };
}

function catalogSourceSafe() {
  return process.env.MODEL_CATALOG_SOURCE?.trim() || "https://models.dev/api.json";
}
