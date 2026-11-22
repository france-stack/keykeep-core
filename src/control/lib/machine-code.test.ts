import { describe, expect, it } from "vitest";

import { machineCodeFingerprint } from "./machine-code";

describe("machineCodeFingerprint", () => {
  it("renders a 16-char hex prefix — never raw bytes (raw bytes corrupt JSON/evidence)", () => {
    const fp = machineCodeFingerprint("a".repeat(64));
    expect(fp.startsWith("sha256:")).toBe(true);
    expect(fp.slice("sha256:".length)).toMatch(/^[0-9a-f]{16}$/);
    expect(fp).not.toMatch(/[\uFFFD\u0000-\u0008\u000b\u000c\u000e-\u001f]/);
  });

  it("returns an empty string for an empty machine code", () => {
    expect(machineCodeFingerprint("")).toBe("");
  });
});
