import { describe, expect, it } from "vitest";

import { ROLES, canIssueKeys, canManageOrg, canViewAllUsage, isRole } from "./permissions";

describe("permissions", () => {
  it("only owner and employee roles exist", () => {
    expect([...ROLES]).toEqual(["admin", "employee"]);
    expect(isRole("admin")).toBe(true);
    expect(isRole("employee")).toBe(true);
    expect(isRole("dept_admin")).toBe(false);
  });

  it("only the owner manages people", () => {
    expect(canManageOrg("admin")).toBe(true);
    expect(canManageOrg("employee")).toBe(false);
  });

  it("only the owner issues keys", () => {
    expect(canIssueKeys("admin")).toBe(true);
    expect(canIssueKeys("employee")).toBe(false);
  });

  it("only the owner sees all usage", () => {
    expect(canViewAllUsage("admin")).toBe(true);
    expect(canViewAllUsage("employee")).toBe(false);
  });
});
