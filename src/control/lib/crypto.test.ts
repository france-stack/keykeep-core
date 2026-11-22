import { describe, expect, it } from "vitest";

import { decryptSecret, encryptSecret, fingerprint, loadKek } from "./crypto";

const KEK = Buffer.alloc(32, 7);

describe("crypto", () => {
  it("round-trips AES-256-GCM ciphertext", () => {
    const plaintext = "upstream-value-123456";
    const encrypted = encryptSecret(plaintext, KEK);
    expect(encrypted.ct.length).toBeGreaterThan(plaintext.length);
    expect(encrypted.nonce).toHaveLength(12);
    expect(encrypted.kid).toBe("v1");
    expect(decryptSecret(encrypted.ct, encrypted.nonce, KEK)).toBe(plaintext);
  });

  it("fails with a different KEK", () => {
    const encrypted = encryptSecret("secret", KEK);
    expect(() => decryptSecret(encrypted.ct, encrypted.nonce, Buffer.alloc(32, 8))).toThrow();
  });

  it("uses a random nonce each time", () => {
    const a = encryptSecret("same", KEK);
    const b = encryptSecret("same", KEK);
    expect(a.nonce.equals(b.nonce)).toBe(false);
    expect(a.ct.equals(b.ct)).toBe(false);
  });

  it("fingerprints the last 6 hex chars", () => {
    expect(fingerprint("abc")).toMatch(/^[0-9a-f]{6}$/);
  });

  it("validates KEK length", () => {
    expect(loadKek(KEK.toString("base64")).length).toBe(32);
    expect(() => loadKek(Buffer.alloc(16).toString("base64"))).toThrow();
    expect(() => loadKek("")).toThrow();
  });
});
