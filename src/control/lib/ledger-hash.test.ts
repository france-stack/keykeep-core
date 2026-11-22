import { describe, expect, it } from "vitest";

import { chainHash, GENESIS_HASH, type LedgerRow } from "./ledger-hash";

const golden: LedgerRow = {
  rid: "rid-1",
  tsSeconds: 1700000000,
  keyId: "k1",
  empId: "e1",
  deptId: "00000000-0000-0000-0000-000000000000",
  provider: "anthropic",
  alias: "a",
  modelReq: "a",
  modelServed: "m",
  authId: "auth",
  priceVersion: "v1",
  unpriced: false,
  inputTokens: 10n,
  outputTokens: 20n,
  cacheCreationInputTokens: 0n,
  cacheReadInputTokens: 0n,
  costCents: 5n,
  status: "success",
  requestId: "trace",
  stream: true,
  failed: false,
};

describe("ledger chain hash", () => {
  it("matches the Go golden vector", () => {
    expect(chainHash(GENESIS_HASH, golden)).toBe(
      "b06f1a53b0c86a722fea701451a4b4264d382463c010e8cfda34ef596deaf0c1",
    );
  });

  it("binds prev hash, cost and dept", () => {
    const base = chainHash(GENESIS_HASH, golden);
    expect(chainHash("deadbeef", golden)).not.toBe(base);
    expect(chainHash(GENESIS_HASH, { ...golden, costCents: 6n })).not.toBe(base);
    expect(chainHash(GENESIS_HASH, { ...golden, deptId: "11111111-1111-1111-1111-111111111111" })).not.toBe(base);
    expect(chainHash(GENESIS_HASH, { ...golden, deptId: null })).toBe(
      chainHash(GENESIS_HASH, { ...golden, deptId: "00000000-0000-0000-0000-000000000000" }),
    );
  });
});
