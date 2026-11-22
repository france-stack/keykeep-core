// 验收测试固定账号与夹具（仅开发/验收环境使用）。
// 与 seed.mjs 一样只从环境变量取口令；本脚本写入已知口令是为了让自动化验收可登录，
// 生产环境不得运行本脚本。运行方式（src/control 目录下）：
//   DATABASE_URL=... node prisma/seed-acceptance.mjs
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

// 口令只能来自环境变量（由 scripts/acceptance/run.sh 每次运行随机生成），
// 仓库内不保存任何默认口令。
const adminPwd = process.env.KEYKEEP_ACCEPTANCE_ADMIN_PASSWORD;
const empPwd = process.env.KEYKEEP_ACCEPTANCE_EMP_PASSWORD;
if (!adminPwd || !empPwd) {
  console.error("KEYKEEP_ACCEPTANCE_ADMIN_PASSWORD / _EMP_PASSWORD 必须设置（run.sh 会自动生成）");
  process.exit(1);
}

async function ensureUser({ account, displayName, roleName, password }) {
  const role = await prisma.role.findUniqueOrThrow({ where: { name: roleName } });
  const passwordHash = await hash(password);
  return prisma.user.upsert({
    where: { account },
    update: {
      displayName,
      roleId: role.id,
      deptId: null,
      passwordHash,
      passwordChangedAt: new Date(),
      disabled: false,
      failedAttempts: 0,
      lockedUntil: null,
    },
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
    password: adminPwd,
  });
  const employee = await ensureUser({
    account: "acc_emp",
    displayName: "Acceptance Employee",
    roleName: "employee",
    password: empPwd,
  });

  // 上次验收遗留的供应商用旧 KEK 加密，网关启动时会解密失败；
  // 先全部停用，由验收脚本用本次 KEK 重新创建。
  const disabled = await prisma.providerCredential.updateMany({
    where: { disabled: false },
    data: { disabled: true },
  });

  // 计价夹具：分值/100 万 token。mock 上游每次返回 12 输入 / 5 输出 → 1 分。
  await prisma.modelCatalog.upsert({
    where: { provider_modelId: { provider: "anthropic", modelId: "claude-3-5-sonnet-20241022" } },
    update: { pricingPromptCents: 300n, pricingCompletionCents: 1500n },
    create: {
      provider: "anthropic",
      modelId: "claude-3-5-sonnet-20241022",
      displayName: "Claude 3.5 Sonnet (fixture)",
      pricingPromptCents: 300n,
      pricingCompletionCents: 1500n,
    },
  });

  console.log(
    JSON.stringify({
      admin: admin.account,
      employee: employee.account,
      disabledProviders: disabled.count,
    }),
  );
}

main()
  .catch((error) => {
    console.error(error);
    process.exitCode = 1;
  })
  .finally(async () => {
    await prisma.$disconnect();
  });
