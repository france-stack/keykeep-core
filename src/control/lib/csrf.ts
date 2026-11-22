// CSRF 防护：仅校验浏览器可伪造的写请求来源。
// 规则（T-31）：Origin 存在且与本站不一致 → 拒绝；Origin: null → 拒绝；
// 没有 Origin 但带 Referer → 校验 Referer 源；两者都没有（CLI/脚本）→ 放行。
const SAFE_METHODS = new Set(["GET", "HEAD", "OPTIONS"]);

export type CsrfDecision = { ok: true } | { ok: false; reason: "origin_mismatch" | "null_origin" | "referer_mismatch"; origin: string | null };

function allowedOrigins(): string[] {
  return (process.env.KEYKEEP_ALLOWED_ORIGINS ?? "")
    .split(",")
    .map((value) => value.trim().replace(/\/+$/, ""))
    .filter(Boolean);
}

function hostOf(value: string): string | null {
  try {
    return new URL(value).host;
  } catch {
    return null;
  }
}

export function checkCsrf(
  method: string,
  headers: { get(name: string): string | null },
  requestHost: string,
): CsrfDecision {
  if (SAFE_METHODS.has(method.toUpperCase())) return { ok: true };

  const origin = headers.get("origin");
  if (origin !== null) {
    if (origin === "null" || origin.trim() === "") {
      return { ok: false, reason: "null_origin", origin };
    }
    const host = hostOf(origin);
    const allowed = new Set([requestHost, ...allowedOrigins().map((item) => hostOf(item)).filter((item): item is string => Boolean(item))]);
    if (!host || !allowed.has(host)) {
      return { ok: false, reason: "origin_mismatch", origin };
    }
    return { ok: true };
  }

  const referer = headers.get("referer");
  if (referer) {
    const host = hostOf(referer);
    if (!host || host !== requestHost) {
      return { ok: false, reason: "referer_mismatch", origin: null };
    }
  }
  return { ok: true };
}
