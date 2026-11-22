// Windows 桌面版首装种子：角色 + 管理员 + 演示上游供应商 + 模型价格。
//
// 与 src/control/prisma/seed.mjs 的区别：
//   - 那个是开发/容器用的（走 Prisma Client），供应商要人工在控制台加；
//   - 这个随安装包分发，只用 pg 驱动（避免把 Prisma Client 的引擎一并塞进包），
//     并自动建一条指向内置 kkmock 的供应商与模型映射，装完即可演示。
//
// 幂等：重复执行不会重复建供应商（按 kid + keyFp6 + providerType 判重），
// 管理员口令只在首次创建时写入，之后不会覆盖。
//
// 运行方式（由 kkdesk 调用）：
//   DATABASE_URL=... KEYKEEP_KEK=... KEYKEEP_SEED_ADMIN_PASSWORD=... \
//   KEYKEEP_ADMIN_ACCOUNT=admin KEYKEEP_DEMO_UPSTREAM=http://127.0.0.1:19011 \
//   node seed-desktop.mjs
//
// 账号名从环境变量的来，和 settings.json 的 adminAccount、初始登录信息.txt
// 里打印的那个是同一个来源；不传才退回默认 admin。

import { createCipheriv, createHash, randomBytes, randomUUID } from "node:crypto";

import pg from "pg";

import { hashPassword } from "./password-params.mjs";
import { fatal } from "./fatal.mjs";

const { Client } = pg;

/**
 * 凭据加密，与 lib/crypto.ts 和网关 internal/providers 的格式严格一致：
 * AES-256-GCM，nonce 12 字节，ct = ciphertext || authTag（16 字节），
 * keyFp6 = sha256(明文) 的十六进制末 6 位。三处实现必须同步修改。
 */
function encryptSecret(plaintext, kek) {
  const nonce = randomBytes(12);
  const cipher = createCipheriv("aes-256-gcm", kek, nonce);
  const body = Buffer.concat([cipher.update(plaintext, "utf8"), cipher.final()]);
  return {
    ct: Buffer.concat([body, cipher.getAuthTag()]),
    nonce,
    kid: "v1",
    keyFp6: createHash("sha256").update(plaintext, "utf8").digest("hex").slice(-6),
  };
}

function loadKek(raw) {
  if (!raw) throw new Error("KEYKEEP_KEK is required (base64 of 32 bytes)");
  const kek = Buffer.from(raw, "base64");
  if (kek.length !== 32) throw new Error("KEYKEEP_KEK must decode to exactly 32 bytes");
  return kek;
}

async function ensureUser(client, { account, displayName, roleName, password }) {
  const role = await client.query(`SELECT "id" FROM "Role" WHERE name = $1`, [roleName]);
  if (role.rowCount === 0) throw new Error(`角色不存在：${roleName}`);
  const roleId = role.rows[0].id;
  const passwordHash = await hashPassword(password);

  const existing = await client.query(`SELECT "id" FROM "User" WHERE account = $1`, [account]);
  if (existing.rowCount > 0) {
    // 已存在的账号只更新展示信息与启用状态，不动口令。
    await client.query(
      `UPDATE "User" SET "displayName" = $1, "roleId" = $2, "deptId" = NULL, disabled = false WHERE account = $3`,
      [displayName, roleId, account],
    );
    return existing.rows[0].id;
  }
  const id = randomUUID();
  // Prisma 的 @updatedAt 由 ORM 填；这里是原生 SQL，必须显式给值，
  // 否则 "updatedAt" 的非空约束会直接拒绝插入。
  await client.query(
    `INSERT INTO "User" ("id","account","passwordHash","displayName","deptId","roleId","passwordChangedAt","updatedAt")
     VALUES ($1,$2,$3,$4,NULL,$5,now(),now())`,
    [id, account, passwordHash, displayName, roleId],
  );
  return id;
}

/** 演示供应商：只在内置 kkmock 地址存在时创建。 */
async function ensureDemoProvider(client, { upstream, model, kek }) {
  if (!upstream) return null;

  // kkmock 不校验 Key，但凭据表要求非空且加密存储；用固定占位值便于排障时辨认。
  const demoKey = "mock-key-not-a-real-credential";
  const encrypted = encryptSecret(demoKey, kek);

  const existing = await client.query(
    `SELECT "id" FROM "ProviderCredential"
      WHERE provider = 'mock' AND kid = $1 AND "keyFp6" = $2 AND "providerType" = 'anthropic'`,
    [encrypted.kid, encrypted.keyFp6],
  );
  if (existing.rowCount > 0) {
    const id = existing.rows[0].id;
    // 地址可能因为换端口而变，每次对齐一遍。
    await client.query(`UPDATE "ProviderCredential" SET "baseUrl" = $1, disabled = false WHERE "id" = $2`,
      [upstream, id]);
    return id;
  }

  const id = randomUUID();
  await client.query(
    `INSERT INTO "ProviderCredential"
       ("id","provider","name","providerType","baseUrl","authHeaderName","authScheme","kid","keyFp6","ct","nonce","updatedAt")
     VALUES ($1,'mock','内置演示上游','anthropic',$2,'Authorization','Bearer',$3,$4,$5,$6,now())`,
    [id, upstream, encrypted.kid, encrypted.keyFp6, encrypted.ct, encrypted.nonce],
  );
  await client.query(
    `INSERT INTO "ModelMap" ("id","alias","provider","modelId","credentialId")
     VALUES ($1,'demo','mock',$2,$3)`,
    [randomUUID(), model, id],
  );
  return id;
}

