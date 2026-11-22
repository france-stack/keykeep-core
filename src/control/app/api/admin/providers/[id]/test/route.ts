import type { NextRequest } from "next/server";
import { NextResponse } from "next/server";

import { requireRoles } from "@/lib/api-auth";
import { decryptSecret, loadKek } from "@/lib/crypto";
import { auditRequestId, recordAudit } from "@/lib/audit";
import { prisma } from "@/lib/db";
import { apiError, json } from "@/lib/http";
import { UpstreamUrlError, assertSafeUpstreamUrl } from "@/lib/ssrf";

export const runtime = "nodejs";

export async function POST(
  request: NextRequest,
  context: { params: Promise<{ id: string }> },
) {
  const auth = await requireRoles(request, ["admin"]);
  if (auth instanceof NextResponse) return auth;

  const { id } = await context.params;
  const provider = await prisma.providerCredential.findUnique({ where: { id } });
  if (!provider) return apiError("not_found", 404);

  // 连通性测试会把解密后的上游 Key 发往 baseUrl：必须先过 SSRF 边界，
  // 否则被劫持的管理员会话可拿它探测内网/云元数据。
  try {
    await assertSafeUpstreamUrl(provider.baseUrl);
  } catch (error) {
    const reason = error instanceof UpstreamUrlError ? error.message : "invalid_url";
    await recordAudit({
      actorId: auth.sub,
      action: "provider.test",
      detail: { providerId: id, ok: false, error: reason },
      requestId: auditRequestId(request),
    });
    return apiError("upstream_not_allowed", 400, { reason });
  }

  let apiKey: string;
  try {
    apiKey = decryptSecret(Buffer.from(provider.ct), Buffer.from(provider.nonce), loadKek());
  } catch {
    return apiError("decrypt_failed", 500);
  }

  const protocol = provider.providerType === "custom" ? "openai" : provider.providerType;
  const base = provider.baseUrl.replace(/\/+$/, "");
  const headers: Record<string, string> = {};
  let url: string;
  if (protocol === "gemini" || protocol === "vertex") {
    // Gemini/Vertex 用 x-goog-api-key；base 可带 /v1beta。
    url = base.endsWith("/v1beta") ? `${base}/models` : `${base}/v1beta/models`;
    headers[provider.authHeaderName || "x-goog-api-key"] = apiKey;
  } else {
    url = base.endsWith("/v1") ? `${base}/models` : `${base}/v1/models`;
    headers[provider.authHeaderName || "Authorization"] = `${provider.authScheme} ${apiKey}`.trim();
    if (protocol === "anthropic") headers["anthropic-version"] = "2023-06-01";
  }

  const started = Date.now();
  try {
    const response = await fetch(url, {
      method: "GET",
      headers,
      // SSRF 边界只复核了初始 URL：默认跟随重定向会让 302 把解密后的
      // 上游 Key 带进未复核的内网地址（B8）。连通性测试不需要跟随，
      // 3xx 一律按测试失败处理。
      redirect: "manual",
      signal: AbortSignal.timeout(3000),
    });
    if (response.status >= 300 && response.status < 400) {
      await recordAudit({
        actorId: auth.sub,
        action: "provider.test",
        detail: { providerId: id, ok: false, status: response.status, error: "redirect_not_allowed" },
        requestId: auditRequestId(request),
      });
      return json({
        ok: false,
        status: response.status,
        latency_ms: Date.now() - started,
        error: "redirect_not_allowed",
      });
    }
    await recordAudit({
      actorId: auth.sub,
      action: "provider.test",
      detail: { providerId: id, ok: response.ok, status: response.status },
      requestId: auditRequestId(request),
    });
    return json({
      ok: response.ok,
      status: response.status,
      latency_ms: Date.now() - started,
    });
  } catch (error) {
    const name = error instanceof Error ? error.name : "Error";
    const reason = name === "TimeoutError" ? "timeout" : "connection_failed";
    await recordAudit({
      actorId: auth.sub,
      action: "provider.test",
      detail: { providerId: id, ok: false, error: reason },
      requestId: auditRequestId(request),
    });
    return json({
      ok: false,
      status: null,
      latency_ms: Date.now() - started,
      error: reason,
    });
  }
}
