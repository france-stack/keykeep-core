// 桌面版管理员口令找回：原地重置口令并解除登录锁定。
//
// 为什么需要它：初始口令的唯一载体是桌面上的「初始登录信息.txt」，而说明书要求
// 用户改完口令后把它删掉——删掉之后就再没有任何地方能查到口令。此前唯一的
// 「找回」手段是删 data\ 重装，等于用丢失全部业务数据来换一次登录。
//
// 信任边界：能执行这个脚本的人本来就能读到 settings.json（里面有 KEK 与数据库口令），
// 也能停掉整个服务——绕过登录界面并不能带来任何新权限。所以校验「谁在跑」没有意义，
// 有意义的是把动作做小：只改这一个账号的口令与锁定计数，不动角色、不动数据。
//
// 口令走环境变量而不是命令行参数：Windows 上命令行参数会出现在进程列表里
// （任务管理器、wmic process、ETW），任何本机用户都能瞄一眼；环境变量只对
// 同一用户（和管理员）可见。--enable 这类非敏感开关才用参数。
//
// 用法（由 kkdesk reset-admin 调用，也可手工执行）：
//   DATABASE_URL=... KEYKEEP_ADMIN_ACCOUNT=admin \
//   KEYKEEP_RESET_ADMIN_PASSWORD=... node reset-admin.mjs [--enable]
//
// 输出：stdout 一行 JSON（结果摘要，绝不含口令本身）；失败时 stderr 给原因、退码 1。

import pg from "pg";

import { hashPassword, passwordPolicyError } from "./password-params.mjs";
import { fatal } from "./fatal.mjs";

const { Client } = pg;

async function main() {
  const connectionString = process.env.DATABASE_URL;
  if (!connectionString) {
    console.error("DATABASE_URL is required");
    process.exit(1);
  }
  const account = process.env.KEYKEEP_ADMIN_ACCOUNT || "admin";
  const password = process.env.KEYKEEP_RESET_ADMIN_PASSWORD ?? "";

  // 口令策略与登录接口同源：否则会出现「脚本改得进去、控制面登不上」的口令，
  // 用户只会以为找回失败并反复重试。
  const policyError = passwordPolicyError(password);
  if (policyError) {
    console.error(`新口令不符合策略：${policyError}`);
    process.exit(1);
  }

  const enable = process.argv.includes("--enable");
  const client = new Client({ connectionString });
  await client.connect();
  try {
    const found = await client.query(
      `SELECT "id", disabled, "failedAttempts" FROM "User" WHERE account = $1`,
      [account],
    );
    if (found.rowCount === 0) {
      // 不猜、不建：账号名以 settings.json 的 adminAccount 为准，建号是首装种子的事。
      // 在这里顺手创建一个同名管理员，只会把「配置写错了」伪装成「找回成功」。
      throw new Error(`账号不存在：${account}（请核对 settings.json 的 adminAccount）`);
    }
    const wasDisabled = found.rows[0].disabled === true;
    const disabledAfter = enable ? false : wasDisabled;
    const passwordHash = await hashPassword(password);
    // passwordChangedAt 必须一起更新：控制面按它和会话签发时间比对来撤销旧会话，
    // 不写这一列就等于「换了口令、旧 Cookie 照样能用」。
    await client.query(
      `UPDATE "User"
          SET "passwordHash" = $1,
              "passwordChangedAt" = now(),
              "failedAttempts" = 0,
              "lockedUntil" = NULL,
              disabled = $2,
              "updatedAt" = now()
        WHERE account = $3`,
      [passwordHash, disabledAfter, account],
    );

    console.log(
      JSON.stringify({
        account,
        reset: true,
        wasDisabled,
        disabled: disabledAfter,
        sessionsRevoked: true,
      }),
    );
    if (disabledAfter) {
      console.error(
        `注意：账号 ${account} 处于停用状态，口令已重置但仍无法登录；再加 --enable 参数可一并启用。`,
      );
    }
  } finally {
    await client.end();
  }
}

main().catch(fatal);