/** 价格单位是「分 / 100 万 token」，与 ModelCatalog 口径一致。 */
async function ensurePricing(client, model) {
  await client.query(
    `INSERT INTO "ModelCatalog"
       ("provider","modelId","displayName","contextLength","maxOutputTokens","pricingPromptCents","pricingCompletionCents")
     VALUES ('mock',$1,'演示模型（内置 Mock）',200000,8192,300,1500)
     ON CONFLICT (provider, "modelId") DO UPDATE
       SET "pricingPromptCents" = 300, "pricingCompletionCents" = 1500,
           "displayName" = '演示模型（内置 Mock）'`,
    [model],
  );
}

async function main() {
  const connectionString = process.env.DATABASE_URL;
  if (!connectionString) {
    console.error("DATABASE_URL is required");
    process.exit(1);
  }
  const adminSecret = process.env.KEYKEEP_SEED_ADMIN_PASSWORD;
  if (!adminSecret) {
    console.error("KEYKEEP_SEED_ADMIN_PASSWORD is required");
    process.exit(1);
  }
  const demoUpstream = process.env.KEYKEEP_DEMO_UPSTREAM ?? "";
  const demoModel = process.env.KEYKEEP_DEMO_MODEL ?? "claude-3-5-sonnet-20241022";
  // 账号名由 kkdesk 从 settings.json 传进来。写死 "admin" 会和「初始登录信息.txt」
  // 里打印的 adminAccount 分叉：用户改了配置就变成「文件里的账号登不进去」，
  // 而口令找回脚本同样按配置里的账号找——三处必须同一个来源。
  const adminAccount = process.env.KEYKEEP_ADMIN_ACCOUNT || "admin";
  // 演示员工：桌面首装只有管理员，而「签发 Key」的目标必须是员工账号。
  // 不建这个账号，首次向导的第 3 步下拉框永远是空的、签发按钮永远灰着——
  // 用户既签不出 Key，也走不到「完成」。这个账号只用于演示与自测，
  // 口令与管理员同源（同样只在首装时写入一次）。
  const demoEmployeeAccount = process.env.KEYKEEP_SEED_EMPLOYEE_ACCOUNT || "demo_employee";
  const kek = loadKek(process.env.KEYKEEP_KEK);

  const client = new Client({ connectionString });
  await client.connect();
  try {
    await client.query("BEGIN");
    const roleInsert = await client.query(
      `INSERT INTO "Role" ("id","name") SELECT $1, $2
        WHERE NOT EXISTS (SELECT 1 FROM "Role" WHERE name = $2)`,
      [randomUUID(), "admin"],
    );
    void roleInsert;
    await client.query(
      `INSERT INTO "Role" ("id","name") SELECT $1, $2
        WHERE NOT EXISTS (SELECT 1 FROM "Role" WHERE name = $2)`,
      [randomUUID(), "employee"],
    );

    const adminId = await ensureUser(client, {
      account: adminAccount,
      displayName: "管理员",
      roleName: "admin",
      password: adminSecret,
    });
    // 演示员工：首次向导第 3 步可选的签发对象。只建在建号阶段，
    // 已存在时 ensureUser 不会覆盖其口令。
    const demoEmployeeId = await ensureUser(client, {
      account: demoEmployeeAccount,
      displayName: "演示员工",
      roleName: "employee",
      password: adminSecret,
    });
    const providerId = await ensureDemoProvider(client, { upstream: demoUpstream, model: demoModel, kek });
    await ensurePricing(client, demoModel);
    await client.query("COMMIT");

    console.log(JSON.stringify({
      admin: adminId,
      demoEmployee: demoEmployeeId,
      demoProvider: providerId,
      demoModel,
      upstream: demoUpstream || null,
    }));
  } catch (error) {
    await client.query("ROLLBACK").catch(() => {});
    throw error;
  } finally {
    await client.end();
  }
}

main().catch(fatal);
