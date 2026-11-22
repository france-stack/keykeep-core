"use client";

import { useCallback, useEffect, useState } from "react";

import { AUDIT_ACTION_CATALOG } from "@/lib/audit-actions";

// 下拉项从 lib/audit-actions.ts 的唯一动作目录派生：新增动作不再需要改这个文件。
const ACTIONS: Array<{ value: string; label: string }> = [
  { value: "", label: "全部动作" },
  ...AUDIT_ACTION_CATALOG.map((spec) => ({ value: spec.value, label: spec.zh })),
];

type EventRow = {
  id: string;
  at: string;
  actor: { id: string; account: string; displayName: string } | null;
  action: string;
  actionLabel: string;
  detail: Record<string, unknown>;
  requestId: string | null;
};

type Payload = { total: number; page: number; pageSize: number; events: EventRow[] };

const PAGE_SIZE = 30;

const MAX_ARRAY_INLINE = 3;
const MAX_OBJECT_KEYS = 4;
const MAX_NESTING = 2;
const MAX_TITLE_CHARS = 2000;

/** 空值跳过；对象摊平而不是 String() 成 [object Object]（P7 的直接成因）。 */
function renderValue(value: unknown, depth: number): string {
  if (value === null || value === undefined) return "";
  if (Array.isArray(value)) {
    if (value.length === 0) return "[]";
    if (value.length <= MAX_ARRAY_INLINE) {
      return `[${value.map((item) => renderValue(item, depth + 1)).join(", ")}]`;
    }
    return `[${value.length} 项]`;
  }
  if (typeof value === "object") {
    // 过深就退回紧凑 JSON，避免递归展开一整棵对象树撑爆单元格。
    if (depth >= MAX_NESTING) return safeJson(value);
    const pairs = Object.entries(value as Record<string, unknown>)
      .filter(([, item]) => item !== undefined && item !== null)
      // 摊平成「键 值」，与报告建议的 `${k} ${String(val)}` 同一形态，只是值本身也会递归。
      .map(([key, item]) => `${key} ${renderValue(item, depth + 1)}`);
    if (pairs.length === 0) return "{}";
    if (pairs.length <= MAX_OBJECT_KEYS) return pairs.join(" / ");
    return `${pairs.slice(0, MAX_OBJECT_KEYS).join(" / ")} / …`;
  }
  return String(value);
}

function safeJson(value: unknown): string {
  try {
    return JSON.stringify(value) ?? String(value);
  } catch {
    return String(value);
  }
}

/** 审计明细的一行摘要：`dims=inputTokens 1000 / outputTokens 500 · keyId=abc`。 */
export function compact(detail: Record<string, unknown>): string {
  const pairs = Object.entries(detail ?? {})
    .filter(([, value]) => value !== undefined && value !== null)
    .map(([key, value]) => `${key}=${renderValue(value, 0)}`);
  return pairs.join(" · ");
}

/** 明细单元格的悬浮提示：完整 JSON，过长截断到 2000 字符。 */
export function detailTitle(detail: Record<string, unknown>): string {
  const text = safeJson(detail ?? {});
  return text.length > MAX_TITLE_CHARS ? `${text.slice(0, MAX_TITLE_CHARS)}…` : text;
}

