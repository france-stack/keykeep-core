import { PrismaPg } from "@prisma/adapter-pg";
import { PrismaClient } from "@prisma/client";

import { withUtcSession } from "./db-session";

// 会话时区必须是 UTC，理由见 lib/db-session.ts 顶部（写入被减 8 小时、
// 读取被加 8 小时，两次偏移在控制面内部互相抵消，只坑网关写入的行）。
const connectionString = process.env.DATABASE_URL;
const sessionUrl = connectionString ? withUtcSession(connectionString) : undefined;
const adapter = sessionUrl ? new PrismaPg({ connectionString: sessionUrl }) : undefined;

const globalForPrisma = globalThis as unknown as { prisma?: PrismaClient };

export const prisma =
  globalForPrisma.prisma ?? new PrismaClient(adapter ? { adapter } : undefined);

if (process.env.NODE_ENV !== "production") {
  globalForPrisma.prisma = prisma;
}
