/**
 * 登录的 IP 级限速：账号级锁定挡不住"低频口令喷洒大量账号"，
 * 进程内固定窗口计数器给整个登录入口兜一道底。
 * 控制面单实例部署（桌面版/compose 均如此），进程内计数即全局计数；
 * 多实例时改为 Redis/PG 计数即可，接口不变。
 */

type window = { count: number; resetAt: number };

const buckets = new Map<string, window>();

/** 每 IP 每窗口允许的登录尝试上限（含失败；成功登录也计数，防撞库探测）。 */
export const MAX_ATTEMPTS_PER_WINDOW = 30;
export const WINDOW_SECONDS = 60;

function clientIp(headers: Headers): string {
  // 控制面直连部署（桌面版/compose），取 TCP 对端不可得时退回 Host 头时代值；
  // x-forwarded-for 只在可信反代后有意义，直连时它可被伪造，仅作聚合维度。
  return (
    headers.get("x-real-ip") ??
    headers.get("x-forwarded-for")?.split(",")[0]?.trim() ??
    "unknown"
  );
}

/** 返回 null 表示放行；否则为建议等待的秒数。 */
export function checkLoginAllowed(headers: Headers, now = Date.now()): number | null {
  const ip = clientIp(headers);
  const bucket = buckets.get(ip);
  if (!bucket || bucket.resetAt <= now) {
    buckets.set(ip, { count: 1, resetAt: now + WINDOW_SECONDS * 1000 });
    return null;
  }
  bucket.count += 1;
  if (bucket.count > MAX_ATTEMPTS_PER_WINDOW) {
    return Math.max(1, Math.ceil((bucket.resetAt - now) / 1000));
  }
  return null;
}

/** 测试与运维用：清空计数。 */
export function resetLoginLimiter(): void {
  buckets.clear();
}
