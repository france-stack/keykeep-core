import type { Prisma } from "@prisma/client";

import { AUDIT_ACTION_LABELS, AUDIT_ACTION_VALUES, isAuditAction } from "./audit-actions";
import {
  AUDIT_CHAIN_LOCK,
  auditChainHash,
  hashAlgoFor,
  createChainVerifier,
  type ChainReport,
  type StoredAuditChainRow,
} from "./audit-chain";
import { prisma } from "./db";
import { GENESIS_HASH } from "./ledger-hash";

// 审计写入是旁路：失败只记控制台，绝不改变主流程结果。
export type AuditInput = {
  actorId?: string | null;
  action: string;
  detail?: Record<string, unknown>;
  requestId?: string | null;
};

// 动作集合与标签只在 lib/audit-actions.ts 登记一次，这里派生。
export const AUDIT_ACTIONS = AUDIT_ACTION_VALUES;

export function actionLabel(action: string): string {
  return AUDIT_ACTION_LABELS[action] ?? action;
}

/** 只接受白名单动作与已知前缀，避免任意字符串进入筛选条件。 */
export function normalizeActionFilter(value: string | null): string | undefined {
  const raw = (value ?? "").trim();
  if (!raw) return undefined;
  return isAuditAction(raw) ? raw : undefined;
}

/**
 * 链密钥。没配就用裸 sha256（链仍能发现改行/删中间行），配了则用 HMAC，
 * 只持有数据库写权限的人也续不动尾巴——代价是密钥本身要进备份与轮换流程。
 */
function chainKey(): string | null {
  return process.env.KEYKEEP_AUDIT_CHAIN_KEY?.trim() || null;
}

let writeFailures = 0;
const recentFailures: { at: string; action: string; reason: string }[] = [];
const RECENT_FAILURES_MAX = 20;

// 审计是旁路，所以"写失败"不能变成业务失败；但也不能只剩一行 console.error——
// 那等于合规声明里最硬的一句话（"一切都有据可查"）悄悄失效而无人知晓。
function noteWriteFailure(action: string, error: unknown): void {
  writeFailures++;
  const reason = error instanceof Error ? `${error.name}: ${error.message}` : String(error);
  recentFailures.unshift({ at: new Date().toISOString(), action, reason: reason.slice(0, 400) });
  if (recentFailures.length > RECENT_FAILURES_MAX) recentFailures.length = RECENT_FAILURES_MAX;
  console.error(`audit_write_failed action=${action} reason=${reason}`);
}

export function auditWriteHealth(): { failures: number; recent: { at: string; action: string; reason: string }[] } {
  return { failures: writeFailures, recent: recentFailures.map((item) => ({ ...item })) };
}

export async function recordAudit(input: AuditInput): Promise<void> {
  const detail = (input.detail ?? {}) as Prisma.InputJsonValue;
  try {
    await prisma.$transaction(async (tx) => {
      // 链头必须在同一事务里锁住：两个并发写各自读到同一个 head，就会分叉出两条
      // prevHash 相同的链，而事后看不出"哪条才是真史"。锁号与网关账本/幂等守卫同族。
      await tx.$executeRaw`SELECT pg_advisory_xact_lock(${AUDIT_CHAIN_LOCK}::bigint)`;
      const head = await tx.auditEvent.findFirst({
        where: { rowHash: { not: null } },
        orderBy: { seq: "desc" },
        select: { rowHash: true },
      });
      const prevHash = head?.rowHash ?? GENESIS_HASH;
      const created = await tx.auditEvent.create({
        data: {
          actorId: input.actorId ?? null,
          action: input.action,
          detail,
          requestId: input.requestId ?? null,
        },
        select: { id: true },
      });
      // 摘要只认库里的值：JSONB 归一化键序与数字（1.0→1、1e3→1000），
      // 拿写入前的 JS 值算，校验时永远复算不出来。
      const stored = await tx.auditEvent.findUnique({
        where: { id: created.id },
        select: { seq: true, at: true, actorId: true, action: true, detail: true, requestId: true },
      });
      if (!stored) throw new Error("audit: 写入后回读不到该行");
      const algo = hashAlgoFor(chainKey());
      const rowHash = auditChainHash(prevHash, stored, algo, chainKey());
      await tx.auditEvent.update({
        where: { id: created.id },
        data: { prevHash, rowHash, hashAlgo: algo },
      });
    });
  } catch (error) {
    noteWriteFailure(input.action, error);
  }
}

const CHAIN_PAGE_SIZE = 2_000;
const CHAIN_MAX_ROWS = 500_000;

/**
 * 全量走链（分页读，不把整张表拉进内存）。
 * 返回值 stoppedEarly 为真时表示"结论不完整"：或因缺密钥早停，或因撞上行数上限。
 */
export async function verifyStoredAuditChain(): Promise<ChainReport> {
  const verifier = createChainVerifier({ key: chainKey() });
  let after = 0n;
  let scanned = 0;
  let capped = false;
  for (;;) {
    const take = Math.min(CHAIN_PAGE_SIZE, CHAIN_MAX_ROWS - scanned);
    if (take <= 0) {
      capped = true;
      break;
    }
    const rows = await prisma.auditEvent.findMany({
      where: { seq: { gt: after } },
      orderBy: { seq: "asc" },
      take,
      select: {
        seq: true,
        at: true,
        actorId: true,
        action: true,
        detail: true,
        requestId: true,
        prevHash: true,
        rowHash: true,
        hashAlgo: true,
      },
    });
    if (rows.length === 0) break;
    for (const row of rows) {
      if (!verifier.feed(row as StoredAuditChainRow)) return verifier.report();
    }
    scanned += rows.length;
    after = rows[rows.length - 1].seq;
    if (rows.length < take) break;
  }
  const report = verifier.report();
  if (capped) report.stoppedEarly = true;
  return report;
}

/**
 * 链头锚点：运维应当定期把它抄进链外的介质（工单/邮件/日志归档）。
 * 只有拿到链外的锚点，"删掉尾巴再重算"才从看不出来变成可核对。
 */
export async function auditChainHead(): Promise<{
  headSeq: string | null;
  headHash: string | null;
  algo: string | null;
  chainedRows: number;
  unchainedRows: number;
  keyConfigured: boolean;
}> {
  const [head, chained, total] = await Promise.all([
    prisma.auditEvent.findFirst({
      where: { rowHash: { not: null } },
      orderBy: { seq: "desc" },
      select: { seq: true, rowHash: true, hashAlgo: true },
    }),
    prisma.auditEvent.count({ where: { rowHash: { not: null } } }),
    prisma.auditEvent.count(),
  ]);
  return {
    headSeq: head?.seq.toString() ?? null,
    headHash: head?.rowHash ?? null,
    algo: head?.hashAlgo ?? null,
    chainedRows: chained,
    unchainedRows: total - chained,
    keyConfigured: Boolean(chainKey()),
  };
}

export function auditRequestId(request: { headers: Headers }): string | null {
  return request.headers.get("x-request-id")?.slice(0, 128) ?? null;
}
