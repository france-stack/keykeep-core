"use client";

import { useCallback, useEffect, useState } from "react";

type BudgetRow = {
  id: string;
  scope: string;
  scopeId: string;
  scopeName: string;
  period: string;
  limitCents: number;
  usedCents: number;
  reservedCents: number;
};

type Payload = {
  budgets: BudgetRow[];
  users: Array<{ id: string; name: string }>;
};

function money(cents: number): string {
  return `¥${(cents / 100).toFixed(2)}`;
}

export default function BudgetsClient() {
  const [data, setData] = useState<Payload>({ budgets: [], users: [] });
  const [scope, setScope] = useState<"global" | "emp">("global");
  const [scopeId, setScopeId] = useState("");
  const [limitYuan, setLimitYuan] = useState("1000");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    const response = await fetch("/api/admin/budgets");
    if (response.status === 401) {
      window.location.assign("/login");
      return;
    }
    if (response.status === 403) {
      setError("只有管理员可以管理预算。");
      return;
    }
    if (!response.ok) {
      setError("加载预算失败。");
      return;
    }
    setData((await response.json()) as Payload);
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  async function save(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setError("");
    const limitCents = Math.round(Number(limitYuan) * 100);
    if (!Number.isFinite(limitCents) || limitCents < 0) {
      setError("限额必须是非负数。");
      return;
    }
    setBusy(true);
    try {
      const response = await fetch("/api/admin/budgets", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ scope, scopeId: scope === "global" ? "global" : scopeId, period: "monthly", limitCents }),
      });
      if (!response.ok) {
        setError("保存失败，请检查范围与对象。");
        return;
      }
      await load();
    } finally {
      setBusy(false);
    }
  }

  async function remove(id: string) {
    setError("");
    const response = await fetch(`/api/admin/budgets?id=${id}`, { method: "DELETE" });
    if (!response.ok) {
      // 409 = 该预算已有用量流水，外键 RESTRICT 拦下了删除：记录还在，不是 404。
      // 产品层软删（Budget.archivedAt）需产品确认，这里只给可操作的替代做法。
      setError(
        response.status === 409
          ? "该预算已有用量流水，无法删除（流水不会随之删除）。如需停用请改为把限额调为 0。"
          : "删除失败。",
      );
      return;
    }
    await load();
  }

  const targets = scope === "emp" ? data.users.map((u) => ({ id: u.id, name: u.name })) : [];
  const missing = data.budgets.length === 0;

  return (
    <>
      {error ? <div className="alert error" role="alert">{error}</div> : null}
      {missing ? (
        <div className="alert warn" role="status">
          还没有预算。没有配额度的层级视为不限；建议至少配置一条全局每月预算。
        </div>
      ) : null}

      <section className="card panel">
        <div className="panel-head">
          <div>
            <h2 className="section-title">设置每月预算</h2>
            <p className="section-sub">员工与全局取更严的那个。已存在的行只更新限额。</p>
          </div>
          <span className="tag-warn">金额单位：元</span>
        </div>
        <form onSubmit={save}>
          <div className="form-row">
            <div className="field">
              <label htmlFor="scope">范围</label>
              <select
                id="scope"
                value={scope}
                onChange={(event) => {
                  setScope(event.target.value as "global" | "emp");
                  setScopeId("");
                }}
              >
                <option value="global">全局</option>
                <option value="emp">员工</option>
              </select>
            </div>
            {targets.length > 0 ? (
              <div className="field">
                <label htmlFor="target">对象</label>
                <select id="target" value={scopeId} onChange={(event) => setScopeId(event.target.value)} required>
                  <option value="">请选择</option>
                  {targets.map((target) => (
                    <option key={target.id} value={target.id}>
                      {target.name}
                    </option>
                  ))}
                </select>
              </div>
            ) : null}
            <div className="field">
              <label htmlFor="limit">每月限额（元）</label>
              <input
                id="limit"
                type="number"
                min="0"
                step="0.01"
                value={limitYuan}
                onChange={(event) => setLimitYuan(event.target.value)}
                required
              />
            </div>
          </div>
          <button className="btn btn-primary" type="submit" disabled={busy || (scope !== "global" && !scopeId)}>
            {busy ? "保存中…" : "保存预算"}
          </button>
        </form>
      </section>

      <section className="card panel">
        <div className="panel-head">
          <div>
            <h2 className="section-title">预算</h2>
            <p className="section-sub">已用 + 预留是当前占用；请求结束后预留即释放。</p>
          </div>
        </div>
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>范围</th>
                <th>对象</th>
                <th>周期</th>
                <th>限额</th>
                <th>已用 / 预留</th>
                <th>用量水位</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {data.budgets.map((budget) => {
                const used = budget.usedCents + budget.reservedCents;
                const pct = budget.limitCents > 0 ? Math.min(100, Math.round((used / budget.limitCents) * 100)) : 0;
                return (
                  <tr key={budget.id}>
                    <td>{budget.scope === "global" ? "全局" : "员工"}</td>
                    <td>{budget.scopeName}</td>
                    <td className="mono">{budget.period}</td>
                    <td className="mono">{money(budget.limitCents)}</td>
                    <td className="mono">
                      {money(budget.usedCents)} / {money(budget.reservedCents)}
                    </td>
                    <td>
                      <div className={`track ${pct >= 90 ? "danger" : pct >= 70 ? "warn" : ""}`}>
                        <i style={{ width: `${pct}%` }} />
                      </div>
                      <span className="kv">{pct}%</span>
                    </td>
                    <td>
                      <button type="button" className="btn btn-sm" onClick={() => void remove(budget.id)}>
                        删除
                      </button>
                    </td>
                  </tr>
                );
              })}
              {data.budgets.length === 0 ? (
                <tr>
                  <td colSpan={7} className="section-sub">还没有预算。</td>
                </tr>
              ) : null}
            </tbody>
          </table>
        </div>
      </section>
    </>
  );
}
