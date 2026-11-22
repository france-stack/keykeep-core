// 开发/首装种子：老板 + 员工两个角色（无部门）。
// 密码只从环境变量读取；默认值仅用于本地开发，生产必须覆盖。
import { randomBytes } from "node:crypto";

import { PrismaPg } from "@prisma/adapter-pg";
import { PrismaClient } from "@prisma/client";

import { withUtcSession } from "../lib/db-session.mjs";
import { hash } from "@node-rs/argon2";

const connectionString = process.env.DATABASE_URL;
if (!connectionString) {
  console.error("DATABASE_URL is required");
  process.exit(1);
}
// 会话时区固定 UTC：否则 timestamptz 写入会被减 8 小时（见 lib/db-session.ts）。
const prisma = new PrismaClient({ adapter: new PrismaPg({ connectionString: withUtcSession(connectionString) }) });

const generated = !process.env.KEYKEEP_SEED_ADMIN_PASSWORD;
const adminSecret =
  process.env.KEYKEEP_SEED_ADMIN_PASSWORD ?? randomBytes(12).toString("base64url");
const userSecret =
  process.env.KEYKEEP_SEED_USER_PASSWORD ?? randomBytes(12).toString("base64url");

async function ensureUser({ account, displayName, roleName, password }) {
  const role = await prisma.role.findUniqueOrThrow({ where: { name: roleName } });
  const passwordHash = await hash(password);
  return prisma.user.upsert({
    where: { account },
    update: { displayName, roleId: role.id, deptId: null, disabled: false },
    create: {
      account,
      displayName,
      roleId: role.id,
      deptId: null,
      passwordHash,
      passwordChangedAt: new Date(),
    },
  });
}

async function main() {
  for (const name of ["admin", "employee"]) {
    await prisma.role.upsert({ where: { name }, update: {}, create: { name } });
  }

  const admin = await ensureUser({
    account: "admin",
    displayName: "Admin",
    roleName: "admin",
    password: adminSecret,
  });
  const employee = await ensureUser({
    account: "linxiao",
    displayName: "Lin Xiao",
    roleName: "employee",
    password: userSecret,
  });

  console.log(`seeded admin=${admin.account} employee=${employee.account}`);
  if (generated) {
    // 生成的口令只在交互式终端上展示一次：容器/CI 日志会永久留存，
    // 非交互环境打印明文口令等于把初始凭据写进日志。
    if (process.stdout.isTTY) {
      console.warn(`generated admin secret: ${adminSecret}`);
      console.warn(`generated employee secret: ${userSecret}`);
    } else {
      console.warn("seed generated random secrets; re-run with KEYKEEP_SEED_ADMIN_PASSWORD to set your own");
    }
  }
}

main()
  .catch((error) => {
    console.error(error);
    process.exitCode = 1;
  })
  .finally(async () => {
    await prisma.$disconnect();
  });
