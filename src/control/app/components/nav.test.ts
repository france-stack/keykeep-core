import { describe, expect, it } from "vitest";

import {
  NAV_GROUPS,
  SETTINGS_ITEM,
  canSeeItem,
  isActive,
  landingHref,
  pageMeta,
  roleLabel,
  visibleGroups,
} from "./nav";

function ids(role: string): string[] {
  return visibleGroups(role).flatMap((group) => group.items.map((item) => item.id));
}

describe("nav", () => {
  // 员工只需要「我的接口 + 用量」：老板的凭据、成员、预算、审计一律不出现。
  it("employee sees My endpoint, Usage and Settings only", () => {
    expect(ids("employee")).toEqual(["my-endpoint", "usage"]);
    expect(canSeeItem("employee", SETTINGS_ITEM)).toBe(true);
  });

  it("owner sees every enabled management entry and no employee-only page", () => {
    expect(ids("admin")).toEqual([
      "keys",
      "distributions",
      "org",
      "providers",
      "catalog",
      "limits",
      "usage",
      "budgets",
      "audit",
      "redaction",
      "maintenance",
    ]);
    expect(ids("admin")).not.toContain("my-endpoint");
  });

  // 「总览」已经下线，首组也不再叫「工作台」：登录后直接是自己的入口。
  it("has no overview entry and no workbench group title", () => {
    const allIds = NAV_GROUPS.flatMap((group) => group.items.map((item) => item.id));
    expect(allIds).not.toContain("overview");
    expect(allIds).not.toContain("工作台");
    expect(NAV_GROUPS.map((group) => group.label)).not.toContain("工作台");
    expect(NAV_GROUPS.flatMap((group) => group.items.map((item) => item.href))).not.toContain("/");
  });

  it("labels the remaining groups in Chinese", () => {
    // 首组（按角色的入口）没有标题，其余三组照旧。
    const labels = NAV_GROUPS.map((group) => group.label);
    expect(labels).toEqual([undefined, "模型", "观测", "扩展"]);
    expect(labels.filter(Boolean)).toEqual(["模型", "观测", "扩展"]);
  });

  it("landingHref sends each role to its first visible page", () => {
    expect(landingHref("admin")).toBe("/admin/keys");
    expect(landingHref("employee")).toBe("/me");
  });

  it("roleLabel maps the two product roles", () => {
    expect(roleLabel("admin")).toBe("老板");
    expect(roleLabel("employee")).toBe("员工");
  });

  it("matches active routes including aliases", () => {
    const prices = pageMeta("/admin/prices");
    expect(prices.label).toBe("模型与价格");
    expect(pageMeta("/admin/usage").label).toBe("用量与请求");
    expect(pageMeta("/me").label).toBe("我的接口");
    expect(isActive("/admin/keys/abc", { id: "keys", label: "API 密钥", href: "/admin/keys", icon: "key" })).toBe(true);
  });

  // P6：员工页不再挂在 /admin 前缀下。canonical 是 /me/*，旧路径只作为 alias 存活，
  // 保证老书签进来时高亮与面包屑仍然对得上（它们本身会 307 到新路径）。
  it("points shared employee pages at /me instead of /admin", () => {
    const employeeUsage = visibleGroups("employee")
      .flatMap((group) => group.items)
      .find((item) => item.id === "usage");
    expect(employeeUsage?.href).toBe("/me/usage");
    expect(SETTINGS_ITEM.href).toBe("/me/settings");
    // 旧路径仍被识别为同一项，否则老书签会掉进「KeyKeep」兜底标题。
    expect(pageMeta("/admin/usage").label).toBe("用量与请求");
    expect(pageMeta("/admin/settings").label).toBe("设置");
    expect(pageMeta("/me/usage").label).toBe("用量与请求");
    expect(pageMeta("/me/settings").label).toBe("设置");
    // 迁地址不能改变登录落点。
    expect(landingHref("employee")).toBe("/me");
    expect(landingHref("admin")).toBe("/admin/keys");
  });

  // /me 现在是 /me/usage、/me/settings 的父路径，但它本身也是一张独立页面：
  // 前缀匹配会让子页面的面包屑全变成「我的接口」，侧栏也会同时点亮两项。
  it("does not let /me swallow its sibling subpages", () => {
    const myEndpoint = visibleGroups("employee")
      .flatMap((group) => group.items)
      .find((item) => item.id === "my-endpoint");
    expect(myEndpoint).toBeDefined();
    expect(isActive("/me", myEndpoint!)).toBe(true);
    expect(isActive("/me/usage", myEndpoint!)).toBe(false);
    expect(isActive("/me/settings", myEndpoint!)).toBe(false);
    expect(pageMeta("/me/usage").label).toBe("用量与请求");
  });

  // 面包屑不再凭空出现「控制台」这一层。
  it("never falls back to the console label", () => {
    expect(pageMeta("/admin/keys").group).toBeUndefined();
    expect(pageMeta("/admin/providers").group).toBe("模型");
    expect(pageMeta("/some/unknown").label).toBe("KeyKeep");
    expect(pageMeta("/some/unknown").label).not.toBe("控制台");
  });

  // FR-60：维护平面入口接通（此前是 disabled 占位）。
  // 页面本身仍受 KEYKEEP_MAINTENANCE_ENABLED 门控——导航可达 ≠ 维护已开启。
  it("exposes the maintenance entry to admins as a real page", () => {
    const item = visibleGroups("admin")
      .flatMap((group) => group.items)
      .find((entry) => entry.id === "maintenance");
    expect(item?.href).toBe("/admin/maintenance");
    expect(item?.disabled).toBeUndefined();
    // 员工永远看不到维护入口。
    expect(ids("employee")).not.toContain("maintenance");
  });

  it("item ids are unique", () => {
    const all = [...NAV_GROUPS.flatMap((group) => group.items.map((item) => item.id)), SETTINGS_ITEM.id];
    expect(new Set(all).size).toBe(all.length);
  });
});
