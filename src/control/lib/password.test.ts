import { describe, expect, it } from "vitest";

import { hashPassword, validatePassword, verifyPassword } from "./password";

describe("password", () => {
  it("hashes with Argon2id and verifies", async () => {
    const hashValue = await hashPassword("correct horse battery staple");
    expect(hashValue.startsWith("$argon2id$")).toBe(true);
    expect(await verifyPassword(hashValue, "correct horse battery staple")).toBe(true);
    expect(await verifyPassword(hashValue, "wrong password")).toBe(false);
  });

  it("rejects invalid hashes without throwing", async () => {
    expect(await verifyPassword("not-a-hash", "x")).toBe(false);
  });

  it("enforces length policy", () => {
    expect(validatePassword("short")).toBe("password_too_short");
    expect(validatePassword("x".repeat(129))).toBe("password_too_long");
    expect(validatePassword("long-enough-password")).toBeNull();
  });
});
