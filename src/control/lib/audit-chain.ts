import { createHash, createHmac } from "node:crypto";

import { GENESIS_HASH } from "./ledger-hash";

// 审计事件的防篡改链。
//
// 与 lib/ledger-hash.ts（网关账本链）同族：固定字段顺序、\x1f 分隔、sha256 hex、
// 同一串 64 个 0 作为创世。分叉只有一处，而且是必须的：
//
// detail 列是 JSONB。Postgres 会归一化它——键序不保留、重复键只留最后一个、
// 数字按 numeric 重写（1.0 变 1、1e3 变 1000）。如果摘要用"写入前的 JS 值"
// 算，而校验用"读回来的库值"算，两者对不上，链就成了自毁装置。
// 因此本模块只认一条规则：**摘要输入永远取自从库里读回来的那一行**
// （见 lib/audit.ts 的写入路径：insert → 回读 → 算摘要 → 回填）。

// 与网关的锁号同族：账本 7411001、幂等守卫 7411002。
export const AUDIT_CHAIN_LOCK = 7411003;

export const ALGO_SHA256 = "a1";
export const ALGO_HMAC = "a1k";

export type AuditChainAlgo = typeof ALGO_SHA256 | typeof ALGO_HMAC;

export type AuditChainRow = {
  seq: bigint;
  at: Date;
  actorId: string | null;
  action: string;
  detail: unknown;
  requestId: string | null;
};

export type StoredAuditChainRow = AuditChainRow & {
  prevHash: string | null;
  rowHash: string | null;
  hashAlgo: string | null;
};

/**
 * 配了链密钥就用 HMAC：只持有数据库写权限的人无法续写尾巴。
 * 没配就退化为裸 sha256 —— 仍能发现改行/删中间行，但挡不住"删掉尾巴重算"，
 * 所以必须配合 /api/admin/audit/chain 的链头锚点定期留档。
 */
export function hashAlgoFor(key: string | null | undefined): AuditChainAlgo {
  return key && key.length > 0 ? ALGO_HMAC : ALGO_SHA256;
}

/**
 * 确定性 JSON：对象键按码元序排序、undefined 键丢弃（与 JSONB 落库结果一致）、
 * -0 归一为 0。只接受纯 JSON；Date/BigInt/function/symbol 一律抛错——
 * 这类值 Prisma 本来也写不进 JSONB，早失败比"写进去但摘要说不清"好。
 */
export function canonicalJson(value: unknown): string {
  if (value === null) return "null";
  switch (typeof value) {
    case "boolean":
      return value ? "true" : "false";
    case "number":
      if (!Number.isFinite(value)) throw new TypeError("audit detail 含非有限数字");
      // 整数分支保证 1.0 与 1 同形；小数交给 String()，与 JSON.stringify 一致。
      return Object.is(value, -0) ? "0" : String(value);
    case "string":
      return JSON.stringify(value);
    case "undefined":
      throw new TypeError("audit detail 不能是 undefined");
    case "bigint":
    case "function":
    case "symbol":
      throw new TypeError("audit detail 必须是纯 JSON");
  }
  if (value instanceof Date) throw new TypeError("audit detail 里的 Date 请先转 ISO 字符串");
  if (Array.isArray(value)) {
    // 数组里的 undefined 按 JSON 规则降级成 null，否则元素会位移。
    return `[${value.map((item) => (item === undefined ? "null" : canonicalJson(item))).join(",")}]`;
  }
  const record = value as Record<string, unknown>;
  const parts: string[] = [];
  for (const key of Object.keys(record).sort()) {
    const item = record[key];
    if (item === undefined) continue;
    parts.push(`${JSON.stringify(key)}:${canonicalJson(item)}`);
  }
  return `{${parts.join(",")}}`;
}

/** 摘要输入：字段顺序固定，空串代表 null（null 与 "" 在审计语义上等价）。 */
export function auditChainInput(prevHash: string, row: AuditChainRow): string {
  return [
    prevHash,
    row.seq.toString(),
    String(row.at.getTime()),
    row.actorId ?? "",
    row.action,
    canonicalJson(row.detail),
    row.requestId ?? "",
  ].join("\x1f");
}

export function auditChainHash(
  prevHash: string,
  row: AuditChainRow,
  algo: AuditChainAlgo,
  key?: string | null,
): string {
  const payload = auditChainInput(prevHash, row);
  if (algo === ALGO_HMAC) {
    if (!key) throw new TypeError("链算法 a1k 需要 KEYKEEP_AUDIT_CHAIN_KEY");
    return createHmac("sha256", key).update(payload, "utf8").digest("hex");
  }
  return createHash("sha256").update(payload, "utf8").digest("hex");
}

export type ChainIssue = { seq: string; reason: string };

export type ChainReport = {
  ok: boolean;
  /** 读过来参与走查的行数（含未链行）。 */
  scanned: number;
  /** 实际复算过摘要的行数（算法标记缺失的行只推进链接、不计入）。 */
  chained: number;
  /** 链起点之前的存量行：迁移前的历史，设计上无法回链，如实计数而不是假装通过。 */
  unchainedBefore: number;
  /** 链已经开始后又出现的未链行：有代码绕过了写入路径，必须报警。 */
  unchainedInside: number;
  /** 已链接行里缺链算法标记（无法确定该用哪种摘要复算）。 */
  algoMissing: number;
  /** 行里用了 HMAC 但本次校验没拿到密钥。 */
  keyRequiredMissing: boolean;
  /** 因缺密钥提前停止：后面的行没看过，不能把结果当成"全部完好"。 */
  stoppedEarly: boolean;
  issues: ChainIssue[];
  issuesTruncated: boolean;
  algos: Record<string, number>;
  firstChainedSeq: string | null;
  headSeq: string | null;
  headHash: string | null;
};

