"use client";

import { useCallback, useEffect, useState } from "react";

import { Icon } from "./Icon";
import { SegmentedTabs } from "./SegmentedTabs";
import { SkeletonCards, SkeletonTable } from "./Skeleton";

type Totals = {
  calls: number;
  errors: number;
  rejected: number;
  rateLimited: number;
  overloaded: number;
  inputTokens: number;
  outputTokens: number;
  costCents: number;
};

type Summary = {
  range: "today" | "month";
  totals: Totals;
  trend: Array<{ day: string; costCents: number; calls: number }>;
  topModels: Array<{ model: string; calls: number; costCents: number }>;
  budgets: Array<{
    id: string;
    scope: string;
    scopeId: string;
    limitCents: number;
    usedCents: number;
    reservedCents: number;
  }>;
};

type RequestRow = {
  rid: string;
  ts: string;
  emp: { account: string; displayName: string };
  key: string;
  provider: string;
  modelReq: string;
  modelServed: string;
  inputTokens: number;
  outputTokens: number;
  cacheReadTokens: number;
  costCents: number;
  status: string;
  stream: boolean;
  latencyMs: number | null;
  unpriced: boolean;
};

type Requests = { total: number; page: number; pageSize: number; requests: RequestRow[] };
type Tab = "overview" | "requests";

const PAGE_SIZE = 20;
const EMPTY_SUMMARY: Summary = {
  range: "today",
  totals: {
    calls: 0,
    errors: 0,
    rejected: 0,
    rateLimited: 0,
    overloaded: 0,
    inputTokens: 0,
    outputTokens: 0,
    costCents: 0,
  },
  trend: [],
  topModels: [],
  budgets: [],
};

function money(cents: number): string {
  return `¥${(cents / 100).toFixed(2)}`;
}

