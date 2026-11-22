import type { Prisma } from "@prisma/client";

import type { Session } from "./session";

export type UsageRange = "today" | "month";

/** 本地时区的统计起点：今天的 0 点或本月 1 日 0 点。 */
export function rangeStart(range: UsageRange, now = new Date()): Date {
  if (range === "month") return new Date(now.getFullYear(), now.getMonth(), 1);
  return new Date(now.getFullYear(), now.getMonth(), now.getDate());
}

export function parseRange(value: string | null): UsageRange {
  return value === "month" ? "month" : "today";
}

/** 员工看自己，老板（admin）看全部。 */
export function usageScope(session: Session): Prisma.UsageRecordWhereInput {
  return session.role === "admin" ? {} : { empId: session.sub };
}

export function num(value: bigint | number | null | undefined): number {
  if (value === null || value === undefined) return 0;
  return typeof value === "bigint" ? Number(value) : value;
}