const MAX_ISSUES = 50;

export type ChainVerifier = {
  /** 喂一行；返回 false 表示已无法继续复算（HMAC 缺密钥），调用方应停止读取。 */
  feed(row: StoredAuditChainRow): boolean;
  report(): ChainReport;
};

/**
 * 走链状态机。抽出来是因为生产端要分页读（lib/audit.ts）、测试端要一次性喂数组
 * ——判定逻辑必须只有一份，否则测的就不是线上跑的那套。
 *
 * 调用方必须按 seq 升序喂；这里再兜一次顺序检查，因为 seq 唯一约束一旦被人改掉，
 * 链的"顺序"语义就没了。seq 允许有洞（事务回滚会烧掉序列号），有洞不算损坏——
 * 删除行的证据是后继的 prevHash 对不上，不是编号不连续。
 */
export function createChainVerifier(opts: { key?: string | null } = {}): ChainVerifier {
  const key = opts.key ?? null;
  const report: ChainReport = {
    ok: true,
    scanned: 0,
    chained: 0,
    unchainedBefore: 0,
    unchainedInside: 0,
    algoMissing: 0,
    keyRequiredMissing: false,
    stoppedEarly: false,
    issues: [],
    issuesTruncated: false,
    algos: {},
    firstChainedSeq: null,
    headSeq: null,
    headHash: null,
  };
  const pushIssue = (seq: bigint, reason: string) => {
    if (report.issues.length >= MAX_ISSUES) {
      report.issuesTruncated = true;
      return;
    }
    report.issues.push({ seq: seq.toString(), reason });
  };

  let expectedPrev = GENESIS_HASH;
  let seenChained = false;
  let previousSeq: bigint | null = null;
  let halted = false;

  return {
    feed(row) {
      if (halted) return false;
      report.scanned++;
      if (previousSeq !== null && row.seq <= previousSeq) {
        pushIssue(row.seq, "seq_order");
        report.ok = false;
      }
      previousSeq = row.seq;

      if (row.rowHash === null || row.rowHash === "") {
        if (seenChained) {
          report.unchainedInside++;
          pushIssue(row.seq, "unchained_after_chain");
          report.ok = false;
        } else {
          report.unchainedBefore++;
        }
        return true;
      }

      const algo = row.hashAlgo ?? "";
      if (algo !== ALGO_SHA256 && algo !== ALGO_HMAC) {
        report.algoMissing++;
        pushIssue(row.seq, algo === "" ? "algo_missing" : `algo_unknown:${algo}`);
        report.ok = false;
        // 这一行复算不了，但它的链接信息仍然可用：库里记录的 rowHash 就是后继声称的
        // 前驱。不推进 expectedPrev 的话，一处算法标记问题会把后面所有行全部误报成
        // prev_mismatch，把"1 个可修的数据问题"变成"整条链不可信"的假象。
        seenChained = true;
        if (report.firstChainedSeq === null) report.firstChainedSeq = row.seq.toString();
        expectedPrev = row.rowHash;
        report.headSeq = row.seq.toString();
        report.headHash = row.rowHash;
        return true;
      }
      report.algos[algo] = (report.algos[algo] ?? 0) + 1;

      if (algo === ALGO_HMAC && !key) {
        report.keyRequiredMissing = true;
        report.ok = false;
        report.stoppedEarly = true;
        halted = true;
        // 后面每一行都复算不了，早停比刷 50 条同类问题有用。
        pushIssue(row.seq, "chain_key_not_configured");
        return false;
      }

      seenChained = true;
      report.chained++;
      if (report.firstChainedSeq === null) report.firstChainedSeq = row.seq.toString();

      // 两类损坏必须分开报，否则一次篡改会同时污染两个结论：
      //   prev_mismatch —— 行声称的前驱不是实际前驱（插行、删行、重排的证据）；
      //   hash_mismatch —— 行内容（含它声称的前驱）与已记录的摘要不符（改字段的证据）。
      // 所以复算用 row.prevHash，链接核对用 expectedPrev。
      if (row.prevHash === null || row.prevHash === "") {
        pushIssue(row.seq, "prev_missing");
        report.ok = false;
      } else {
        if (row.prevHash !== expectedPrev) {
          // expectedPrev 还是创世值时，这条就是"链头之前的行被删了并想蒙过去"。
          pushIssue(row.seq, "prev_mismatch");
          report.ok = false;
        }
        try {
          if (auditChainHash(row.prevHash, row, algo, key) !== row.rowHash) {
            pushIssue(row.seq, "hash_mismatch");
            report.ok = false;
          }
        } catch (error) {
          pushIssue(row.seq, `detail_uncanonicalizable:${error instanceof Error ? error.message : "?"}`);
          report.ok = false;
        }
      }

      expectedPrev = row.rowHash;
      report.headSeq = row.seq.toString();
      report.headHash = row.rowHash;
      return true;
    },
    report() {
      return report;
    },
  };
}

/** 一次性喂完的同步驱动：测试与读得下全表的场景使用。 */
export function verifyAuditChain(
  rows: Iterable<StoredAuditChainRow>,
  opts: { key?: string | null } = {},
): ChainReport {
  const verifier = createChainVerifier(opts);
  for (const row of rows) {
    if (!verifier.feed(row)) break;
  }
  return verifier.report();
}
