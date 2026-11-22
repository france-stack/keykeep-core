import type { NextRequest } from "next/server";
import { NextResponse } from "next/server";

import { requireRoles, requireWritable } from "@/lib/api-auth";
import { toBytes } from "@/lib/bytes";
import { encryptSecret, loadKek } from "@/lib/crypto";
import { auditRequestId, recordAudit } from "@/lib/audit";
import { prisma } from "@/lib/db";
import { apiError, json } from "@/lib/http";
import { UpstreamUrlError, assertSafeUpstreamUrl } from "@/lib/ssrf";

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
  const provider = await prisma.providerCredential.findUnique({ where: { id } });
  if (!provider) return apiError("not_found", 404);

  let body: {
    name?: string;
    baseUrl?: string;
    disabled?: boolean;
    apiKey?: string;
    authHeaderName?: string;
    authScheme?: string;
    streamTerminator?: string;
    models?: Array<{ alias?: string; modelId?: string }>;
  };
  try {
    body = (await request.json()) as typeof body;
  } catch {
    return apiError("invalid_request", 400);
  }

  const data: {
    name?: string;
    baseUrl?: string;
    disabled?: boolean;
    authHeaderName?: string;
    authScheme?: string;
    streamTerminator?: string;
    kid?: string;
    keyFp6?: string;
    ct?: Uint8Array<ArrayBuffer>;
    nonce?: Uint8Array<ArrayBuffer>;
  } = {};

  if (typeof body.name === "string" && body.name.trim()) data.name = body.name.trim();
  if (typeof body.baseUrl === "string" && body.baseUrl.trim()) {
    const baseUrl = body.baseUrl.trim().replace(/\/+$/, "");
    if (!/^https?:\/\//.test(baseUrl)) return apiError("invalid_request", 400, { reason: "base_url" });
    // 入库前过 SSRF 边界，与 POST 一致（连通性测试时还会再验一次）。
    try {
      await assertSafeUpstreamUrl(baseUrl);
    } catch (error) {
      const reason = error instanceof UpstreamUrlError ? error.message : "invalid_url";
      return apiError("upstream_not_allowed", 400, { reason });
    }
    data.baseUrl = baseUrl;
  }
  if (typeof body.disabled === "boolean") data.disabled = body.disabled;
  if (typeof body.authHeaderName === "string" && body.authHeaderName.trim()) {
    // 头名白名单字符集：换行/控制字符会被网关拼进请求头（头注入面）。
    const name = body.authHeaderName.trim();
    if (!/^[A-Za-z0-9-]{1,64}$/.test(name)) {
      return apiError("invalid_request", 400, { reason: "auth_header_name" });
    }
    data.authHeaderName = name;
  }
  if (typeof body.authScheme === "string") data.authScheme = body.authScheme.trim();
  if (typeof body.streamTerminator === "string") data.streamTerminator = body.streamTerminator;

  if (typeof body.apiKey === "string" && body.apiKey.length > 0) {
    try {
      const encrypted = encryptSecret(body.apiKey, loadKek());
      data.kid = encrypted.kid;
      data.keyFp6 = encrypted.keyFp6;
      data.ct = toBytes(encrypted.ct);
      data.nonce = toBytes(encrypted.nonce);
    } catch {
      return apiError("kek_not_configured", 500);
    }
  }

  if (Object.keys(data).length === 0 && !Array.isArray(body.models)) {
    return apiError("invalid_request", 400);
  }

  const updated = await prisma.providerCredential.update({
    where: { id },
    data,
    include: { modelMaps: true },
  });

  if (Array.isArray(body.models)) {
    const models = body.models
      .map((m) => ({ alias: (m.alias ?? "").trim(), modelId: (m.modelId ?? "").trim() }))
      .filter((m) => m.alias && m.modelId)
      .slice(0, 100);
    await prisma.$transaction([
      prisma.modelMap.deleteMany({ where: { credentialId: id } }),
      prisma.modelMap.createMany({
        data: models.map((m) => ({
          alias: m.alias,
          provider: updated.providerType,
          modelId: m.modelId,
          credentialId: id,
        })),
      }),
    ]);
  }

  await recordAudit({
    actorId: auth.sub,
    action: "provider.update",
    detail: {
      providerId: id,
      disabled: typeof body.disabled === "boolean" ? body.disabled : undefined,
      keyRotated: Boolean(data.ct),
      modelsReplaced: Array.isArray(body.models),
    },
    requestId: auditRequestId(request),
  });

  const fresh = await prisma.providerCredential.findUniqueOrThrow({
    where: { id },
    include: { modelMaps: { orderBy: { alias: "asc" } } },
  });

  return json({
    provider: {
      id: fresh.id,
      name: fresh.name,
      providerType: fresh.providerType,
      baseUrl: fresh.baseUrl,
      keyFp6: fresh.keyFp6,
      disabled: fresh.disabled,
      models: fresh.modelMaps.map((m) => ({ alias: m.alias, modelId: m.modelId })),
    },
  });
}
