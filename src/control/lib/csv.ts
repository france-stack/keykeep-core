// CSV 导出：UTF-8 BOM + CRLF，便于 Excel 直接打开；字段白名单由调用方保证。
export type CsvCell = string | number | bigint | null | undefined | boolean;

function cell(value: CsvCell): string {
  if (value === null || value === undefined) return "";
  let text = typeof value === "boolean" ? (value ? "true" : "false") : String(value);
  // 公式注入防护：Excel 会把 = + - @ 开头（含 Tab/CR 前缀）的单元格当公式执行。
  // 当前导出字段都是管理员可控的名称，但审计 detail 是自由文本，前置单引号中和。
  if (/^[=+\-@\t\r]/.test(text)) {
    text = `'` + text;
  }
  if (/[",\r\n]/.test(text) || text !== text.trim()) {
    return `"${text.replace(/"/g, '""')}"`;
  }
  return text;
}

export function toCsv(headers: string[], rows: CsvCell[][]): string {
  const lines = [headers.map(cell).join(",")];
  for (const row of rows) lines.push(row.map(cell).join(","));
  return "\ufeff" + lines.join("\r\n") + "\r\n";
}

/** 仅这些字段允许导出（FR-41 字段白名单）。 */
export const USAGE_EXPORT_FIELDS = [
  "Time",
  "Employee account",
  "Employee name",
  "Key",
  "Provider",
  "Requested model",
  "Served model",
  "Price version",
  "Input tokens",
  "Output tokens",
  "Cache read tokens",
  "Cache write tokens",
  "Cost (cents)",
  "Status",
  "Stream",
  "Latency (ms)",
] as const;

// 审计导出即链片段：Seq / Prev Hash / Row Hash 让归档出去的 CSV 在不接触数据库
// 的情况下仍可逐行复算。三列的取值全部来自库里那一行，不在导出时重新计算，
// 否则"导出"本身就成了第二个可以伪造摘要的地方。
export const AUDIT_EXPORT_FIELDS = [
  "Seq",
  "Time",
  "Actor",
  "Action",
  "Detail",
  "Request ID",
  "Prev Hash",
  "Row Hash",
] as const;
