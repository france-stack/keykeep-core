import { NextResponse } from "next/server";

import { prisma } from "@/lib/db";
import { GENESIS_HASH } from "@/lib/ledger-hash";

export const runtime = "nodejs";
export const dynamic = "force-dynamic";

/**
 * 控制面健康检查（compose / 托盘 / 外部探针使用）。
 *
 * 与网关的 /healthz 不同，这里确实检查依赖，因为控制面"活着但用不了"的
 * 典型形态就是数据库迁移没跟上——那正好是升级事故里出现过的那一种。
 *
 * 鉴权与泄露面：不要求登录（探针不会带会话），也**不返回任何依赖细节**——
 * 只有 { status, checks } 的布尔摘要。200 全绿、503 有红项。错误原文一律
 * 不落到响应体里：连接串、主机名、凭据都在那些错误里。
 */
type Check = { ok: boolean; detail?: string };

async function checkDatabase(): Promise<Check> {
  try {
    await prisma.$queryRaw`SELECT 1`;
    return { ok: true };
  } catch {
    return { ok: false, detail: "database_unreachable" };
  }
}

/**
 * 迁移是否跟得上：_prisma_migrations 最近一行的 finished_at 非空，且没有失败记录。
 * 迁移未跑完时控制面会对着旧表结构报错，这是最值得在探针里提前暴露的一种。
 */
async function checkMigrations(): Promise<Check> {
  try {
    const rows = await prisma.$queryRaw<{ pending: bigint; failed: bigint }[]>`
      SELECT
        count(*) FILTER (WHERE "finished_at" IS NULL AND "rolled_back_at" IS NULL) AS pending,
        count(*) FILTER (WHERE "finished_at" IS NULL AND "logs" IS NOT NULL
                           AND "rolled_back_at" IS NULL) AS failed
        FROM "_prisma_migrations"`;
    const row = rows[0];
    const pending = Number(row?.pending ?? 0);
    const failed = Number(row?.failed ?? 0);
    if (failed > 0) return { ok: false, detail: "migration_failed" };
    if (pending > 0) return { ok: false, detail: "migration_pending" };
    return { ok: true };
  } catch {
    return { ok: false, detail: "migration_table_unreadable" };
  }
}

/**
 * 审计链起点是否存在：空表时读不到任何 rowHash 也算正常（刚装好还没事件），
 * 但"链已开始却在链尾之后又出现未链行"不在这里判断——那是 /admin/audit 的
 * 全量走链的职责，探针只做轻量检查。
 */
function checkAuditKey(): Check {
  const key = process.env.KEYKEEP_AUDIT_CHAIN_KEY?.trim();
  if (key && key.length >= 32) return { ok: true };
  if (key) return { ok: false, detail: "audit_chain_key_too_short" };
  // 没配不是故障，是降级：裸 sha256 仍能发现改行与删中间行。
  return { ok: true, detail: "audit_chain_key_absent_sha256_mode" };
}

export async function GET() {
  const [database, migrations] = await Promise.all([checkDatabase(), checkMigrations()]);
  const audit = checkAuditKey();
  const checks = { database, migrations, audit };

  const ok = database.ok && migrations.ok && audit.ok;
  return NextResponse.json(
    {
      status: ok ? "ok" : "degraded",
      checks: Object.fromEntries(Object.entries(checks).map(([name, check]) => [name, check.ok])),
      detail: Object.fromEntries(
        Object.entries(checks)
          .filter(([, check]) => check.detail)
          .map(([name, check]) => [name, check.detail]),
      ),
      chainGenesis: GENESIS_HASH.slice(0, 8),
    },
    { status: ok ? 200 : 503, headers: { "Cache-Control": "no-store" } },
  );
}
