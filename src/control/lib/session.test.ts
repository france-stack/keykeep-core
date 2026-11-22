import { beforeEach, describe, expect, it } from "vitest";

import { sessionRevoked, signSession, verifySession, type Session } from "./session";

const SESSION: Session = {
  sub: "user-1",
  account: "admin",
  displayName: "管理员",
  role: "admin",
  iat: Math.floor(Date.now() / 1000),
  exp: Math.floor(Date.now() / 1000) + 3600,
};

describe("session", () => {
  beforeEach(() => {
    process.env.SESSION_SECRET = "test-session-secret-0123456789";
  });

  it("round-trips a signed session", () => {
    const token = signSession(SESSION);
    expect(verifySession(token)).toEqual(SESSION);
  });

  it("rejects a tampered payload", () => {
    const token = signSession(SESSION);
    const [payload, signature] = token.split(".");
    const tampered = Buffer.from(
      JSON.stringify({ ...SESSION, role: "employee" }),
      "utf8",
    ).toString("base64url");
    expect(verifySession(`${tampered}.${signature}`)).toBeNull();
    expect(payload).not.toBe(tampered);
  });

  it("rejects a tampered signature", () => {
    const token = signSession(SESSION);
    const [payload, signature] = token.split(".");
    // 32 字节签名编成 base64url 是 43 字符（43×6=258 位，末字符只有 2 位有效、4 位丢弃），
    // 改末字符可能解出完全相同的字节；verifySession 比的是解码后的字节，
    // 于是「篡改末位、以 A/B 互为替补」的写法约 2/64 概率篡改无效、用例假失败。
    // 改首位：6 位全有效，任何替换都必然改变签名。下面同时把这条前提钉住，
    // 今后再出现「无效篡改」会用例自己报错，而不是随机红。
    const first = signature[0] === "A" ? "B" : "A";
    const mutated = `${first}${signature.slice(1)}`;
    expect(Buffer.from(mutated, "base64url")).not.toEqual(Buffer.from(signature, "base64url"));
    expect(verifySession(`${payload}.${mutated}`)).toBeNull();
  });

  it("rejects a signature of the wrong length", () => {
    const token = signSession(SESSION);
    const [payload] = token.split(".");
    expect(verifySession(`${payload}.${"A".repeat(20)}`)).toBeNull();
  });

  it("rejects expired sessions", () => {
    const token = signSession(SESSION);
    expect(verifySession(token, SESSION.exp + 1)).toBeNull();
  });

  it("rejects sessions without iat (pre-revocation cookies)", () => {
    const { iat: _iat, ...legacy } = SESSION;
    const token = signSession(legacy as Session);
    expect(verifySession(token)).toBeNull();
  });

  it("revokes when the user is disabled", () => {
    expect(sessionRevoked(SESSION, { disabled: true, passwordChangedAt: null })).toBe(true);
    expect(sessionRevoked(SESSION, { disabled: false, passwordChangedAt: null })).toBe(false);
  });

  it("revokes sessions issued before a password change", () => {
    const changedAfter = new Date((SESSION.iat + 10) * 1000);
    const changedBefore = new Date((SESSION.iat - 10) * 1000);
    expect(sessionRevoked(SESSION, { disabled: false, passwordChangedAt: changedAfter })).toBe(true);
    expect(sessionRevoked(SESSION, { disabled: false, passwordChangedAt: changedBefore })).toBe(false);
  });

  // FR-01 向导实测：登录后 1 秒内改密（验收自动化的常态），changedAt 截秒后
  // 等于 iat。改密让「不晚于改密时刻签发」的会话全部失效，等号必须算撤销。
  it("revokes sessions issued in the same second as the password change", () => {
    const sameSecond = new Date(SESSION.iat * 1000);
    expect(sessionRevoked(SESSION, { disabled: false, passwordChangedAt: sameSecond })).toBe(true);
  });

  it("rejects malformed tokens", () => {
    expect(verifySession("")).toBeNull();
    expect(verifySession("onlypayload")).toBeNull();
  });
});
