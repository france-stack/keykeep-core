import { describe, expect, it } from "vitest";

import { TIMEZONE_OPTION, withUtcSession } from "./db-session";

describe("会话时区规整", () => {
  it("给带 schema 参数的 Prisma 连接串追加 options", () => {
    const result = withUtcSession("postgresql://u:p@host:5432/db?schema=public");
    expect(result).toContain("schema=public");
    expect(result).toContain(`options=${encodeURIComponent(TIMEZONE_OPTION)}`);
  });

  it("给不带查询串的连接串补上 ?", () => {
    expect(withUtcSession("postgresql://u:p@host:5432/db")).toBe(
      `postgresql://u:p@host:5432/db?options=${encodeURIComponent(TIMEZONE_OPTION)}`,
    );
  });

  it("已有 options 时不重复追加（重连时不会套娃）", () => {
    const once = withUtcSession("postgresql://u:p@host:5432/db?schema=public");
    expect(withUtcSession(once)).toBe(once);
  });

  it("空值原样返回", () => {
    expect(withUtcSession("")).toBe("");
  });

  it("编码后的值不破坏连接串的键值结构", () => {
    const result = withUtcSession("postgresql://u:p@host:5432/db?schema=public");
    const query = new URLSearchParams(result.split("?")[1]);
    expect(query.get("schema")).toBe("public");
    expect(query.get("options")).toBe(TIMEZONE_OPTION);
  });
});
