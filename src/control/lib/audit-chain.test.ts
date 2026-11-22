import { createHash, createHmac } from "node:crypto";

import { describe, expect, it } from "vitest";

import { GENESIS_HASH } from "./ledger-hash";
import {
  ALGO_HMAC,
  ALGO_SHA256,
  auditChainHash,
  auditChainInput,
  canonicalJson,
  createChainVerifier,
  hashAlgoFor,
  verifyAuditChain,
  AUDIT_CHAIN_LOCK,
  type AuditChainAlgo,
  type StoredAuditChainRow,
} from "./audit-chain";

type BuildOptions = {
  count?: number;
  algo?: AuditChainAlgo;
  key?: string | null;
  /** 前 legacy 行：链开始之前的存量历史，rowHash 为空。 */
  legacy?: number;
};

/**
 * 按生产路径（lib/audit.ts 的 insert→回读→算摘要→回填）造一条链。
 * 这里刻意不复用 recordAudit：它要连数据库；而是把它的规则逐条写出来，
 * 于是测试同时也是"规则有没有被写歪"的独立证据。
 */
function buildChain(opts: BuildOptions = {}): StoredAuditChainRow[] {
  const count = opts.count ?? 5;
  const algo = opts.algo ?? ALGO_SHA256;
  const key = opts.key ?? null;
  const at = Date.parse("2026-11-07T00:00:00Z");
  const rows: StoredAuditChainRow[] = [];
  let prevHash = GENESIS_HASH;
  for (let index = 0; index < legacyPlus(count, opts.legacy); index++) {
    const legacy = index < (opts.legacy ?? 0);
    const row: StoredAuditChainRow = {
      seq: BigInt(index + 1),
      at: new Date(at + index * 1000),
      actorId: legacy ? null : `emp-${index}`,
      action: legacy ? "auth.login" : "key.issue",
      detail: { index, nested: { b: 2, a: 1 }, amount: 1.0 },
      requestId: legacy ? null : `req-${index}`,
      prevHash: null,
      rowHash: null,
      hashAlgo: null,
    };
    if (!legacy) {
      row.prevHash = prevHash;
      row.rowHash = auditChainHash(prevHash, row, algo, key);
      row.hashAlgo = algo;
      prevHash = row.rowHash;
    }
    rows.push(row);
  }
  return rows;
}

function legacyPlus(count: number, legacy: number | undefined): number {
  return count + (legacy ?? 0);
}

function reasons(report: { issues: { reason: string }[] }): string[] {
  return report.issues.map((issue) => issue.reason);
}

describe("audit chain digests", () => {
  it("pins the advisory lock away from the ledger and idempotency locks", () => {
    // 7411001=账本 7411002=幂等守卫；撞号会让两条链互相串行，是隐性吞吐陷阱。
    expect(AUDIT_CHAIN_LOCK).toBe(7411003);
  });

  it("canonicalizes JSON the way JSONB stores it", () => {
    expect(canonicalJson({ b: 1, a: 2 })).toBe('{"a":2,"b":1}');
    expect(canonicalJson({ a: 2, b: 1 })).toBe('{"a":2,"b":1}');
    // JSONB 的归一化：1.0→1、1e3→1000、-0→0；摘要必须跟着走，否则复算永远失败。
    expect(canonicalJson({ a: 1.0 })).toBe('{"a":1}');
    expect(canonicalJson({ a: 1e3 })).toBe('{"a":1000}');
    expect(canonicalJson({ a: -0 })).toBe('{"a":0}');
    // 对象里的 undefined 键被 JSONB 丢掉；数组里的 undefined 降级成 null（否则元素位移）。
    expect(canonicalJson({ a: 1, b: undefined })).toBe('{"a":1}');
    expect(canonicalJson([1, undefined, 3])).toBe("[1,null,3]");
    expect(canonicalJson(null)).toBe("null");
    expect(canonicalJson([])).toBe("[]");
  });

  it("refuses values JSONB cannot hold", () => {
    // 这类值让"写进去的值"与"算摘要的值"分家，宁可写入前就炸。
    expect(() => canonicalJson(new Date(0))).toThrow(TypeError);
    expect(() => canonicalJson({ a: 1n })).toThrow(TypeError);
    expect(() => canonicalJson({ a: () => 1 })).toThrow(TypeError);
    expect(() => canonicalJson({ a: Number.NaN })).toThrow(TypeError);
    expect(() => canonicalJson(undefined)).toThrow(TypeError);
  });

  it("joins the fixed field order with the unit separator", () => {
    const row = buildChain({ count: 1 })[0];
    const input = auditChainInput(GENESIS_HASH, row);
    expect(input.split("\x1f")).toHaveLength(7);
    expect(input.split("\x1f")[1]).toBe("1");
    // null 与 "" 同形：库里没写 actorId 与写了空串，摘要必须一致。
    expect(auditChainInput(GENESIS_HASH, { ...row, actorId: null })).toBe(
      auditChainInput(GENESIS_HASH, { ...row, actorId: "" }),
    );
  });

  it("picks a1k only when a key is configured", () => {
    expect(hashAlgoFor(null)).toBe(ALGO_SHA256);
    expect(hashAlgoFor("")).toBe(ALGO_SHA256);
    expect(hashAlgoFor("secret")).toBe(ALGO_HMAC);
  });

  it("computes sha256 and HMAC over the same payload", () => {
    const row = buildChain({ count: 1 })[0];
    const payload = auditChainInput(GENESIS_HASH, row);
    expect(auditChainHash(GENESIS_HASH, row, ALGO_SHA256)).toBe(
      createHash("sha256").update(payload, "utf8").digest("hex"),
    );
    expect(auditChainHash(GENESIS_HASH, row, ALGO_HMAC, "k")).toBe(
      createHmac("sha256", "k").update(payload, "utf8").digest("hex"),
    );
    // 配了 a1k 却没密钥：早失败，不能悄悄退回裸 sha256。
    expect(() => auditChainHash(GENESIS_HASH, row, ALGO_HMAC, null)).toThrow(TypeError);
  });
});

