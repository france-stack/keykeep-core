import { afterEach, describe, expect, it } from "vitest";

import { checkCsrf } from "./csrf";

function headers(map: Record<string, string>) {
  const lower = new Map(Object.entries(map).map(([k, v]) => [k.toLowerCase(), v]));
  return { get: (name: string) => lower.get(name.toLowerCase()) ?? null };
}

afterEach(() => {
  delete process.env.KEYKEEP_ALLOWED_ORIGINS;
});

describe("checkCsrf", () => {
  it("skips safe methods", () => {
    expect(checkCsrf("GET", headers({ origin: "http://evil.example" }), "console.local")).toEqual({ ok: true });
  });

  it("allows same-origin writes", () => {
    expect(checkCsrf("POST", headers({ origin: "http://console.local" }), "console.local")).toEqual({ ok: true });
  });

  it("rejects forged and null origins", () => {
    expect(checkCsrf("POST", headers({ origin: "http://evil.example" }), "console.local")).toMatchObject({
      ok: false,
      reason: "origin_mismatch",
    });
    expect(checkCsrf("POST", headers({ origin: "null" }), "console.local")).toMatchObject({
      ok: false,
      reason: "null_origin",
    });
  });

  it("honours the allowed-origin list", () => {
    process.env.KEYKEEP_ALLOWED_ORIGINS = "https://console.example.com";
    expect(checkCsrf("POST", headers({ origin: "https://console.example.com" }), "127.0.0.1:3000").ok).toBe(true);
  });

  it("validates referer when origin is absent and allows CLI requests", () => {
    expect(checkCsrf("POST", headers({ referer: "http://evil.example/x" }), "console.local")).toMatchObject({
      ok: false,
      reason: "referer_mismatch",
    });
    expect(checkCsrf("POST", headers({}), "console.local")).toEqual({ ok: true });
  });
});
