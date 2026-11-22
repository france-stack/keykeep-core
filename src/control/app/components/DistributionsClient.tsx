"use client";

import { Fragment, useCallback, useEffect, useState } from "react";

import { Icon } from "./Icon";

type UserRow = {
  id: string;
  account: string;
  displayName: string;
  disabled: boolean;
};

type DistributionRow = {
  id: string;
  name: string;
  description: string;
  allowedModels: string[];
  disabled: boolean;
  revokedAt: string | null;
  createdAt: string;
  assignedEmployees: number;
  activeKeys: number;
};

type AssignedKey = {
  id: string;
  fingerprint: string;
  label: string;
  allowedModels: string[];
  modelsText: string;
  emp: { id: string; account: string; displayName: string; disabled: boolean };
  revokedAt: string | null;
  createdAt: string;
};

type Issued = { id: string; plaintext: string; prefix4: string; fp6: string };

function statusOf(row: DistributionRow): { text: string; cls: string } {
  if (row.revokedAt) return { text: "已撤销", cls: "error" };
  if (row.disabled) return { text: "已停用", cls: "limited" };
  return { text: "可用", cls: "success" };
}

function modelsText(models: string[]): string {
  return models.length > 0 ? models.join(", ") : "不限";
}

function splitModels(text: string): string[] {
  return text
    .split(/[,，\s]+/)
    .map((value) => value.trim())
    .filter(Boolean);
}

const ERROR_TEXT: Record<string, string> = {
  conflict: "同名分发 API 已存在，换一个名字。",
  invalid_name: "名称不能为空。",
  name_too_long: "名称不能超过 64 个字符。",
  description_too_long: "说明不能超过 500 个字符。",
  distribution_not_assignable: "该分发 API 已停用或已撤销，不能再分配。",
  forbidden: "你没有权限执行这个操作。",
  license_read_only: "授权处于只读状态，暂时不能修改分发。",
  not_found: "分发 API 不存在，可能已被删除。",
};

