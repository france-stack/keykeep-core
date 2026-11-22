import { describe, expect, it } from "vitest";

import { isUuid } from "./uuid";

describe("isUuid", () => {
  it("accepts canonical UUIDs (any case) and the zero UUID", () => {
    expect(isUuid("0a1b2c3d-4e5f-6789-abcd-ef0123456789")).toBe(true);
    expect(isUuid("0A1B2C3D-4E5F-6789-ABCD-EF0123456789")).toBe(true);
    expect(isUuid("00000000-0000-0000-0000-000000000000")).toBe(true);
  });

  it("rejects anything Postgres would choke on or silently reshape", () => {
    expect(isUuid("")).toBe(false);
    expect(isUuid("not-a-uuid")).toBe(false);
    expect(isUuid("0a1b2c3d-4e5f-6789-abcd-ef012345678")).toBe(false); // 短一位
    expect(isUuid("0a1b2c3d-4e5f-6789-abcd-ef01234567890")).toBe(false); // 长一位
    expect(isUuid("0a1b2c3d4e5f6789abcdef0123456789")).toBe(false); // 无连字符变体
    expect(isUuid("{0a1b2c3d-4e5f-6789-abcd-ef0123456789}")).toBe(false); // 花括号变体
    expect(isUuid("0a1b2c3d-4e5f-6789-abcd-ef012345678g")).toBe(false); // 非十六进制
    expect(isUuid("0a1b2c3d-4e5f-6789-abcd-ef0123456789' OR '1'='1")).toBe(false);
    expect(isUuid(" 0a1b2c3d-4e5f-6789-abcd-ef0123456789")).toBe(false); // 不修剪空白
  });
});