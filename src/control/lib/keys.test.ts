import { describe, expect, it } from "vitest";

import { hashKey, isKeyFormat, issueKey } from "./keys";

describe("issueKey", () => {
  it("returns a one-time plaintext with kk_ prefix and 43-char secret", () => {
    const key = issueKey();
    expect(key.plaintext.startsWith("kk_")).toBe(true);
    expect(key.plaintext).toHaveLength(46);
    expect(isKeyFormat(key.plaintext)).toBe(true);
    expect(key.prefix4).toBe(key.plaintext.slice(0, 4));
    expect(key.fp6).toMatch(/^[0-9a-f]{6}$/);
  });

  it("hashes deterministically and never returns the plaintext as hash", () => {
    const key = issueKey();
    expect(hashKey(key.plaintext).equals(key.keyHash)).toBe(true);
    expect(key.keyHash).toHaveLength(32);
    expect(key.keyHash.toString("utf8")).not.toContain(key.plaintext);
  });

  it("generates distinct keys", () => {
    const values = new Set(Array.from({ length: 100 }, () => issueKey().plaintext));
    expect(values.size).toBe(100);
  });

  it("rejects malformed keys", () => {
    expect(isKeyFormat("kk_short")).toBe(false);
    expect(isKeyFormat("sk_" + "a".repeat(43))).toBe(false);
  });
});