export default function AuditClient() {
  const [action, setAction] = useState("");
  const [from, setFrom] = useState("");
  const [to, setTo] = useState("");
  const [page, setPage] = useState(1);
  const [data, setData] = useState<Payload | null>(null);
  const [error, setError] = useState("");

  const load = useCallback(async () => {
    const params = new URLSearchParams({ page: String(page), pageSize: String(PAGE_SIZE) });
    if (action) params.set("action", action);
    if (from) params.set("from", from);
    if (to) params.set("to", to);
    const response = await fetch(`/api/admin/audit?${params}`);
    if (response.status === 401) {
      window.location.assign("/login");
      return;
    }
    if (response.status === 403) {
      setError("只有管理员可以查看审计日志。");
      return;
    }
    if (!response.ok) {
      setError("加载审计日志失败。");
      return;
    }
    setError("");
    setData((await response.json()) as Payload);
  }, [action, from, to, page]);

  useEffect(() => {
    void load();
  }, [load]);

  const pageCount = data ? Math.max(1, Math.ceil(data.total / data.pageSize)) : 1;

  return (
    <>
      <div className="toolbar">
        <select
          aria-label="按动作筛选"
          value={action}
          onChange={(event) => {
            setAction(event.target.value);
            setPage(1);
          }}
          style={{ padding: "7px 10px", borderRadius: 8, border: "1px solid var(--line)", background: "var(--surface)", color: "var(--ink)" }}
        >
          {ACTIONS.map((item) => (
            <option key={item.value || "all"} value={item.value}>
              {item.label}
            </option>
          ))}
        </select>
        <input
          type="date"
          aria-label="开始日期"
          value={from}
          onChange={(event) => {
            setFrom(event.target.value);
            setPage(1);
          }}
          style={{ padding: "6px 9px", borderRadius: 8, border: "1px solid var(--line)", background: "var(--surface)", color: "var(--ink)" }}
        />
        <input
          type="date"
          aria-label="结束日期"
          value={to}
          onChange={(event) => {
            setTo(event.target.value);
            setPage(1);
          }}
          style={{ padding: "6px 9px", borderRadius: 8, border: "1px solid var(--line)", background: "var(--surface)", color: "var(--ink)" }}
        />
        <div className="grow" />
        <span className="section-sub">{data?.total ?? 0} 条</span>
        <a
          className="btn btn-sm"
          href={`/api/admin/audit/export?${new URLSearchParams({
            ...(action ? { action } : {}),
            ...(from ? { from } : {}),
            ...(to ? { to } : {}),
          }).toString()}`}
        >
          导出 CSV
        </a>
      </div>

      {error ? <div className="alert error" role="alert">{error}</div> : null}

      <section className="card panel">
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>时间</th>
                <th>操作者</th>
                <th>动作</th>
                <th>明细</th>
                <th>Request ID</th>
              </tr>
            </thead>
            <tbody>
              {(data?.events ?? []).map((row) => (
                <tr key={row.id}>
                  <td className="mono">{new Date(row.at).toLocaleString()}</td>
                  <td>
                    {row.actor ? (
                      <span className="employee">
                        <span className="mini-avatar">{(row.actor.displayName || row.actor.account || "?").slice(0, 1)}</span>
                        {row.actor.displayName || row.actor.account || row.actor.id.slice(0, 8)}
                      </span>
                    ) : (
                      <span className="kv">匿名</span>
                    )}
                  </td>
                  <td>
                    <span className="provider-chip">{row.actionLabel}</span>
                    <div className="kv">{row.action}</div>
                  </td>
                  <td
                    className="mono"
                    title={detailTitle(row.detail)}
                    style={{ maxWidth: 420, whiteSpace: "normal", wordBreak: "break-all" }}
                  >
                    {compact(row.detail)}
                  </td>
                  <td className="mono">{row.requestId ?? "—"}</td>
                </tr>
              ))}
              {data && data.events.length === 0 ? (
                <tr>
                  <td colSpan={5} className="section-sub">没有审计事件。</td>
                </tr>
              ) : null}
            </tbody>
          </table>
        </div>
        <div className="toolbar" style={{ marginTop: 12 }}>
          <button type="button" className="btn btn-sm" disabled={page <= 1} onClick={() => setPage((p) => p - 1)}>
            上一页
          </button>
          <span className="section-sub">第 {page} / {pageCount} 页</span>
          <button type="button" className="btn btn-sm" disabled={page >= pageCount} onClick={() => setPage((p) => p + 1)}>
            下一页
          </button>
        </div>
      </section>
    </>
  );
}
