"use client";

import { useRouter } from "next/navigation";
import { useEffect, useState } from "react";

import { roleLabel } from "./nav";
import { SegmentedTabs } from "./SegmentedTabs";
import { useTheme } from "./useTheme";

const MOTION_KEY = "kk-motion";

export default function SettingsClient({
  role,
  gatewayUrl,
  version,
}: {
  role: string;
  gatewayUrl: string;
  version: string;
}) {
  const { theme, setTheme } = useTheme();
  const router = useRouter();
  const [motion, setMotion] = useState<"full" | "reduced">("full");
  const [systemReduced, setSystemReduced] = useState(false);
  const [signingOut, setSigningOut] = useState(false);
  const isAdmin = role === "admin";

  useEffect(() => {
    const saved = window.localStorage.getItem(MOTION_KEY);
    const next = saved === "reduced" ? "reduced" : "full";
    setMotion(next);
    if (next === "reduced") document.documentElement.dataset.motion = "reduced";
    const query = window.matchMedia("(prefers-reduced-motion: reduce)");
    setSystemReduced(query.matches);
    const onChange = () => setSystemReduced(query.matches);
    query.addEventListener("change", onChange);
    return () => query.removeEventListener("change", onChange);
  }, []);

  function applyMotion(next: "full" | "reduced") {
    setMotion(next);
    window.localStorage.setItem(MOTION_KEY, next);
    if (next === "reduced") document.documentElement.dataset.motion = "reduced";
    else delete document.documentElement.dataset.motion;
  }

  async function logout() {
    setSigningOut(true);
    try {
      await fetch("/api/auth/logout", { method: "POST" });
      router.push("/login");
      router.refresh();
    } finally {
      setSigningOut(false);
    }
  }

  let host = gatewayUrl;
  let port = "";
  try {
    const url = new URL(gatewayUrl);
    host = url.hostname;
    port = url.port || (url.protocol === "https:" ? "443" : "80");
  } catch {
    // keep the original string
  }

  return (
    <div className="page">
      <div className="page-head">
        <div>
          <h1 className="page-title">设置</h1>
          <p className="page-sub">外观、动效、账号与运行信息；改动立即生效。</p>
        </div>
      </div>

      <section className="card panel">
        <div className="panel-head">
          <div>
            <h2 className="panel-title">外观</h2>
            <p className="panel-sub">主题同时作用于顶栏和所有页面。</p>
          </div>
        </div>
        <div className="setting-row">
          <div>
            <strong>主题</strong>
            <p>默认深色；需要时在这里调亮，只保存在这个浏览器里。</p>
          </div>
          <SegmentedTabs
            label="主题"
            value={theme}
            onChange={(id) => setTheme(id as "light" | "dark")}
            items={[{ id: "dark", label: "深色" }, { id: "light", label: "调亮" }]}
          />
        </div>
        <div className="setting-row">
          <div>
            <strong>界面语言</strong>
            <p>当前仅提供简体中文；多语言在后续版本加入。</p>
          </div>
          <select className="setting-select" value="zh-CN" disabled aria-label="界面语言">
            <option value="zh-CN">简体中文</option>
          </select>
        </div>
      </section>

      <section className="card panel">
        <div className="panel-head">
          <div>
            <h2 className="panel-title">动效</h2>
            <p className="panel-sub">
              系统当前{motion === "reduced" || systemReduced ? "要求减少动效" : "允许播放动效"}；
              {systemReduced ? "操作系统的偏好优先" : "你可以在这里覆盖"}。
            </p>
          </div>
        </div>
        <div className="setting-row">
          <div>
            <strong>页面动效</strong>
            <p>页面进入、导航高亮、骨架屏与图表增长。</p>
          </div>
          <SegmentedTabs
            label="动效"
            value={motion}
            onChange={(id) => applyMotion(id as "full" | "reduced")}
            items={[{ id: "full", label: "完整" }, { id: "reduced", label: "精简" }]}
          />
        </div>
      </section>

      <section className="card panel">
        <div className="panel-head">
          <div>
            <h2 className="panel-title">账号</h2>
            <p className="panel-sub">当前会话就登录在这个身份下。</p>
          </div>
        </div>
        <div className="setting-row">
          <div>
            <strong>{roleLabel(role)}</strong>
            <p>退出后需要重新登录才能访问控制台。</p>
          </div>
          <button className="btn btn-sm" type="button" onClick={logout} disabled={signingOut}>
            {signingOut ? "退出中…" : "退出登录"}
          </button>
        </div>
      </section>

      <section className="card panel">
        <div className="panel-head">
          <div>
            <h2 className="panel-title">运行信息</h2>
            <p className="panel-sub">
              {isAdmin
                ? "只读。「接口配置」表示已配置地址，不是实时探测结果。"
                : "只读。员工侧只需要知道自己的接口地址，见「我的接口」。"}
            </p>
          </div>
        </div>
        <dl className="kv-list">
          <div><dt>当前身份</dt><dd>{roleLabel(role)}</dd></div>
          <div><dt>接口地址</dt><dd className="mono">{host}{port ? `:${port}` : ""}</dd></div>
          <div>
            <dt>接口配置</dt>
            <dd>
              <span className={`status ${gatewayUrl ? "success" : "error"}`}>
                {gatewayUrl ? "已配置" : "未配置"}
              </span>
            </dd>
          </div>
          <div><dt>控制台版本</dt><dd className="mono">v{version}</dd></div>
          {isAdmin ? (
            <div><dt>界面语言</dt><dd>简体中文</dd></div>
          ) : null}
        </dl>
      </section>
    </div>
  );
}
