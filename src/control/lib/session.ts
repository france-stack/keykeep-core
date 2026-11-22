import { createHmac, timingSafeEqual } from "node:crypto";

export const SESSION_COOKIE = "kk_session";
export const SESSION_TTL_SECONDS = 8 * 60 * 60;

/** 生产默认 Secure；本地 HTTP（如 E2E compose）可用 KEYKEEP_COOKIE_SECURE=false 显式关闭。 */
export function cookieSecure(): boolean {
  const override = process.env.KEYKEEP_COOKIE_SECURE;
  if (override === "false") return false;
  if (override === "true") return true;
  return process.env.NODE_ENV === "production";
}

export type Session = {
  sub: string;
  account: string;
  displayName: string;
  role: string;
  /** 签发时间（epoch 秒）：与 User.passwordChangedAt 比对实现服务端撤销。 */
  iat: number;
  exp: number;
};

function sessionSecret(): string {
  const value = process.env.SESSION_SECRET;
  if (value && value.length >= 16) return value;
  if (process.env.NODE_ENV === "production") {
    throw new Error("SESSION_SECRET must be set to at least 16 characters in production");
  }
  return "keykeep-dev-session-secret-change-me";
}

function encode(input: string | Buffer): string {
  return Buffer.from(input).toString("base64url");
}

export function signSession(session: Session): string {
  const payload = encode(JSON.stringify(session));
  const signature = createHmac("sha256", sessionSecret()).update(payload).digest("base64url");
  return `${payload}.${signature}`;
}

export function verifySession(token: string, nowSeconds = Math.floor(Date.now() / 1000)): Session | null {
  const [payload, signature] = token.split(".");
  if (!payload || !signature) return null;
  const expected = createHmac("sha256", sessionSecret()).update(payload).digest();
  const given = Buffer.from(signature, "base64url");
  if (given.length !== expected.length || !timingSafeEqual(given, expected)) return null;
  try {
    const session = JSON.parse(Buffer.from(payload, "base64url").toString("utf8")) as Session;
    if (!session.sub || !session.account || !session.role || !session.exp) return null;
    // iat 缺失（旧版 cookie）按无效处理：升级后全员重新登录一次，换取服务端撤销能力。
    if (!Number.isInteger(session.iat) || session.iat <= 0) return null;
    if (session.exp <= nowSeconds) return null;
    return session;
  } catch {
    return null;
  }
}

export function newSession(
  user: {
    id: string;
    account: string;
    displayName: string;
    role: { name: string };
    /** 可选：本次登录时刻最近一次的改密时间（epoch 秒）。iat 会避开它。 */
    passwordChangedAt?: Date | null;
  },
  nowSeconds = Math.floor(Date.now() / 1000),
): Session {
  // 秒级粒度下的同秒竞态：改密后同秒重登，iat == changedAt 会被
  // sessionRevoked 判成撤销（登录 200 却处处 401）。iat 至少取
  // changedAt + 1，保证新会话严格晚于改密时刻。
  let iat = nowSeconds;
  if (user.passwordChangedAt) {
    const changedAt = Math.floor(user.passwordChangedAt.getTime() / 1000);
    if (iat <= changedAt) iat = changedAt + 1;
  }
  return {
    sub: user.id,
    account: user.account,
    displayName: user.displayName,
    role: user.role.name,
    iat,
    exp: iat + SESSION_TTL_SECONDS,
  };
}

/**
 * 服务端撤销判定：账号被停用，或口令在本会话签发之后被修改/重置，会话立即失效。
 * 之前的会话是纯无状态 cookie——停用账号、重置口令后旧 cookie 仍能用到过期，
 * 这是控制面安全模型的核心缺口。
 *
 * same-second 边界（FR-01 向导实测踩中两侧）：iat 是秒、passwordChangedAt 落库
 * 毫秒截到秒。`changedAt > iat` 会放行「登录后同秒改密」的旧会话（验收脚本/
 * 向导自动化的常态）；`changedAt >= iat` 又会杀掉「改密后同秒重登」的新会话
 * （同一秒里 iat == changedAt，登录 200 却处处 401）。秒级粒度下两种顺序
 * 无法区分，所以入口在这里：**签发会话时保证 iat 严格大于最近的改密秒**。
 * newSession 由登录路径独占调用，登录前不可能改自己的密——把 minIat 从
 * passwordChangedAt 传进来，newSession 对 iat 取 max(now, changedAt + 1)。
 */
export function sessionRevoked(
  session: Session,
  user: { disabled: boolean; passwordChangedAt: Date | null },
): boolean {
  if (user.disabled) return true;
  if (user.passwordChangedAt) {
    const changedAt = Math.floor(user.passwordChangedAt.getTime() / 1000);
    if (changedAt >= session.iat) return true;
  }
  return false;
}
