"use client";

import { useEffect, useRef, useState } from "react";

import { BrandTile } from "./BrandMark";
import { landingHref } from "./nav";

const MESSAGES: Record<string, string> = {
  invalid_credentials: "账号或密码不正确。",
  account_locked: "失败次数过多，账号已被临时锁定。",
  invalid_request: "请输入账号和密码。",
  invalid_session: "登录已过期，请重新登录。",
};

export default function LoginForm({ notice }: { notice?: "session_revoked" }) {
  const [account, setAccount] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const passwordRef = useRef<HTMLInputElement | null>(null);
  const cleared = useRef(false);

  // 会话已被服务端撤销（停用 / 口令变更）时，cookie 还在浏览器里但已经无效。
  // RSC 不能写 cookie，所以在这里最佳努力清一次：POST /api/auth/logout 不要求有
  // 有效库会话，只要同源就会把 cookie 置空。失败静默忽略——清不掉也不影响下面的登录。
  // 用 ref 兜住，保证 StrictMode 双挂载或重渲染下只发一次请求，不会变成请求风暴。
  useEffect(() => {
    if (notice !== "session_revoked" || cleared.current) return;
    cleared.current = true;
    void fetch("/api/auth/logout", { method: "POST" }).catch(() => {
      /* 清 cookie 是最佳努力，失败不打扰用户 */
    });
  }, [notice]);

  async function submit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setError("");
    setLoading(true);
    try {
      const response = await fetch("/api/auth/login", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ account, password }),
      });
      const data = (await response.json()) as {
        user?: { role?: string };
        error?: { code?: string; retry_after?: number };
      };
      if (!response.ok) {
        const code = data.error?.code ?? "invalid_credentials";
        const retry = data.error?.retry_after ? ` 请 ${data.error.retry_after} 秒后再试。` : "";
        setError((MESSAGES[code] ?? "登录失败，请重试。") + retry);
        passwordRef.current?.focus();
        return;
      }
      // 登录后按角色落到各自第一个页面："/" 已不再是页面，直接跳过去只会多一次重定向。
      window.location.assign(landingHref(data.user?.role ?? "employee"));
    } catch {
      setError("网络错误：控制台不可达，请检查服务是否已启动。");
    } finally {
      setLoading(false);
    }
  }

  return (
    <main className="login-page">
      <div className="login-body">
        <BrandTile size={56} />
        <h1 className="login-title">欢迎使用 KeyKeep</h1>
        <p className="login-sub">给老板：接上游、管成员、发 Key、看用量。<br />给员工：拿到自己的接口和密钥就能用。</p>

        {notice === "session_revoked" ? (
          <p className="alert warn" role="status">
            登录状态已失效（账号被停用或密码已变更），请重新登录。
          </p>
        ) : null}

        {error ? (
          <p className="login-error" role="alert" aria-live="polite">{error}</p>
        ) : null}

        <form className="login-form" onSubmit={submit}>
          <input
            aria-label="账号"
            name="account"
            placeholder="账号"
            autoComplete="username"
            autoFocus
            value={account}
            onChange={(event) => setAccount(event.target.value)}
            required
          />
          <input
            ref={passwordRef}
            aria-label="密码"
            name="password"
            type="password"
            placeholder="密码"
            autoComplete="current-password"
            value={password}
            onChange={(event) => setPassword(event.target.value)}
            required
          />
          <button className="login-submit" type="submit" disabled={loading} aria-busy={loading}>
            {loading ? "登录中…" : "登录"}
          </button>
        </form>

        <p className="login-hint">没有账号？找老板开通；已经有 Key 的员工，登录后到「我的接口」查看接入方式。</p>
      </div>

      <p className="login-legal">继续即表示你同意公司的内部使用与隐私规范。</p>
    </main>
  );
}
