import { describe, expect, it } from "vitest";

import {
  MAX_KEYS_PER_ASSIGN,
  MAX_MODELS,
  canAssign,
  clampAssignCount,
  describeModels,
  distributionStatus,
  normalizeAllowedModels,
  resolveAssignmentModels,
  validateDistributionName,
} from "./distribution";

describe("normalizeAllowedModels", () => {
  it("accepts arrays and comma/whitespace separated strings", () => {
    expect(normalizeAllowedModels(["a", "b"])).toEqual(["a", "b"]);
    expect(normalizeAllowedModels("a, b  c")).toEqual(["a", "b", "c"]);
    // 全角逗号也要能吃：中文输入法下老板打的就是这个。
    expect(normalizeAllowedModels("a，b")).toEqual(["a", "b"]);
  });

  it("splits inside array items so pasted lists still work", () => {
    expect(normalizeAllowedModels(["a,b", "c"])).toEqual(["a", "b", "c"]);
  });

  it("dedupes while keeping the first occurrence order", () => {
    expect(normalizeAllowedModels(["b", "a", "b"])).toEqual(["b", "a"]);
  });

  it("drops blanks and non-strings", () => {
    expect(normalizeAllowedModels("  ,  ")).toEqual([]);
    expect(normalizeAllowedModels([1, null, "a"])).toEqual(["a"]);
    expect(normalizeAllowedModels(undefined)).toEqual([]);
    expect(normalizeAllowedModels(42)).toEqual([]);
  });

  it("caps the list at MAX_MODELS", () => {
    const many = Array.from({ length: MAX_MODELS + 20 }, (_, index) => `m${index}`);
    expect(normalizeAllowedModels(many)).toHaveLength(MAX_MODELS);
  });
});

describe("resolveAssignmentModels", () => {
  const template = ["deepseek-chat", "deepseek-reasoner"];

  it("falls back to the distribution default when the body omits the field", () => {
    expect(resolveAssignmentModels({ empId: "u1" }, template)).toEqual(template);
  });

  it("treats an explicit empty array as 'no restriction', not as 'use the default'", () => {
    expect(resolveAssignmentModels({ allowedModels: [] }, template)).toEqual([]);
  });

  it("uses the per-assignment list when provided", () => {
    expect(resolveAssignmentModels({ allowedModels: "a,b" }, template)).toEqual(["a", "b"]);
  });

  it("does not mutate the distribution's template", () => {
    const out = resolveAssignmentModels({}, template);
    out.push("mutated");
    expect(template).toEqual(["deepseek-chat", "deepseek-reasoner"]);
  });
});

describe("validateDistributionName", () => {
  it("rejects blank and over-long names", () => {
    expect(validateDistributionName("   ")).toBe("invalid_name");
    expect(validateDistributionName("ok")).toBeNull();
    expect(validateDistributionName("x".repeat(65))).toBe("name_too_long");
  });
});

describe("distributionStatus", () => {
  it("prefers revoked over disabled because revoking is terminal", () => {
    expect(distributionStatus({ disabled: false, revokedAt: null })).toBe("active");
    expect(distributionStatus({ disabled: true, revokedAt: null })).toBe("disabled");
    expect(distributionStatus({ disabled: false, revokedAt: "2026-11-10T00:00:00Z" })).toBe("revoked");
    expect(distributionStatus({ disabled: true, revokedAt: "2026-11-10T00:00:00Z" })).toBe("revoked");
  });

  it("only lets active distributions be assigned", () => {
    expect(canAssign({ disabled: false, revokedAt: null })).toBe(true);
    expect(canAssign({ disabled: true, revokedAt: null })).toBe(false);
    expect(canAssign({ disabled: false, revokedAt: new Date() })).toBe(false);
  });
});

describe("clampAssignCount", () => {
  it("clamps to 1..MAX_KEYS_PER_ASSIGN and defaults bad input to 1", () => {
    expect(clampAssignCount(undefined)).toBe(1);
    expect(clampAssignCount(0)).toBe(1);
    expect(clampAssignCount(-5)).toBe(1);
    expect(clampAssignCount(3)).toBe(3);
    expect(clampAssignCount(999)).toBe(MAX_KEYS_PER_ASSIGN);
    expect(clampAssignCount("abc")).toBe(1);
  });
});

describe("describeModels", () => {
  it("renders an empty list as unrestricted, matching the gateway whitelist", () => {
    expect(describeModels([])).toBe("不限");
    expect(describeModels(["a", "b"])).toBe("a, b");
  });
});