function tokens(value: number): string {
  if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(2)}M`;
  if (value >= 1_000) return `${(value / 1_000).toFixed(1)}K`;
  return String(value);
}

export default function UsageClient({
  role,
  initialTab = "overview",
}: {
  role: string;
  initialTab?: Tab;
}) {
  const [tab, setTab] = useState<Tab>(initialTab);
  const [range, setRange] = useState<"today" | "month">("today");
  const [summary, setSummary] = useState<Summary>(EMPTY_SUMMARY);
  const [data, setData] = useState<Requests | null>(null);
  const [page, setPage] = useState(1);
  const [status, setStatus] = useState("");
  const [model, setModel] = useState("");
  const [modelInput, setModelInput] = useState("");
  const [verify, setVerify] = useState<{ ok: boolean; checked?: number; reason?: string } | null>(null);
  const [error, setError] = useState("");
  const [loadingSummary, setLoadingSummary] = useState(true);
  const [loadingRequests, setLoadingRequests] = useState(true);

  const loadSummary = useCallback(async () => {
    setLoadingSummary(true);
    const response = await fetch(`/api/admin/usage/summary?range=${range}`);
    if (response.status === 401) {
      window.location.assign("/login");
      return;
    }
    if (!response.ok) {
      setError("读取用量失败。");
      setLoadingSummary(false);
      return;
    }
    setSummary((await response.json()) as Summary);
    setLoadingSummary(false);
  }, [range]);

  const loadRequests = useCallback(async () => {
    setLoadingRequests(true);
    const params = new URLSearchParams({ page: String(page), pageSize: String(PAGE_SIZE) });
    if (status) params.set("status", status);
    if (model) params.set("model", model);
    const response = await fetch(`/api/admin/usage/requests?${params}`);
    if (!response.ok) {
      setError("读取请求明细失败。");
      setLoadingRequests(false);
      return;
    }
    setData((await response.json()) as Requests);
    setLoadingRequests(false);
  }, [page, status, model]);

  useEffect(() => {
    void loadSummary();
  }, [loadSummary]);

  useEffect(() => {
    void loadRequests();
  }, [loadRequests]);

  function changeTab(next: Tab) {
    setTab(next);
    const url = next === "requests" ? "/me/usage?tab=requests" : "/me/usage";
    window.history.replaceState(null, "", url);
  }

  async function runVerify() {
    setVerify(null);
    const response = await fetch("/api/admin/usage/verify");
    if (!response.ok) {
      setError("校验失败：只有老板可以校验账本。");
      return;
    }
    setVerify((await response.json()) as { ok: boolean; checked?: number; reason?: string });
  }

  const maxTrend = Math.max(1, ...summary.trend.map((day) => day.costCents));
  const pageCount = data ? Math.max(1, Math.ceil(data.total / data.pageSize)) : 1;

  return (
    <div className="page">
      <div className="page-head">
        <div>
          <h1 className="page-title">{role === "admin" ? "用量与请求" : "我的用量"}</h1>
          <p className="page-sub">{role === "admin" ? "整体趋势与预算水位；请求可逐条审计。" : "我自己的调用、费用与剩余额度。"}</p>
        </div>
        <div className="page-actions">
          <SegmentedTabs
            label="用量视图"
            value={tab}
            onChange={(id) => changeTab(id as Tab)}
            items={[{ id: "overview", label: "概览" }, { id: "requests", label: "请求明细" }]}
          />
        </div>
      </div>

      {error ? <div className="alert error" role="alert">{error}</div> : null}
      {verify ? (
        <div className={`alert ${verify.ok ? "ok" : "error"}`} role="status">
          {verify.ok
            ? `账本完整：已重算 ${verify.checked ?? 0} 行。`
            : `账本被破坏：${verify.reason ?? "hash_mismatch"}（已检查 ${verify.checked ?? 0} 行）。`}
        </div>
      ) : null}

      {tab === "overview" ? (
        <div id="panel-overview" role="tabpanel" aria-labelledby="tab-overview" className="stack-block">
          <div className="toolbar">
            <SegmentedTabs
              label="时间范围"
              value={range}
              onChange={(id) => setRange(id as "today" | "month")}
              items={[{ id: "today", label: "今天" }, { id: "month", label: "本月" }]}
            />
            <div className="grow" />
            {role === "admin" ? (
              <button type="button" className="btn btn-sm" onClick={() => void runVerify()}>校验账本哈希链</button>
            ) : null}
          </div>

          {loadingSummary ? <SkeletonCards count={4} /> : (
            <section className="kpis">
              <article className="card kpi">
                <div className="kpi-head"><span>调用次数</span><Icon name="list" size={16} /></div>
                <div className="kpi-value">{summary.totals.calls.toLocaleString("zh-CN")}</div>
                <div className="kpi-foot">
                  {/* 被网关挡下的请求不产生调用记录（P4），在这里单列，否则永远显示「0 次失败」。 */}
                  {summary.totals.errors} 次失败 · {summary.totals.rateLimited} 次被限流
                  {summary.totals.overloaded > 0 ? ` · ${summary.totals.overloaded} 次被过载拒绝` : null}
                  <span title="被限流或被过载保护挡下的请求没有发到上游，因此不计入调用次数；明细见审计日志。">
                    {" "}
                    · {range === "today" ? "今天" : "本月"}
                  </span>
                </div>
              </article>
              <article className="card kpi">
                <div className="kpi-head"><span>费用</span><Icon name="wallet" size={16} /></div>
                <div className="kpi-value">{money(summary.totals.costCents)}</div>
                <div className="kpi-foot">按模型目录计价</div>
              </article>
              <article className="card kpi">
                <div className="kpi-head"><span>输入 Token</span><Icon name="down" size={16} /></div>
                <div className="kpi-value">{tokens(summary.totals.inputTokens)}</div>
                <div className="kpi-foot">含缓存命中</div>
              </article>
              <article className="card kpi">
                <div className="kpi-head"><span>输出 Token</span><Icon name="up" size={16} /></div>
                <div className="kpi-value">{tokens(summary.totals.outputTokens)}</div>
                <div className="kpi-foot">计费依据</div>
              </article>
            </section>
          )}

          <section className="card panel">
            <div className="panel-head">
              <div>
                <h2 className="panel-title">费用趋势（最近 14 天）</h2>
                <p className="panel-sub">按天聚合；柱高是当天费用。</p>
              </div>
              <span className="pill"><span className="dot" />存 UTC · 按本地时间展示</span>
            </div>
            {loadingSummary ? <div className="stack"><span className="skeleton" style={{ height: 150 }} /></div> : summary.trend.length === 0 ? (
              <p className="empty">暂无数据。</p>
            ) : (
              <div className="bars" role="img" aria-label="最近 14 天费用趋势">
                {summary.trend.map((day, index) => (
                  <div className="bar-col" key={day.day} title={`${day.day} · ${money(day.costCents)} · ${day.calls} 次`}>
                    <div
                      className="bar"
                      style={{ height: `${Math.max(5, Math.round((day.costCents / maxTrend) * 100))}%`, ["--i" as string]: index }}
                    />
                    <span>{day.day.slice(5)}</span>
                  </div>
                ))}
              </div>
            )}
          </section>

          <section className="card panel">
            <div className="panel-head">
              <div>
                <h2 className="panel-title">模型排行</h2>
                <p className="panel-sub">按费用排序，前 5 名。</p>
              </div>
            </div>
            {summary.topModels.length === 0 ? (
              <p className="empty">暂无数据。</p>
            ) : (
              <ol className="model-list">
                {summary.topModels.map((item, index) => (
                  <li key={item.model}>
                    <span className="model-rank">{String(index + 1).padStart(2, "0")}</span>
                    <span className="model-name mono">{item.model || "unknown"}</span>
                    <span className="model-meta">{item.calls} 次</span>
                    <span className="mono">{money(item.costCents)}</span>
                  </li>
                ))}
              </ol>
            )}
          </section>

          {summary.budgets.length > 0 ? (
            <section className="card panel">
              <div className="panel-head">
                <div>
                  <h2 className="panel-title">预算水位</h2>
                  <p className="panel-sub">员工与全局预算取更严的一条；预占额度也计入。</p>
                </div>
              </div>
              <div className="quota-list">
                {summary.budgets.map((budget) => {
                  const used = budget.usedCents + budget.reservedCents;
                  const pct = budget.limitCents > 0 ? Math.min(100, Math.round((used / budget.limitCents) * 100)) : 0;
                  return (
                    <div className="quota" key={budget.id}>
                      <div className="quota-top">
                        <span className="quota-name">{budget.scope === "global" ? "全局" : `员工 ${budget.scopeId.slice(0, 8)}`}</span>
                        <span className="quota-kind">{pct}%</span>
                      </div>
                      <div className={`track ${pct >= 90 ? "danger" : pct >= 70 ? "warn" : ""}`}>
                        <i style={{ width: `${pct}%` }} />
                      </div>
                      <div className="quota-foot">
                        <span><strong>{money(used)}</strong> / {money(budget.limitCents)}</span>
                        <span>预占 {money(budget.reservedCents)}</span>
                      </div>
                    </div>
                  );
                })}
              </div>
            </section>
          ) : null}
        </div>
      ) : (
        <div id="panel-requests" role="tabpanel" aria-labelledby="tab-requests">
          <section className="card panel requests">
            <div className="panel-head">
              <div>
                <h2 className="panel-title">请求明细</h2>
                <p className="panel-sub">共 {data?.total ?? 0} 条，最新的在前。</p>
              </div>
              <div className="table-tools">
                {["", "success", "failed"].map((value) => (
                  <button
                    key={value || "all"}
                    type="button"
                    className="chip"
                    aria-pressed={status === value}
                    onClick={() => {
                      setStatus(value);
                      setPage(1);
                    }}
                  >
                    {value === "" ? "全部" : value === "success" ? "成功" : "失败"}
                  </button>
                ))}
                <form
                  onSubmit={(event) => {
                    event.preventDefault();
                    setModel(modelInput.trim());
                    setPage(1);
                  }}
                >
                  <input
                    className="table-filter"
                    placeholder="按模型筛选"
                    value={modelInput}
                    onChange={(event) => setModelInput(event.target.value)}
                  />
                </form>
                <a
                  className="btn btn-sm"
                  href={`/api/admin/usage/export?${new URLSearchParams({
                    ...(status ? { status } : {}),
                    ...(model ? { model } : {}),
                  }).toString()}`}
                >
                  导出 CSV
                </a>
              </div>
            </div>
            {loadingRequests ? <SkeletonTable rows={8} columns={8} /> : (
              <div className="table-wrap">
                <table>
                  <thead>
                    <tr>
                      <th>时间</th><th>员工</th><th>Key</th><th>模型</th><th>Token（入/出）</th><th>费用</th><th>状态</th><th>耗时</th>
                    </tr>
                  </thead>
                  <tbody>
                    {(data?.requests ?? []).map((row) => (
                      <tr key={row.rid}>
                        <td className="mono">{new Date(row.ts).toLocaleString()}</td>
                        <td>
                          <span className="employee">
                            <span className="mini-avatar">{row.emp.displayName.slice(0, 1)}</span>
                            {row.emp.displayName}
                          </span>
                        </td>
                        <td className="mono">{row.key}</td>
                        <td>
                          <div className="model-name">{row.modelServed || row.modelReq || "—"}</div>
                          <div className="model-meta">
                            <span className="provider-chip">{row.provider || "—"}</span>
                            {row.stream ? <span>流式</span> : null}
                            {row.unpriced ? <span className="tag-warn">未定价</span> : null}
                          </div>
                        </td>
                        <td className="mono">{tokens(row.inputTokens)} / {tokens(row.outputTokens)}</td>
                        <td className="mono">{money(row.costCents)}</td>
                        <td>
                          <span className={`status ${row.status === "success" ? "success" : "error"}`}>
                            {row.status === "success" ? "成功" : "失败"}
                          </span>
                        </td>
                        <td className="mono">{row.latencyMs ? `${row.latencyMs} ms` : "—"}</td>
                      </tr>
                    ))}
                    {data && data.requests.length === 0 ? (
                      <tr><td colSpan={8} className="empty">暂无记录。</td></tr>
                    ) : null}
                  </tbody>
                </table>
              </div>
            )}
            <div className="toolbar" style={{ marginTop: 12 }}>
              <button type="button" className="btn btn-sm" disabled={page <= 1} onClick={() => setPage((p) => p - 1)}>上一页</button>
              <span className="page-sub">第 {page} / {pageCount} 页</span>
              <button type="button" className="btn btn-sm" disabled={page >= pageCount} onClick={() => setPage((p) => p + 1)}>下一页</button>
            </div>
          </section>
        </div>
      )}
    </div>
  );
}
