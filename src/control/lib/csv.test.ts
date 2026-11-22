import { describe, expect, it } from "vitest";

import { AUDIT_EXPORT_FIELDS, toCsv, USAGE_EXPORT_FIELDS } from "./csv";

describe("csv export", () => {
  it("escapes commas, quotes and newlines", () => {
    const csv = toCsv(["a", "b"], [["x,y", 'he said "hi"'], ["line\nbreak", 3]]);
    expect(csv.startsWith("\ufeff")).toBe(true);
    expect(csv).toContain('"x,y"');
    expect(csv).toContain('"he said ""hi"""');
    expect(csv).toContain('"line\nbreak"');
    expect(csv.endsWith("\r\n")).toBe(true);
  });

  it("renders null/boolean", () => {
    const csv = toCsv(["a", "b", "c"], [[null, true, false]]);
    expect(csv.split("\r\n")[1]).toBe(",true,false");
  });

  it("keeps the field whitelist stable", () => {
    expect(USAGE_EXPORT_FIELDS).toHaveLength(16);
    expect(USAGE_EXPORT_FIELDS).not.toContain("Department");
    expect(USAGE_EXPORT_FIELDS).toContain("Key");
    expect(USAGE_EXPORT_FIELDS).not.toContain("keyHash");
    // 审计导出是"链的一段"：Seq/Prev Hash/Row Hash 三列缺一不可，
    // 少了任何一列，归档出去的 CSV 就退化成可被改写的普通报表。
    expect(AUDIT_EXPORT_FIELDS).toEqual([
      "Seq",
      "Time",
      "Actor",
      "Action",
      "Detail",
      "Request ID",
      "Prev Hash",
      "Row Hash",
    ]);
    expect(AUDIT_EXPORT_FIELDS).not.toContain("actorId");
  });
});
