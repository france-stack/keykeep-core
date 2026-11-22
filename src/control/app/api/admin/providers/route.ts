import type { NextRequest } from "next/server";
import { NextResponse } from "next/server";

import { requireRoles, requireWritable } from "@/lib/api-auth";
import { toBytes } from "@/lib/bytes";
import { encryptSecret, loadKek } from "@/lib/crypto";
import { auditRequestId, recordAudit } from "@/lib/audit";
import { prisma } from "@/lib/db";
import { apiError, json } from "@/lib/http";
import { findCatalogEntry } from "@/lib/provider-catalog";
import { UpstreamUrlError, assertSafeUpstreamUrl } from "@/lib/ssrf";

/** 上游请求头名的白名单字符集；换行/空格/控制字符一律拒绝（防头注入）。 */
const HEADER_NAME_RE = /^[A-Za-z0-9-]{1,64}$/;

function sanitizeHeaderName(raw: string): string {
  const name = raw.trim();
  return HEADER_NAME_RE.test(name) ? name : "Authorization";
}

/** extraHeaders 只接受扁平 string→string map（键名白名单字符集、值限长）。 */
function sanitizeExtraHeaders(raw: unknown): Record<string, string> | null {
  if (raw === undefined || raw === null) return {};
  if (typeof raw !== "object" || Array.isArray(raw)) return null;
  const out: Record<string, string> = {};
  for (const [key, value] of Object.entries(raw as Record<string, unknown>)) {
    if (!HEADER_NAME_RE.test(key)) return null;
    if (typeof value !== "string" || value.length > 1024) return null;
    out[key] = value;
  }
  return out;
}

export const runtime = "nodejs";

const PROTOCOLS = new Set(["openai", "anthropic", "gemini", "vertex", "xai", "codex", "custom"]);

type ModelInput = { alias?: string; modelId?: string };

function mask(provider: {
  id: string;
  provider: string;
  name: string;
  providerType: string;
  baseUrl: string;
  authHeaderName: string;
  authScheme: string;
  streamTerminator: string;
  keyFp6: string;
  disabled: boolean;
  createdAt: Date;
  modelMaps: Array<{ alias: string; modelId: string }>;
}) {
  return {
    id: provider.id,
    provider: provider.provider,
    name: provider.name,
    providerType: provider.providerType,
    baseUrl: provider.baseUrl,
    authHeaderName: provider.authHeaderName,
    authScheme: provider.authScheme,
    streamTerminator: provider.streamTerminator,
    keyFp6: provider.keyFp6,
    disabled: provider.disabled,
    createdAt: provider.createdAt,
    models: provider.modelMaps.map((m) => ({ alias: m.alias, modelId: m.modelId })),
  };
}

export async function GET(request: NextRequest) {
  const auth = await requireRoles(request, ["admin"]);
  if (auth instanceof NextResponse) return auth;

  const providers = await prisma.providerCredential.findMany({
    include: { modelMaps: { orderBy: { alias: "asc" } } },
    orderBy: { createdAt: "desc" },
  });
  return json({ providers: providers.map(mask) });
}

