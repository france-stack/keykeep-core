"use client";

import { useEffect, useState } from "react";

import { Icon } from "./Icon";

type UserRow = {
  id: string;
  account: string;
  displayName: string;
  disabled: boolean;
};

type KeyRow = {
  id: string;
  prefix4: string;
  fp6: string;
  emp: { id: string; account: string; displayName: string };
  label: string;
  distribution: { id: string; name: string } | null;
  allowedModels: string[];
  limits: Record<string, number>;
  revokedAt: string | null;
  createdAt: string;
};

const LIMIT_DIMS = ["rpm", "tpm", "itpm", "otpm", "concurrency"] as const;
type LimitDim = (typeof LIMIT_DIMS)[number];
type LimitDraft = Record<LimitDim, string>;

const DIM_LABELS: Record<string, string> = { rpm: "RPM", tpm: "TPM", itpm: "ITPM", otpm: "OTPM", concurrency: "并发数" };
const EMPTY_DRAFT: LimitDraft = { rpm: "", tpm: "", itpm: "", otpm: "", concurrency: "" };

function formatLimits(limits: Record<string, number>): string {
  const parts = LIMIT_DIMS.filter((dim) => limits[dim] > 0).map(
    (dim) => `${DIM_LABELS[dim] ?? dim.toUpperCase()} ${limits[dim]}`,
  );
  return parts.length > 0 ? parts.join(" · ") : "不限";
}

type Issued = { id: string; plaintext: string; prefix4: string; fp6: string };

