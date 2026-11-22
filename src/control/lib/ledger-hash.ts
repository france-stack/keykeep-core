import { createHash } from "node:crypto";

// 与网关 internal/ledger 的 ChainHash 保持一致：
// 字段顺序固定，\x1f 分隔，布尔 0/1，时间取 Unix 秒（JS Date 只有毫秒精度）。

export const GENESIS_HASH = "0".repeat(64);
// 无部门员工在网关侧统一写成零 UUID，校验时必须用同样的值复算。
const ZERO_DEPT = "00000000-0000-0000-0000-000000000000";

export type LedgerRow = {
  rid: string;
  tsSeconds: number | bigint;
  keyId: string;
  empId: string;
  deptId: string | null;
  provider: string;
  alias: string;
  modelReq: string;
  modelServed: string;
  authId: string;
  priceVersion: string;
  unpriced: boolean;
  inputTokens: bigint;
  outputTokens: bigint;
  cacheCreationInputTokens: bigint;
  cacheReadInputTokens: bigint;
  costCents: bigint;
  status: string;
  requestId: string;
  stream: boolean;
  failed: boolean;
};

export function chainHash(prev: string, row: LedgerRow): string {
  const fields = [
    prev,
    row.rid,
    row.tsSeconds.toString(),
    row.keyId,
    row.empId,
    row.deptId ?? ZERO_DEPT,
    row.provider,
    row.alias,
    row.modelReq,
    row.modelServed,
    row.authId,
    row.priceVersion,
    row.unpriced ? "1" : "0",
    row.inputTokens.toString(),
    row.outputTokens.toString(),
    row.cacheReadInputTokens.toString(),
    row.cacheCreationInputTokens.toString(),
    row.costCents.toString(),
    row.status,
    row.requestId,
    row.stream ? "1" : "0",
    row.failed ? "1" : "0",
  ];
  return createHash("sha256").update(fields.join("\x1f"), "utf8").digest("hex");
}
