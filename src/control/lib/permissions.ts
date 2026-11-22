export const ROLES = ["admin", "employee"] as const;
export type Role = (typeof ROLES)[number];

export function isRole(value: string): value is Role {
  return (ROLES as readonly string[]).includes(value);
}

/** 产品只有老板（admin）与员工（employee）两种角色。 */
export function canManageOrg(role: string): boolean {
  return role === "admin";
}

export function canIssueKeys(role: string): boolean {
  return role === "admin";
}

export function canViewAllUsage(role: string): boolean {
  return role === "admin";
}
