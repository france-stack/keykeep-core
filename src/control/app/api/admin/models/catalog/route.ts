import type { NextRequest } from "next/server";
import { NextResponse } from "next/server";

import { requireRoles } from "@/lib/api-auth";
import { prisma } from "@/lib/db";
import { json } from "@/lib/http";
import { findCatalogEntry } from "@/lib/provider-catalog";

export const runtime = "nodejs";

/**
 * FR-11：按目录条目取 models.dev 的模型名，供「上游模型 ID」下拉用。
 *
 * 入参 catalogId 是 ProviderCredential.provider（目录 id，如 deepseek），
 * 不是 providerType；映射关系写在 provider-catalog.ts 的 catalogIds 字段里。
 * 没配 catalogIds 的条目返回空数组，前端退回自由输入。
 */
const PROTOCOL_FALLBACK: Record<string, string[]> = {
  anthropic: ["anthropic"],
  openai: ["openai"],
  gemini: ["google"],
  vertex: ["google-vertex"],
  xai: ["xai"],
  codex: ["openai"],
};

export async function GET(request: NextRequest) {
  const auth = await requireRoles(request, ["admin"]);
  if (auth instanceof NextResponse) return auth;

  const params = new URL(request.url).searchParams;
  const catalogId = (params.get("catalogId") ?? "").trim();
  const protocol = (params.get("protocol") ?? "").trim();
  const entry = catalogId ? findCatalogEntry(catalogId) : undefined;
  const catalogIds = entry?.catalogIds?.length
    ? entry.catalogIds
    : PROTOCOL_FALLBACK[protocol] ?? [];
  if (catalogIds.length === 0) return json({ catalogId, catalogIds, models: [] });

  const rows = await prisma.modelCatalog.findMany({
    where: { provider: { in: catalogIds }, type: "chat" },
    select: { modelId: true },
    orderBy: { modelId: "asc" },
    take: 500,
  });
  return json({ catalogId, catalogIds, models: rows.map((row) => row.modelId) });
}