export default function DistributionsClient() {
  const [users, setUsers] = useState<UserRow[]>([]);
  const [rows, setRows] = useState<DistributionRow[]>([]);
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [busy, setBusy] = useState(false);

  // 新建表单
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [models, setModels] = useState("");

  // 分配表单：一次只展开一个分发
  const [assigning, setAssigning] = useState<string | null>(null);
  const [assignEmpId, setAssignEmpId] = useState("");
  const [assignCount, setAssignCount] = useState(1);
  const [assignModels, setAssignModels] = useState("");
  const [assignLabel, setAssignLabel] = useState("");
  const [issued, setIssued] = useState<Issued[]>([]);

  // 详情
  const [detailId, setDetailId] = useState<string | null>(null);
  const [detailKeys, setDetailKeys] = useState<AssignedKey[]>([]);

  const load = useCallback(async () => {
    const [usersResponse, rowsResponse] = await Promise.all([
      fetch("/api/admin/users"),
      fetch("/api/admin/distributions"),
    ]);
    if (usersResponse.status === 401 || rowsResponse.status === 401) {
      window.location.assign("/login");
      return;
    }
    if (usersResponse.ok) {
      const data = (await usersResponse.json()) as { users: UserRow[] };
      setUsers(data.users.filter((user) => !user.disabled && user.id));
    }
    if (rowsResponse.ok) {
      const data = (await rowsResponse.json()) as { distributions: DistributionRow[] };
      setRows(data.distributions);
    }
    setLoaded(true);
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  async function create(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setError("");
    setNotice("");
    setIssued([]);
    setBusy(true);
    try {
      const response = await fetch("/api/admin/distributions", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ name, description, allowedModels: splitModels(models) }),
      });
      const data = (await response.json()) as { error?: { code?: string } };
      if (!response.ok) {
        setError(ERROR_TEXT[data.error?.code ?? ""] ?? "创建分发 API 失败。");
        return;
      }
      setName("");
      setDescription("");
      setModels("");
      setNotice("分发 API 已创建，接下来把它分配给员工。");
      await load();
    } catch {
      setError("网络错误，分发 API 未创建。");
    } finally {
      setBusy(false);
    }
  }

  function startAssign(row: DistributionRow) {
    setError("");
    setNotice("");
    setIssued([]);
    setAssigning(row.id);
    setAssignEmpId("");
    setAssignCount(1);
    // 预填分发上的默认名单：老板多数情况下就是按这个发出去。
    setAssignModels(row.allowedModels.join(", "));
    setAssignLabel("");
  }

  async function assign(row: DistributionRow) {
    setError("");
    setNotice("");
    setIssued([]);
    setBusy(true);
    try {
      const response = await fetch(`/api/admin/distributions/${row.id}/assign`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          empId: assignEmpId,
          count: Number(assignCount),
          allowedModels: splitModels(assignModels),
          label: assignLabel,
        }),
      });
      const data = (await response.json()) as {
        keys?: Issued[];
        error?: { code?: string };
      };
      if (!response.ok) {
        setError(ERROR_TEXT[data.error?.code ?? ""] ?? "分配失败，请检查员工与参数。");
        return;
      }
      setIssued(data.keys ?? []);
      setAssigning(null);
      setNotice("已分配，请把下面的明文密钥交给该员工（只显示一次）。");
      await load();
    } catch {
      setError("网络错误，密钥未签发。");
    } finally {
      setBusy(false);
    }
  }

  async function toggleDisabled(row: DistributionRow) {
    setError("");
    setNotice("");
    const response = await fetch(`/api/admin/distributions/${row.id}`, {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ disabled: !row.disabled }),
    });
    if (!response.ok) {
      setError("更新分发 API 失败。");
      return;
    }
    await load();
  }

  async function revoke(row: DistributionRow) {
    setError("");
    setNotice("");
    if (!window.confirm(`撤销「${row.name}」会同时撤销它下面 ${row.activeKeys} 把仍在用的密钥，员工会立即无法调用。继续？`)) {
      return;
    }
    const response = await fetch(`/api/admin/distributions/${row.id}/revoke`, { method: "POST" });
    const data = (await response.json()) as { revokedKeys?: number; error?: { code?: string } };
    if (!response.ok) {
      setError(ERROR_TEXT[data.error?.code ?? ""] ?? "撤销失败。");
      return;
    }
    setNotice(`已撤销，同时作废 ${data.revokedKeys ?? 0} 把密钥。`);
    await load();
  }

  async function openDetail(row: DistributionRow) {
    setError("");
    if (detailId === row.id) {
      setDetailId(null);
      return;
    }
    const response = await fetch(`/api/admin/distributions/${row.id}`);
    if (!response.ok) {
      setError("读取分发详情失败。");
      return;
    }
    const data = (await response.json()) as { keys: AssignedKey[] };
    setDetailKeys(data.keys);
    setDetailId(row.id);
  }

  return (
    <>
      {error ? <div className="alert error" role="alert">{error}</div> : null}
      {notice ? <div className="alert ok" role="status">{notice}</div> : null}
      {issued.length > 0 ? (
        <div className="alert warn" role="status">
          <strong>已签发 {issued.length} 把密钥，明文只显示这一次，请立即交给员工保存。</strong>
          {issued.map((key) => (
            <div className="key-plain" key={key.id}>{key.plaintext}</div>
          ))}
          <button className="btn btn-sm" type="button" style={{ marginTop: 8 }} onClick={() => setIssued([])}>
            我已保存
          </button>
        </div>
      ) : null}

      <section className="card panel">
        <div className="panel-head">
          <div>
            <h2 className="section-title">新建分发 API</h2>
            <p className="section-sub">
              一个分发 API = 一组可用模型。分发出去时在员工名下生成虚拟密钥，员工只能用它调用这些模型。
            </p>
          </div>
          <span className="pill">{rows.length} 个</span>
        </div>

        <form onSubmit={create}>
          <div className="form-row">
            <div className="field" style={{ flex: 1 }}>
              <label htmlFor="dist-name">名称</label>
              <input
                id="dist-name"
                value={name}
                onChange={(event) => setName(event.target.value)}
                placeholder="研发-DeepSeek"
                maxLength={64}
                required
              />
            </div>
            <div className="field" style={{ flex: 2 }}>
              <label htmlFor="dist-models">可用模型（逗号分隔；留空 = 不限）</label>
              <input
                id="dist-models"
                value={models}
                onChange={(event) => setModels(event.target.value)}
                placeholder="deepseek-chat, deepseek-reasoner"
              />
            </div>
          </div>
          <div className="field">
            <label htmlFor="dist-desc">说明（可选）</label>
            <input
              id="dist-desc"
              value={description}
              onChange={(event) => setDescription(event.target.value)}
              placeholder="给研发同事日常编码用"
              maxLength={500}
            />
          </div>
          <button className="btn btn-primary" type="submit" disabled={busy || !name.trim()}>
            <Icon name="share" size={14} />{busy ? "创建中…" : "创建"}
          </button>
        </form>
      </section>

      <section className="card panel">
        <div className="panel-head">
          <div>
            <h2 className="section-title">分发 API</h2>
            <p className="section-sub">
              撤销会连带作废它下面所有仍在用的密钥；停用只是不允许再分配，已发出的密钥仍可用。
            </p>
          </div>
        </div>
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th scope="col">名称</th>
                <th scope="col">可用模型</th>
                <th scope="col">已分配给</th>
                <th scope="col">在用密钥</th>
                <th scope="col">状态</th>
                <th scope="col">操作</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((row) => {
                const status = statusOf(row);
                const assignable = !row.revokedAt;
                return (
                  <Fragment key={row.id}>
                    <tr>
                      <td>
                        <strong>{row.name}</strong>
                        {row.description ? (
                          <div style={{ color: "var(--muted)", fontSize: 11 }}>{row.description}</div>
                        ) : null}
                      </td>
                      <td className="mono">{modelsText(row.allowedModels)}</td>
                      <td className="mono">{row.assignedEmployees} 人</td>
                      <td className="mono">{row.activeKeys} 把</td>
                      <td>
                        <span className={`status ${status.cls}`}>{status.text}</span>
                      </td>
                      <td>
                        <div className="row-actions">
                          <button
                            className="btn btn-sm"
                            type="button"
                            disabled={!assignable || busy}
                            onClick={() => (assigning === row.id ? setAssigning(null) : startAssign(row))}
                          >
                            分配给员工
                          </button>
                          <button className="btn btn-sm" type="button" onClick={() => void openDetail(row)}>
                            {detailId === row.id ? "收起" : "详情"}
                          </button>
                          <button
                            className="btn btn-sm"
                            type="button"
                            disabled={Boolean(row.revokedAt)}
                            onClick={() => void toggleDisabled(row)}
                          >
                            {row.disabled ? "启用" : "停用"}
                          </button>
                          <button
                            className="btn btn-sm"
                            type="button"
                            disabled={Boolean(row.revokedAt)}
                            onClick={() => void revoke(row)}
                          >
                            撤销
                          </button>
                        </div>
                      </td>
                    </tr>

                    {assigning === row.id ? (
                      <tr>
                        <td colSpan={6}>
                          <div className="form-row" style={{ alignItems: "flex-end" }}>
                            <div className="field" style={{ flex: 1 }}>
                              <label htmlFor={`assign-emp-${row.id}`}>员工</label>
                              <select
                                id={`assign-emp-${row.id}`}
                                value={assignEmpId}
                                onChange={(event) => setAssignEmpId(event.target.value)}
                                required
                              >
                                <option value="">选择员工</option>
                                {users.map((user) => (
                                  <option value={user.id} key={user.id}>
                                    {user.displayName} ({user.account})
                                  </option>
                                ))}
                              </select>
                            </div>
                            <div className="field" style={{ maxWidth: 120 }}>
                              <label htmlFor={`assign-count-${row.id}`}>数量</label>
                              <input
                                id={`assign-count-${row.id}`}
                                type="number"
                                min={1}
                                max={100}
                                value={assignCount}
                                onChange={(event) => setAssignCount(Number(event.target.value))}
                              />
                            </div>
                            <div className="field" style={{ flex: 2 }}>
                              <label htmlFor={`assign-models-${row.id}`}>
                                该员工可用的模型（默认沿用分发上的名单；留空 = 不限）
                              </label>
                              <input
                                id={`assign-models-${row.id}`}
                                value={assignModels}
                                onChange={(event) => setAssignModels(event.target.value)}
                                placeholder={modelsText(row.allowedModels)}
                              />
                            </div>
                            <div className="field" style={{ maxWidth: 200 }}>
                              <label htmlFor={`assign-label-${row.id}`}>备注（可选）</label>
                              <input
                                id={`assign-label-${row.id}`}
                                value={assignLabel}
                                onChange={(event) => setAssignLabel(event.target.value)}
                                placeholder="张三的笔记本"
                                maxLength={64}
                              />
                            </div>
                            <button
                              className="btn btn-primary btn-sm"
                              type="button"
                              disabled={busy || !assignEmpId}
                              onClick={() => void assign(row)}
                            >
                              {busy ? "分配中…" : "确认分配"}
                            </button>
                            <button className="btn btn-sm" type="button" onClick={() => setAssigning(null)}>
                              取消
                            </button>
                          </div>
                        </td>
                      </tr>
                    ) : null}

                    {detailId === row.id ? (
                      <tr>
                        <td colSpan={6}>
                          {detailKeys.length === 0 ? (
                            <p className="empty">这个分发 API 还没有分配给任何员工。</p>
                          ) : (
                            <div className="table-wrap">
                              <table>
                                <thead>
                                  <tr>
                                    <th scope="col">员工</th>
                                    <th scope="col">指纹</th>
                                    <th scope="col">备注</th>
                                    <th scope="col">该员工可用模型</th>
                                    <th scope="col">状态</th>
                                    <th scope="col">签发时间</th>
                                  </tr>
                                </thead>
                                <tbody>
                                  {detailKeys.map((key) => (
                                    <tr key={key.id}>
                                      <td>
                                        {key.emp.displayName}（{key.emp.account}）
                                        {key.emp.disabled ? (
                                          <span className="status error" style={{ marginLeft: 6 }}>员工已停用</span>
                                        ) : null}
                                      </td>
                                      <td className="mono">{key.fingerprint}</td>
                                      <td>{key.label || <span style={{ color: "var(--muted)" }}>—</span>}</td>
                                      <td className="mono">{key.modelsText}</td>
                                      <td>
                                        <span className={`status ${key.revokedAt ? "error" : "success"}`}>
                                          {key.revokedAt ? "已撤销" : "可用"}
                                        </span>
                                      </td>
                                      <td className="mono">{new Date(key.createdAt).toLocaleString("zh-CN")}</td>
                                    </tr>
                                  ))}
                                </tbody>
                              </table>
                            </div>
                          )}
                        </td>
                      </tr>
                    ) : null}
                  </Fragment>
                );
              })}
              {loaded && rows.length === 0 ? (
                <tr>
                  <td colSpan={6} style={{ color: "var(--muted)" }}>
                    还没有分发 API。先在上面创建一个，再分配给员工。
                  </td>
                </tr>
              ) : null}
            </tbody>
          </table>
        </div>
      </section>
    </>
  );
}
