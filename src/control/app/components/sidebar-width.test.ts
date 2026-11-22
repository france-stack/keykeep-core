import { describe, expect, it } from "vitest";

import { SIDEBAR_DEFAULT, SIDEBAR_MAX, SIDEBAR_MIN, clampSidebarWidth, parseStoredWidth } from "./sidebar-width";

describe("sidebar width", () => {
  it("clamp 到 168–320", () => {
    expect(clampSidebarWidth(100)).toBe(SIDEBAR_MIN);
    expect(clampSidebarWidth(500)).toBe(SIDEBAR_MAX);
    expect(clampSidebarWidth(212)).toBe(212);
  });

  it("非法输入回退默认值", () => {
    expect(clampSidebarWidth(Number.NaN)).toBe(SIDEBAR_DEFAULT);
    expect(parseStoredWidth(null)).toBe(SIDEBAR_DEFAULT);
    expect(parseStoredWidth("oops")).toBe(SIDEBAR_DEFAULT);
  });

  it("解析本地存储值", () => {
    expect(parseStoredWidth("300")).toBe(300);
    expect(parseStoredWidth("50")).toBe(SIDEBAR_MIN);
  });
});
