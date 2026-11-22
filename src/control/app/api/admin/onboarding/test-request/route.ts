import type { NextRequest } from "next/server";
import { NextResponse } from "next/server";

import { requireRoles } from "@/lib/api-auth";
import { auditRequestId, recordAudit } from "@/lib/audit";
import { apiError, json } from "@/lib/http";

export const runtime = "nodejs";

/** 响应预览截断长度；测试请求只需要「看到了什么」，不需要正文全文。 */
const PREVIEW_LIMIT = 120;
/** 端到端测试请求超时：演示上游毫秒级返回，5 秒足够慢网络下的真实上游。 */
const TIMEOUT_MS = 5000;

/**
 * 打码：与 scripts/acceptance/run_acceptance.py 的 mask() 同规则。
 * 向导页本来就有 Key 明文，但审计/日志里不该再多一份。
 */
function mask(text: string): string {
  return text
    .replace(/kk_[A-Za-z0-9_-]{43}/g, "kk_****")
    .replace(/sk-[A-Za-z0-9_-]{16,}/g, "sk-****")
    .replace(/((?:authorization|api-key|x-api-key)\s*[:=]\s*"?bearer\s+)[A-Za-z0-9._-]+/gi, "$1****");
}

/**
 * FR-01 向导第 3 步的「一次成功的测试请求」：经网关真实调用一次
 * /v1/messages（Anthropic 形状），让「签发 Key → 调用 → 计量」整条链路
 * 在首装页面上闭环（T-01 判据：最后一步显示一次成功的测试请求）。
 *
 * 服务端代理而不是浏览器直连：网关地址只有控制面知道（KEYKEEP_GATEWAY_URL），
 * 浏览器侧可能拿到的 PUBLIC 地址与部署形态（桌面回环/容器内网）不一致。
 * Key 明文随请求体经过这里，响应里只回状态、延迟与打码后的预览。
 */
export async function POST(request: NextRequest) {
  const auth = await requireRoles(request, ["admin"]);
  if (auth instanceof NextResponse) return auth;

  let body: { key?: string; model?: string };
  try {
    body = (await request.json()) as typeof body;
  } catch {
    return apiError("invalid_request", 400);
  }
  const key = body.key ?? "";
  const model = (body.model ?? "").trim();
  if (!key || !model) return apiError("invalid_request", 400);
  // 虚拟 Key 形状校验（lib/keys.ts 同规则）：防止这个端点被当成任意
  // Bearer 凭据的转发器用。
  if (!/^kk_[A-Za-z0-9_-]{43}$/.test(key)) return apiError("invalid_request", 400, { reason: "key_format" });

  const gatewayUrl = (process.env.KEYKEEP_GATEWAY_URL ?? "http://127.0.0.1:8080").replace(/\/+$/, "");
  const started = Date.now();
  try {
    const response = await fetch(`${gatewayUrl}/v1/messages`, {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        Authorization: `Bearer ${key}`,
        "anthropic-version": "2023-06-01",
      },
      body: JSON.stringify({
        model,
        max_tokens: 16,
        messages: [{ role: "user", content: "hi" }],
      }),
      redirect: "manual",
      signal: AbortSignal.timeout(TIMEOUT_MS),
    });
    const latency = Date.now() - started;
    const raw = (await response.text()).slice(0, 4096);
    await recordAudit({
      actorId: auth.sub,
      action: "onboarding.test_request",
      detail: { ok: response.ok, status: response.status, model, latencyMs: latency },
      requestId: auditRequestId(request),
    });
    return json({
      ok: response.ok,
      status: response.status,
      latency_ms: latency,
      preview: mask(raw).slice(0, PREVIEW_LIMIT),
    });
  } catch (error) {
    const name = error instanceof Error ? error.name : "Error";
    const reason = name === "TimeoutError" ? "timeout" : "connection_failed";
    const latency = Date.now() - started;
    await recordAudit({
      actorId: auth.sub,
      action: "onboarding.test_request",
      detail: { ok: false, error: reason, model, latencyMs: latency },
      requestId: auditRequestId(request),
    });
    return json({ ok: false, status: null, latency_ms: latency, error: reason });
  }
}
