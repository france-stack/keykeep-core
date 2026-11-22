"use client";

import { useCallback, useEffect, useState } from "react";

type Rule = {
  id: string;
  name: string;
  pattern: string;
  kind: string;
  target: string;
  replacement: string;
  enabled: boolean;
  priority: number;
};

const EMPTY = {
  name: "",
  pattern: "",
  kind: "regex",
  target: "request",
  replacement: "",
  priority: "100",
};

export default function RedactionClient({ mode }: { mode: string }) {
  const [rules, setRules] = useState<Rule[]>([]);
  const [form, setForm] = useState(EMPTY);
  const [error, setError] = useState("");
  const [ok, setOk] = useState("");
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    const response = await fetch("/api/admin/redact-rules");
    if (response.status === 401) {
      window.location.assign("/login");
      return;
    }
    if (response.status === 403) {
      setError("只有管理员可以管理脱敏规则。");
      return;
    }
    if (!response.ok) {
      setError("加载脱敏规则失败。");
      return;
    }
    const data = (await response.json()) as { rules: Rule[] };
    setRules(data.rules);
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  async function create(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setError("");
    setOk("");
    setBusy(true);
    try {
      const response = await fetch("/api/admin/redact-rules", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ ...form, priority: Number(form.priority) }),
      });
      if (!response.ok) {
        setError("创建失败，请检查名称、正则和优先级。");
        return;
      }
      setForm(EMPTY);
      setOk("规则已创建，网关将在 2 秒内重载。");
      await load();
    } finally {
      setBusy(false);
    }
  }

  async function toggle(rule: Rule) {
    setError("");
    const response = await fetch(`/api/admin/redact-rules/${rule.id}`, {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ enabled: !rule.enabled }),
    });
    if (!response.ok) {
      setError("更新失败。");
      return;
    }
    await load();
  }

  async function remove(rule: Rule) {
    setError("");
    const response = await fetch(`/api/admin/redact-rules/${rule.id}`, { method: "DELETE" });
    if (!response.ok) {
      setError("删除失败。");
      return;
    }
    await load();
  }

  return (
    <>
      {error ? <div className="alert error" role="alert">{error}</div> : null}
      {ok ? <div className="alert ok" role="status">{ok}</div> : null}

      <section className="card panel">
        <div className="panel-head">
          <div>
            <h2 className="section-title">脱敏模式</h2>
            <p className="section-sub">
              网关当前预设：<strong>{mode}</strong>。内置规则始终生效；下面的规则用于追加或覆盖内置规则。
              映射只保留在内存中，不会写入日志或账本。
            </p>
          </div>
          <span className="tag-warn">FR-63 / FR-68</span>
        </div>
        <a className="btn" href="/api/admin/diagnostics">下载诊断包</a>
      </section>

      <section className="card panel">
        <div className="panel-head">
          <div>
            <h2 className="section-title">新增规则</h2>
            <p className="section-sub">支持正则或字面量；优先级 1–1000，数字小的先执行。替换内容留空时使用 ⟦PII-0001⟧ 占位符。</p>
          </div>
        </div>
        <form onSubmit={create}>
          <div className="form-row">
            <div className="field"><label htmlFor="rr-name">名称</label><input id="rr-name" value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} required /></div>
            <div className="field"><label htmlFor="rr-kind">类型</label><select id="rr-kind" value={form.kind} onChange={(e) => setForm({ ...form, kind: e.target.value })}><option value="regex">正则</option><option value="literal">字面量</option></select></div>
            <div className="field"><label htmlFor="rr-target">作用对象</label><select id="rr-target" value={form.target} onChange={(e) => setForm({ ...form, target: e.target.value })}><option value="request">请求</option><option value="log">日志</option><option value="diagnostic">诊断</option><option value="all">全部</option></select></div>
            <div className="field"><label htmlFor="rr-priority">优先级</label><input id="rr-priority" type="number" min={1} max={1000} value={form.priority} onChange={(e) => setForm({ ...form, priority: e.target.value })} /></div>
          </div>
          <div className="form-row">
            <div className="field" style={{ flex: 2 }}><label htmlFor="rr-pattern">匹配规则</label><input id="rr-pattern" value={form.pattern} onChange={(e) => setForm({ ...form, pattern: e.target.value })} required /></div>
            <div className="field"><label htmlFor="rr-replacement">替换内容</label><input id="rr-replacement" value={form.replacement} onChange={(e) => setForm({ ...form, replacement: e.target.value })} placeholder="可选" /></div>
          </div>
          <button className="btn btn-primary" type="submit" disabled={busy}>{busy ? "创建中…" : "创建规则"}</button>
        </form>
      </section>

      <section className="card panel">
        <div className="panel-head"><div><h2 className="section-title">规则</h2><p className="section-sub">内置规则不在此列出；同名自定义规则会覆盖内置规则。</p></div></div>
        <table className="table">
          <thead><tr><th>名称</th><th>匹配规则</th><th>类型</th><th>作用对象</th><th>优先级</th><th>启用</th><th /></tr></thead>
          <tbody>
            {rules.length === 0 ? <tr><td colSpan={7}>没有自定义规则。</td></tr> : rules.map((rule) => (
              <tr key={rule.id}>
                <td>{rule.name}</td>
                <td><code>{rule.pattern}</code></td>
                <td>{rule.kind}</td>
                <td>{rule.target}</td>
                <td>{rule.priority}</td>
                <td>{rule.enabled ? "是" : "否"}</td>
                <td>
                  <button className="btn" type="button" onClick={() => void toggle(rule)}>{rule.enabled ? "停用" : "启用"}</button>{" "}
                  <button className="btn" type="button" onClick={() => void remove(rule)}>删除</button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </section>
    </>
  );
}