export async function POST(request: NextRequest) {
  const auth = await requireRoles(request, ["admin"]);
  if (auth instanceof NextResponse) return auth;
  const readOnly = await requireWritable(request, auth.sub);
  if (readOnly) return readOnly;

  let body: {
    catalogId?: string;
    name?: string;
    providerType?: string;
    protocol?: string;
    baseUrl?: string;
    apiKey?: string;
    authHeaderName?: string;
    authScheme?: string;
    extraHeaders?: Record<string, string>;
    limitDims?: string[];
    streamTerminator?: string;
    models?: ModelInput[];
  };
  try {
    body = (await request.json()) as typeof body;
  } catch {
    return apiError("invalid_request", 400);
  }

  const entry = findCatalogEntry((body.catalogId ?? "").trim());
  if (entry?.status === "unsupported") {
    return apiError("provider_unsupported", 400, { catalogId: entry.id, reason: entry.note });
  }

  const protocolInput = (body.protocol ?? body.providerType ?? entry?.protocol ?? "").trim();
  const providerType = protocolInput === "custom" ? "openai" : protocolInput;
  const catalogId = entry?.id ?? ((body.catalogId ?? "").trim() || providerType);
  const name = (body.name ?? entry?.name ?? "").trim();
  const baseUrl = (body.baseUrl ?? entry?.baseUrl ?? "").trim().replace(/\/+$/, "");
  const apiKey = body.apiKey ?? "";
  if (!PROTOCOLS.has(protocolInput) || !name || !apiKey) {
    return apiError("invalid_request", 400);
  }
  if (!baseUrl) return apiError("base_url_required", 400, { catalogId });
  if (!/^https?:\/\//.test(baseUrl)) return apiError("invalid_request", 400, { reason: "base_url" });
  // 入库前就过 SSRF 边界（连通性测试时再验一次）：网关与测试都会把上游 Key 发往这里。
  try {
    await assertSafeUpstreamUrl(baseUrl);
  } catch (error) {
    const reason = error instanceof UpstreamUrlError ? error.message : "invalid_url";
    return apiError("upstream_not_allowed", 400, { reason });
  }
  // extraHeaders 直接进上游请求头：限定扁平 string→string，头名白名单字符集，
  // 否则任意 JSON（任意深度/类型）都会被网关原样拼进请求。
  const extraHeaders = sanitizeExtraHeaders(body.extraHeaders);
  if (extraHeaders === null) return apiError("invalid_request", 400, { reason: "extra_headers" });

  const models = (body.models ?? [])
    .map((m) => ({ alias: (m.alias ?? "").trim(), modelId: (m.modelId ?? "").trim() }))
    .filter((m) => m.alias && m.modelId)
    .slice(0, 100);

  let kek: Buffer;
  try {
    kek = loadKek();
  } catch {
    return apiError("kek_not_configured", 500);
  }
  const encrypted = encryptSecret(apiKey, kek);

  // 同一把上游 Key 在同一协议下只允许一条凭据（唯一键 kid+keyFp6+providerType，
  // ADR-009）：同协议重复接入返回 409，让调用方去 PATCH 已有凭据；不同协议
  // 各建一条是允许的（DeepSeek 的 /v1 与 /anthropic 就此并存）。
  const duplicate = await prisma.providerCredential.findUnique({
    where: {
      kid_keyFp6_providerType: {
        kid: encrypted.kid,
        keyFp6: encrypted.keyFp6,
        providerType,
      },
    },
    select: { id: true, name: true, baseUrl: true },
  });
  if (duplicate) {
    return apiError("credential_exists", 409, {
      providerId: duplicate.id,
      name: duplicate.name,
      baseUrl: duplicate.baseUrl,
    });
  }

  const provider = await prisma.providerCredential.create({
    data: {
      provider: catalogId,
      name,
      providerType,
      baseUrl,
      authHeaderName: sanitizeHeaderName(body.authHeaderName ?? entry?.auth.header ?? "Authorization"),
      authScheme: (body.authScheme ?? entry?.auth.scheme ?? "Bearer").trim(),
      extraHeaders,
      limitDims: body.limitDims ?? [],
      streamTerminator: body.streamTerminator ?? "",
      kid: encrypted.kid,
      keyFp6: encrypted.keyFp6,
      ct: toBytes(encrypted.ct),
      nonce: toBytes(encrypted.nonce),
      modelMaps: {
        create: models.map((m) => ({ alias: m.alias, provider: providerType, modelId: m.modelId })),
      },
    },
    include: { modelMaps: true },
  });

  await recordAudit({
    actorId: auth.sub,
    action: "provider.create",
    detail: { providerId: provider.id, name, providerType, catalogId, modelCount: provider.modelMaps.length },
    requestId: auditRequestId(request),
  });

  return json({ provider: mask(provider) }, 201);
}