describe("audit chain verification", () => {
  it("accepts an intact chain and reports the head", () => {
    const rows = buildChain({ count: 6 });
    const report = verifyAuditChain(rows);
    expect(report).toMatchObject({ ok: true, scanned: 6, chained: 6, unchainedBefore: 0 });
    expect(report.issues).toEqual([]);
    expect(report.firstChainedSeq).toBe("1");
    expect(report.headSeq).toBe("6");
    expect(report.headHash).toBe(rows[5].rowHash);
    expect(report.algos[ALGO_SHA256]).toBe(6);
  });

  it("counts pre-chain legacy rows without failing the chain", () => {
    const report = verifyAuditChain(buildChain({ count: 3, legacy: 2 }));
    expect(report.ok).toBe(true);
    expect(report.unchainedBefore).toBe(2);
    expect(report.chained).toBe(3);
    expect(report.firstChainedSeq).toBe("3");
  });

  it("flags a row that joined the chain without being written through it", () => {
    const rows = buildChain({ count: 5 });
    rows[3] = { ...rows[3], prevHash: null, rowHash: null, hashAlgo: null };
    const report = verifyAuditChain(rows);
    expect(report.ok).toBe(false);
    expect(report.unchainedInside).toBe(1);
    expect(reasons(report)).toContain("unchained_after_chain");
  });

  it("separates edited content from broken linkage", () => {
    // 改字段：只应报 hash_mismatch，链接仍是通的。
    const rows = buildChain({ count: 5 });
    rows[2] = { ...rows[2], detail: { index: 2, nested: { b: 2, a: 1 }, amount: 999 } };
    const report = verifyAuditChain(rows);
    expect(report.ok).toBe(false);
    expect(reasons(report)).toEqual(["hash_mismatch"]);
    expect(report.issues[0].seq).toBe("3");
  });

  it("detects a deleted middle row at its successor", () => {
    // 删中间行不改后继的 prevHash，于是断点自己说话。
    const rows = buildChain({ count: 5 });
    const report = verifyAuditChain(rows.filter((row) => row.seq !== 3n));
    expect(report.ok).toBe(false);
    expect(reasons(report)).toEqual(["prev_mismatch"]);
    expect(report.issues[0].seq).toBe("4");
    // 断点之后链重新自洽：一次损坏不该级联成满屏噪声。
    expect(report.headHash).toBe(rows[4].rowHash);
  });

  it("detects a spliced-in row even when its own digest is valid", () => {
    const rows = buildChain({ count: 5 });
    // 伪造者的最优策略：接着 seq1 造一条自己的行，摘要用同一条规则算——单看这行是合法的。
    const link = rows[0].rowHash;
    if (link === null) throw new Error("构造链的链头不应为空");
    const forged: StoredAuditChainRow = {
      ...rows[1],
      seq: 2n,
      action: "key.revoke",
      prevHash: link,
      rowHash: null,
      hashAlgo: ALGO_SHA256,
    };
    forged.rowHash = auditChainHash(link, forged, ALGO_SHA256);
    const report = verifyAuditChain([...rows.slice(0, 1), forged, ...rows.slice(1)]);
    // 插入的行自己干净，但它把原行的 seq 顶成了重复号，且原行声称的前驱不再是链上的前驱。
    expect(reasons(report)).toEqual(["seq_order", "prev_mismatch"]);
  });

  it("reports a missing prevHash separately from a mismatch", () => {
    const rows = buildChain({ count: 3 });
    rows[1] = { ...rows[1], prevHash: null };
    const report = verifyAuditChain(rows);
    expect(reasons(report)).toEqual(["prev_missing"]);
  });

  it("rejects unknown or missing hash algorithm markers without cascading", () => {
    const rows = buildChain({ count: 3 });
    rows[0] = { ...rows[0], hashAlgo: null };
    rows[1] = { ...rows[1], hashAlgo: "a9" };
    const report = verifyAuditChain(rows);
    expect(report.ok).toBe(false);
    expect(report.algoMissing).toBe(2);
    expect(reasons(report)).toEqual(["algo_missing", "algo_unknown:a9"]);
    // 算法标记只挡住"复算这一行"，不该挡住链接：库里记的 rowHash 仍是后继的前驱。
    expect(report.chained).toBe(1);
    expect(report.headHash).toBe(rows[2].rowHash);
  });

  it("stops early when a1k rows meet no key, and says so", () => {
    const rows = buildChain({ count: 5, algo: ALGO_HMAC, key: "secret" });
    const report = verifyAuditChain(rows, { key: null });
    expect(report.ok).toBe(false);
    expect(report.keyRequiredMissing).toBe(true);
    expect(report.stoppedEarly).toBe(true);
    // 早停意味着结论不完整：这一行都没复算成，后面的行更没看过，
    // 绝不能把结果报成"全部完好"。
    expect(report.chained).toBe(0);
    expect(report.scanned).toBe(1);
    expect(reasons(report)).toEqual(["chain_key_not_configured"]);
  });

  it("verifies a1k only with the right key", () => {
    const rows = buildChain({ count: 4, algo: ALGO_HMAC, key: "secret" });
    expect(verifyAuditChain(rows, { key: "secret" }).ok).toBe(true);
    const wrong = verifyAuditChain(rows, { key: "other" });
    expect(wrong.ok).toBe(false);
    expect(reasons(wrong)).toEqual(["hash_mismatch", "hash_mismatch", "hash_mismatch", "hash_mismatch"]);
  });

  it("proves why the head must be anchored outside the chain", () => {
    // 裸 sha256 的已知边界：只持有库写权限的人可以删掉尾巴并重算新尾巴。
    // 校验器看不出损坏——所以 headHash 必须定期抄进链外介质，差异即证据。
    const rows = buildChain({ count: 5 });
    const anchored = rows[4].rowHash;
    const truncated = rows.slice(0, 3);
    let prev = truncated[2].rowHash as string;
    for (let index = 3; index < 5; index++) {
      const row = { ...truncated[truncated.length - 1], seq: BigInt(index + 1), prevHash: prev };
      row.rowHash = auditChainHash(prev, row, ALGO_SHA256);
      prev = row.rowHash;
      truncated.push(row);
    }
    const report = verifyAuditChain(truncated);
    expect(report.ok).toBe(true);
    expect(report.headHash).not.toBe(anchored);
  });

  it("keeps seq ordering itself a finding", () => {
    const rows = buildChain({ count: 3 });
    const report = verifyAuditChain([rows[0], rows[2], rows[1]]);
    expect(report.ok).toBe(false);
    expect(reasons(report)).toContain("seq_order");
  });

  it("truncates the issue list instead of returning megabytes of report", () => {
    const rows = buildChain({ count: 60 }).map((row) => ({ ...row, detail: { tampered: true } }));
    const report = verifyAuditChain(rows);
    expect(report.issues).toHaveLength(50);
    expect(report.issuesTruncated).toBe(true);
    expect(report.ok).toBe(false);
  });

  it("resumes scanning after a halted verifier only by returning false", () => {
    const rows = buildChain({ count: 4, algo: ALGO_HMAC, key: "secret" });
    const verifier = createChainVerifier({ key: null });
    expect(verifier.feed(rows[0])).toBe(false);
    // 已停机的 verifier 不再消费行，但报告仍然是可用的。
    expect(verifier.feed(rows[1])).toBe(false);
    expect(verifier.report().scanned).toBe(1);
  });
});
