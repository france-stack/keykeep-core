"use client";

import { useEffect, useState } from "react";

import { Icon } from "./Icon";

type UserRow = {
  id: string;
  account: string;
  displayName: string;
  role: string;
  disabled: boolean;
  lockedUntil: string | null;
};

const ROLE_LABEL: Record<string, string> = {
  admin: "老板",
  employee: "员工",
};

export default function OrgClient() {
  const [users, setUsers] = useState<UserRow[]>([]);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [busy, setBusy] = useState(false);

  const [account, setAccount] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [newRole, setNewRole] = useState("employee");
  const [password, setPassword] = useState("");

  async function load() {
    const response = await fetch("/api/admin/users");
    if (response.status === 401) {
      window.location.assign("/login");
      return;
    }
    if (response.ok) {
      const data = (await response.json()) as { users: UserRow[] };
      setUsers(data.users);
    }
  }

  useEffect(() => {
    void load();
  }, []);

  async function addUser(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setError("");
    setNotice("");
    setBusy(true);
    try {
      const response = await fetch("/api/admin/users", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ account, displayName, role: newRole, password }),
      });
      const data = (await response.json()) as { error?: { code?: string; reason?: string } };
      if (!response.ok) {
        setError(
          data.error?.code === "conflict"
            ? "该账号已存在。"
            : data.error?.reason === "password_too_short"
              ? "口令至少 12 个字符。"
              : "创建成员失败。",
        );
        return;
      }
      setAccount("");
      setDisplayName("");
      setPassword("");
      setNotice("成员已创建。");
      await load();
    } finally {
      setBusy(false);
    }
  }

  async function patchUser(id: string, payload: { disabled?: boolean; password?: string }) {
    setError("");
    setNotice("");
    const response = await fetch(`/api/admin/users/${id}`, {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload),
    });
    if (!response.ok) {
      setError("更新失败。");
      return;
    }
    setNotice("已更新。");
    await load();
  }

  async function resetPassword(user: UserRow) {
    const next = window.prompt(`为 ${user.displayName} 设置新口令（至少 12 个字符）`);
    if (!next) return;
    await patchUser(user.id, { password: next });
  }

  return (
    <>
      {error ? <div className="alert error" role="alert">{error}</div> : null}
      {notice ? <div className="alert ok" role="status">{notice}</div> : null}

      <section className="card panel">
        <div className="panel-head">
          <div>
            <h2 className="section-title">成员</h2>
            <p className="section-sub">老板与员工、账号状态与口令重置。</p>
          </div>
          <span className="pill">{users.length} 人</span>
        </div>
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th scope="col">账号</th>
                <th scope="col">姓名</th>
                <th scope="col">角色</th>
                <th scope="col">状态</th>
                <th scope="col">操作</th>
              </tr>
            </thead>
            <tbody>
              {users.map((user) => (
                <tr key={user.id}>
                  <td className="mono">{user.account}</td>
                  <td>{user.displayName}</td>
                  <td>{ROLE_LABEL[user.role] ?? user.role}</td>
                  <td>
                    <span className={`status ${user.disabled ? "error" : user.lockedUntil ? "limited" : "success"}`}>
                      {user.disabled ? "已停用" : user.lockedUntil ? "已锁定" : "正常"}
                    </span>
                  </td>
                  <td>
                    <div className="row-actions">
                      <button
                        className="btn btn-sm"
                        type="button"
                        onClick={() => void patchUser(user.id, { disabled: !user.disabled })}
                      >
                        {user.disabled ? "启用" : "停用"}
                      </button>
                      <button className="btn btn-sm" type="button" onClick={() => void resetPassword(user)}>
                        重置口令
                      </button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </section>

      <section className="card panel">
        <div className="panel-head">
          <div>
            <h2 className="section-title">新建成员</h2>
            <p className="section-sub">口令至少 12 个字符，使用 Argon2id 存储。</p>
          </div>
        </div>
        <form onSubmit={addUser}>
          <div className="form-row">
            <div className="field">
              <label htmlFor="account">账号</label>
              <input id="account" value={account} onChange={(event) => setAccount(event.target.value)} required />
            </div>
            <div className="field">
              <label htmlFor="displayName">姓名</label>
              <input id="displayName" value={displayName} onChange={(event) => setDisplayName(event.target.value)} required />
            </div>
            <div className="field">
              <label htmlFor="newRole">角色</label>
              <select id="newRole" value={newRole} onChange={(event) => setNewRole(event.target.value)}>
                <option value="employee">员工</option>
                <option value="admin">老板</option>
              </select>
            </div>
            <div className="field">
              <label htmlFor="password">初始口令</label>
              <input
                id="password"
                type="password"
                minLength={12}
                value={password}
                onChange={(event) => setPassword(event.target.value)}
                required
              />
            </div>
          </div>
          <button className="btn btn-primary" type="submit" disabled={busy}>
            <Icon name="users" size={14} />创建成员
          </button>
        </form>
      </section>
    </>
  );
}
