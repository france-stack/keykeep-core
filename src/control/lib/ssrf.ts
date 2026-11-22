import { lookup } from "node:dns/promises";
import { isIP } from "node:net";

/**
 * 上游地址的 SSRF 边界。
 *
 * 供应商连通性测试会把解密后的上游 API Key 发往 baseUrl，等于一个
 * 可配置的出网点；不设边界时，被劫持的管理员会话可以让控制面探测任意
 * 内网地址（云元数据 169.254.169.254、内网管理端口等）。
 *
 * 边界设计（与产品形态对齐）：
 *  - 仅允许 http/https；
 *  - 回环恒允许：桌面版的演示上游 kkmock 就跑在 127.0.0.1；
 *  - 云元数据链路本地段（169.254.0.0/16、fd00:fec::/…）恒禁止；
 *  - 私网段（10/8、172.16/12、192.168/16）默认禁止，桌面/内网聚合器
 *    部署可用 KEYKEEP_ALLOW_PRIVATE_UPSTREAMS=true 显式放开；
 *  - 域名先解析再逐个校验解析结果，防 DNS rebinding 绕过。
 *
 * 注意：本函数只校验初始 URL。fetch 默认跟随重定向，302 可跳进未复核的
 * 内网地址（B8）；因此所有发往 baseUrl 的 fetch 必须 redirect:"manual"，
 * 并把 3xx 视为失败。确需跟随的调用点必须逐跳重新调用本函数。
 */

export class UpstreamUrlError extends Error {
  constructor(reason: string) {
    super(reason);
    this.name = "UpstreamUrlError";
  }
}

function allowPrivate(): boolean {
  return process.env.KEYKEEP_ALLOW_PRIVATE_UPSTREAMS === "true";
}

function ipBlocked(ip: string): string | null {
  const version = isIP(ip);
  if (version === 4) {
    const parts = ip.split(".").map(Number);
    if (parts[0] === 169 && parts[1] === 254) return "link_local";
    if (parts[0] === 127) return null; // 回环恒允许（演示上游）
    if (parts[0] === 0) return "unspecified";
    if (
      parts[0] === 10 ||
      (parts[0] === 172 && parts[1] >= 16 && parts[1] <= 31) ||
      (parts[0] === 192 && parts[1] === 168)
    ) {
      // 私网默认禁止；KEYKEEP_ALLOW_PRIVATE_UPSTREAMS=true 按文档承诺显式放开
      // （此前逃生门只对 IPv6 ULA 生效，IPv4 私网恒拦——E2E 复跑实测暴露）。
      return allowPrivate() ? null : "private";
    }
    return null;
  }
  if (version === 6) {
    const lower = ip.toLowerCase();
    if (lower === "::1") return null; // 回环恒允许
    if (lower === "::") return "unspecified";
    if (lower.startsWith("fe80")) return "link_local";
    if (lower.startsWith("fc") || lower.startsWith("fd")) {
      return allowPrivate() ? null : "private";
    }
    // IPv4 映射地址按 IPv4 规则复核，防止 ::ffff:169.254.169.254 绕过。
    // 两种形态都要认：调用方直传的是点分十进制，而 `new URL()` 会把
    // hostname 规范化成十六进制（::ffff:a9fe:a9fe）——只认一种就漏防
    // （2026-11-08 ssrf.test.ts 实测两种形态都真实可达）。
    const mapped = lower.match(/^::ffff:(\d+\.\d+\.\d+\.\d+)$/);
    if (mapped) return ipBlocked(mapped[1]);
    const hexMapped = lower.match(/^::ffff:([0-9a-f]{1,4}):([0-9a-f]{1,4})$/);
    if (hexMapped) {
      const hi = parseInt(hexMapped[1], 16);
      const lo = parseInt(hexMapped[2], 16);
      return ipBlocked(`${(hi >> 8) & 0xff}.${hi & 0xff}.${(lo >> 8) & 0xff}.${lo & 0xff}`);
    }
    return null;
  }
  return "invalid";
}

/** 校验一个上游根地址；违规抛 UpstreamUrlError。返回解析后的主机名列表便于诊断。 */
export async function assertSafeUpstreamUrl(raw: string): Promise<void> {
  let parsed: URL;
  try {
    parsed = new URL(raw);
  } catch {
    throw new UpstreamUrlError("invalid_url");
  }
  if (parsed.protocol !== "http:" && parsed.protocol !== "https:") {
    throw new UpstreamUrlError("scheme_not_allowed");
  }
  const host = parsed.hostname.replace(/^\[|\]$/g, "");
  if (!host) throw new UpstreamUrlError("empty_host");

  const direct = isIP(host);
  if (direct !== 0) {
    const reason = ipBlocked(host);
    if (reason) throw new UpstreamUrlError(`blocked_${reason}`);
    return;
  }
  let addresses;
  try {
    addresses = await lookup(host, { all: true, verbatim: true });
  } catch {
    throw new UpstreamUrlError("dns_lookup_failed");
  }
  if (addresses.length === 0) throw new UpstreamUrlError("dns_lookup_failed");
  for (const { address } of addresses) {
    const reason = ipBlocked(address);
    if (reason) throw new UpstreamUrlError(`blocked_${reason}`);
  }
}