export default function KeysClient() {
  const [users, setUsers] = useState<UserRow[]>([]);
  const [keys, setKeys] = useState<KeyRow[]>([]);
  const [empId, setEmpId] = useState("");
  const [count, setCount] = useState(1);
  const [modelsText, setModelsText] = useState("");
  const [issued, setIssued] = useState<Issued[]>([]);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [loaded, setLoaded] = useState(false);
  const [editing, setEditing] = useState<string | null>(null);
  const [draft, setDraft] = useState<LimitDraft>(EMPTY_DRAFT);

  async function load() {
    const [usersResponse, keysResponse] = await Promise.all([
      fetch("/api/admin/users"),
      fetch("/api/admin/keys"),
    ]);
    if (usersResponse.status === 401 || keysResponse.status === 401) {
      window.location.assign("/login");
      return;
    }
    if (usersResponse.ok) {
      const data = (await usersResponse.json()) as { users: UserRow[] };
      setUsers(data.users.filter((user) => !user.disabled));
    }
    if (keysResponse.ok) {
      const data = (await keysResponse.json()) as { keys: KeyRow[] };
      setKeys(data.keys);
    }
    setLoaded(true);
  }

  useEffect(() => {
    void load();
  }, []);

  async function issue(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setError("");
    setIssued([]);
    setBusy(true);
    try {
      const allowedModels = modelsText
        .split(/[,，\s]+/)
        .map((value) => value.trim())
        .filter(Boolean);
      const response = await fetch("/api/admin/keys", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ empId, count: Number(count), allowedModels }),
      });
      const data = (await response.json()) as { keys?: Issued[]; error?: { code?: string } };
      if (!response.ok) {
        setError(data.error?.code === "forbidden" ? "你没有权限为该员工签发密钥。" : "签发失败，请检查员工与参数。");
        return;
      }
      setIssued(data.keys ?? []);
      await load();
    } catch {
      setError("网络错误，密钥未签发。");
    } finally {
      setBusy(false);
    }
  }

  function startEdit(key: KeyRow) {
    setError("");
    setEditing(key.id);
    setDraft({
      rpm: key.limits.rpm ? String(key.limits.rpm) : "",
      tpm: key.limits.tpm ? String(key.limits.tpm) : "",
      itpm: key.limits.itpm ? String(key.limits.itpm) : "",
      otpm: key.limits.otpm ? String(key.limits.otpm) : "",
      concurrency: key.limits.concurrency ? String(key.limits.concurrency) : "",
    });
  }

  async function saveLimits(id: string) {
    setError("");
    const payload: Record<string, number> = {};
    for (const dim of LIMIT_DIMS) {
      const value = draft[dim].trim();
      payload[dim] = value === "" ? 0 : Number(value);
    }
    const response = await fetch(`/api/admin/keys/${id}/limits`, {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload),
    });
    if (!response.ok) {
      setError("更新限额失败，请检查数值（0 表示不限）。");
      return;
    }
    setEditing(null);
    await load();
  }

  async function revoke(id: string) {
    setError("");
    const response = await fetch(`/api/admin/keys/${id}/revoke`, { method: "POST" });
    if (!response.ok) {
      setError("撤销失败。");
      return;
    }
    await load();
  }

  return (
    <>
      <section className="card panel">
        <div className="panel-head">
          <div>
            <h2 className="section-title">签发虚拟密钥</h2>
            <p className="section-sub">明文只显示一次；库里只存 SHA-256 与首尾指纹。</p>
          </div>
          <span className="tag-warn">R-03 · 之后无法再读取</span>
        </div>

        {error ? <div className="alert error" role="alert">{error}</div> : null}
        {issued.length > 0 ? (
          <div className="alert warn" role="status">
            <strong>已签发 {issued.length} 把密钥，请立即保存。</strong>
            {issued.map((key) => (
              <div className="key-plain" key={key.id}>{key.plaintext}</div>
            ))}
            <button className="btn btn-sm" type="button" style={{ marginTop: 8 }} onClick={() => setIssued([])}>
              我已保存
            </button>
          </div>
        ) : null}

        <form onSubmit={issue}>
          <div className="form-row">
            <div className="field">
              <label htmlFor="emp">员工</label>
              <select id="emp" value={empId} onChange={(event) => setEmpId(event.target.value)} required>
                <option value="">选择员工</option>
                {users.map((user) => (
                  <option value={user.id} key={user.id}>
                    {user.displayName} ({user.account})
                  </option>
                ))}
              </select>
            </div>
            <div className="field" style={{ maxWidth: 140 }}>
              <label htmlFor="count">数量</label>
              <input
                id="count"
                type="number"
                min={1}
                max={100}
                value={count}
                onChange={(event) => setCount(Number(event.target.value))}
                required
              />
            </div>
            <div className="field" style={{ flex: 2 }}>
              <label htmlFor="models">允许的模型（逗号分隔；留空 = 不限）</label>
              <input
                id="models"
                value={modelsText}
                onChange={(event) => setModelsText(event.target.value)}
                placeholder="claude-sonnet-4.5, gpt-5.1"
              />
            </div>
          </div>
          <button className="btn btn-primary" type="submit" disabled={busy || !empId || !loaded}>
            <Icon name="key" size={14} />{busy ? "签发中…" : "签发"}
          </button>
        </form>
      </section>

      <section className="card panel">
        <div className="panel-head">
          <div>
            <h2 className="section-title">密钥</h2>
            <p className="section-sub">最新 200 条；撤销后立即拒绝。</p>
          </div>
          <span className="pill">{keys.length} 把</span>
        </div>
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th scope="col">指纹</th>
                <th scope="col">员工</th>
                <th scope="col">来源</th>
                <th scope="col">允许的模型</th>
                <th scope="col">限额</th>
                <th scope="col">状态</th>
                <th scope="col">创建时间</th>
                <th scope="col">操作</th>
              </tr>
            </thead>
            <tbody>
              {keys.map((key) => (
                <tr key={key.id}>
                  <td className="mono">{key.prefix4}…{key.fp6}</td>
                  <td>{key.emp.displayName}（{key.emp.account}）</td>
                  <td>
                    {key.distribution ? (
                      <span title={key.label || undefined}>{key.distribution.name}</span>
                    ) : (
                      <span style={{ color: "var(--muted)" }}>{key.label || "直接签发"}</span>
                    )}
                  </td>
                  <td className="mono">{key.allowedModels.length > 0 ? key.allowedModels.join(", ") : "不限"}</td>
                  <td className="mono">{formatLimits(key.limits)}</td>
                  <td>
                    <span className={`status ${key.revokedAt ? "error" : "success"}`}>
                      {key.revokedAt ? "已撤销" : "正常"}
                    </span>
                  </td>
                  <td className="mono">{new Date(key.createdAt).toLocaleString("zh-CN")}</td>
                  <td>
                    <div className="row-actions">
                      <button
                        className="btn btn-sm"
                        type="button"
                        disabled={Boolean(key.revokedAt)}
                        onClick={() => (editing === key.id ? setEditing(null) : startEdit(key))}
                      >
                        限额
                      </button>
                      <button
                        className="btn btn-sm"
                        type="button"
                        disabled={Boolean(key.revokedAt)}
                        onClick={() => void revoke(key.id)}
                      >
                        撤销
                      </button>
                    </div>
                  </td>
                </tr>
              ))}
              {editing ? (
                <tr>
                  <td colSpan={8}>
                    <div className="form-row" style={{ alignItems: "flex-end" }}>
                      {LIMIT_DIMS.map((dim) => (
                        <div className="field" key={dim} style={{ maxWidth: 160 }}>
                          <label htmlFor={`limit-${dim}`}>{DIM_LABELS[dim] ?? dim.toUpperCase()}</label>
                          <input
                            id={`limit-${dim}`}
                            type="number"
                            min={0}
                            value={draft[dim]}
                            onChange={(event) => setDraft({ ...draft, [dim]: event.target.value })}
                            placeholder="0 = 不限"
                          />
                        </div>
                      ))}
                      <button className="btn btn-primary btn-sm" type="button" onClick={() => void saveLimits(editing)}>
                        保存
                      </button>
                      <button className="btn btn-sm" type="button" onClick={() => setEditing(null)}>
                        取消
                      </button>
                    </div>
                  </td>
                </tr>
              ) : null}
              {loaded && keys.length === 0 ? (
                <tr><td colSpan={8} style={{ color: "var(--muted)" }}>还没有密钥。</td></tr>
              ) : null}
            </tbody>
          </table>
        </div>
      </section>
    </>
  );
}
