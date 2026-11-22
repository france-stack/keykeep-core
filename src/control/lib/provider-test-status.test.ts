import { describe, expect, it } from "vitest";

import { describeTestStatus } from "./provider-test-status";

describe("describeTestStatus", () => {
  // F4：详情折叠时也必须能看到测试结论——状态位由这些分支决定。
  it("shows nothing before any test runs", () => {
    const status = describeTestStatus(undefined);
    expect(status.label).toBe("");
    expect(status.testing).toBe(false);
  });

  it("marks an in-flight test so the button can be disabled", () => {
    const status = describeTestStatus({ ok: false, status: null, latency_ms: 0, error: "testing" });
    expect(status.label).toBe("测试中…");
    expect(status.testing).toBe(true);
    expect(status.tone).toBe("limited");
  });

  it("reports success with latency", () => {
    const status = describeTestStatus({ ok: true, status: 200, latency_ms: 25.4 });
    expect(status.label).toBe("连接成功 25 ms");
    expect(status.tone).toBe("success");
    expect(status.testing).toBe(false);
  });

  it("reports failure with the error code in the tooltip", () => {
    const status = describeTestStatus({ ok: false, status: 401, latency_ms: 12, error: "invalid_api_key" });
    expect(status.label).toBe("连接失败");
    expect(status.tone).toBe("error");
    expect(status.title).toContain("invalid_api_key");
  });

  it("falls back to connection_failed when no error code is returned", () => {
    const status = describeTestStatus({ ok: false, status: null, latency_ms: 0 });
    expect(status.title).toContain("connection_failed");
  });

  it("never renders a fractional or negative latency", () => {
    expect(describeTestStatus({ ok: true, status: 200, latency_ms: -3 }).label).toBe("连接成功 0 ms");
    expect(describeTestStatus({ ok: true, status: 200, latency_ms: Number.NaN }).label).toBe("连接成功 0 ms");
  });
});
