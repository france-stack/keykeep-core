import { describe, expect, it } from "vitest";

import { AUDIT_ACTIONS, actionLabel, auditRequestId, normalizeActionFilter } from "./audit";

describe("audit helpers", () => {
  it("labels every whitelisted action", () => {
    for (const action of AUDIT_ACTIONS) {
      expect(actionLabel(action)).not.toBe(action);
    }
    expect(actionLabel("custom.event")).toBe("custom.event");
  });

  it("accepts only whitelisted filters", () => {
    expect(normalizeActionFilter("auth.login")).toBe("auth.login");
    expect(normalizeActionFilter(" key.issue ")).toBe("key.issue");
    expect(normalizeActionFilter("")).toBeUndefined();
    expect(normalizeActionFilter(null)).toBeUndefined();
    expect(normalizeActionFilter("auth.*")).toBeUndefined();
    expect(normalizeActionFilter("' OR 1=1 --")).toBeUndefined();
  });

  it("truncates request id header", () => {
    expect(auditRequestId({ headers: new Headers({ "x-request-id": "abc" }) })).toBe("abc");
    expect(auditRequestId({ headers: new Headers() })).toBeNull();
    expect(auditRequestId({ headers: new Headers({ "x-request-id": "x".repeat(200) }) })).toHaveLength(128);
  });
});
