"use client";

import { useCallback, useEffect, useState } from "react";

import { Icon } from "./Icon";
import { roleLabel } from "./nav";
import { Skeleton } from "./Skeleton";

type Session = { account: string; displayName: string; role: string };

type KeyRow = {
  id: string;
  fingerprint: string;
  label: string;
  distribution: { id: string; name: string } | null;
  allowedModels: string[];
  limits: Record<string, number>;
  revokedAt: string | null;
  createdAt: string;
};

type Payload = { account: string; gatewayPath: string; keys: KeyRow[] };

const DIM_LABELS: Record<string, string> = {
  rpm: "RPM",
  tpm: "TPM",
  itpm: "ITPM",
  otpm: "OTPM",
  concurrency: "并发",
};

function formatLimits(limits: Record<string, number>): string {
  const parts = Object.entries(limits)
    .filter(([, value]) => value > 0)
    .map(([dim, value]) => `${DIM_LABELS[dim] ?? dim.toUpperCase()} ${value}`);
  return parts.length > 0 ? parts.join(" · ") : "不限";
}

/**
 * 拼出员工真正能填进客户端的地址。
 *
 * 优先用部署方给的公网网关地址；没配时按「浏览器当前访问的主机 + 网关端口」
 * 推断——开发机上控制台在 :3000、网关在 :8080，员工照抄 127.0.0.1 只会在自己
 * 电脑上撞空。
 */
function publicBase(gatewayUrl: string, fallbackPort: string): string {
  if (gatewayUrl) {
    try {
      return new URL(gatewayUrl).origin;
    } catch {
      return gatewayUrl;
    }
  }
  if (typeof window === "undefined") return "";
  const { protocol, hostname } = window.location;
  return `${protocol}//${hostname}:${fallbackPort}`;
}

// trimOrigin 归一化成一个 origin（去掉路径与尾斜杠）。
function trimOrigin(value: string): string {
  try {
    return new URL(value).origin;
  } catch {
    return value.replace(/\/+$/, "");
  }
}

function Copyable({ label, value, hint }: { label: string; value: string; hint?: string }) {
  const [copied, setCopied] = useState(false);

  async function copy() {
    try {
      await navigator.clipboard.writeText(value);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1600);
    } catch {
      setCopied(false);
    }
  }

  return (
    <div className="copy-row">
      <div className="copy-body">
        <span className="copy-label">{label}</span>
        <code className="copy-value mono">{value}</code>
        {hint ? <span className="copy-hint">{hint}</span> : null}
      </div>
      <button type="button" className="btn btn-sm" onClick={() => void copy()}>
        {copied ? "已复制" : "复制"}
      </button>
    </div>
  );
}

export default function MyEndpointClient({
  session,
  gatewayUrl,
  gatewayPort,
  consoleUrl = "",
}: {
  session: Session;
  gatewayUrl: string;
  gatewayPort: string;
  consoleUrl?: string;
}) {
  const [data, setData] = useState<Payload | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  // 服务端渲染时不知道员工用什么域名访问，先留空；挂载后再按 window.location 补齐，
  // 避免 SSR 与浏览器算出两个不同的地址（hydration 报错）。
  const [origin, setOrigin] = useState("");

  const load = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      const response = await fetch("/api/me/keys");
      if (response.status === 401) {
        // 带上 reason：登录页据此不再按 cookie 签名把人送回本页（那会形成跳转死循环）。
        window.location.assign("/login?reason=session_revoked");
        return;
      }
      if (!response.ok) throw new Error("keys");
      setData((await response.json()) as Payload);
    } catch {
      setError("读取你的接口信息失败，请重试。");
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  useEffect(() => {
    setOrigin(publicBase(gatewayUrl, gatewayPort));
  }, [gatewayUrl, gatewayPort]);

  // Anthropic SDK 自己会拼 /v1/messages，所以 ANTHROPIC_BASE_URL 不能带 /v1；
  // OpenAI SDK 拼的是 /chat/completions，base_url 必须带 /v1。两者不能共用同一个值。
  const pending = "正在解析访问地址…";
  const anthropicBase = origin || pending;
  const openaiBase = origin ? `${origin}/v1` : pending;
  const active = (data?.keys ?? []).filter((key) => !key.revokedAt);
  const models = Array.from(new Set(active.flatMap((key) => key.allowedModels)));

  // 按「分发 API」归拢员工能用的东西：员工关心的是「老板给了我哪几条通道、
  // 每条能用什么模型」，而不是一把把互相独立的指纹。同一分发下多把 Key 合并成一行。
  const grants: Array<{ name: string; modelsText: string; keys: number }> = [];
  const grantIndex = new Map<string, number>();
  for (const key of active) {
    if (!key.distribution) continue;
    const allowed = key.allowedModels;
    const known = grantIndex.get(key.distribution.id);
    if (known === undefined) {
      grantIndex.set(key.distribution.id, grants.length);
      grants.push({ name: key.distribution.name, modelsText: allowed.length > 0 ? allowed.join(", ") : "不限", keys: 1 });
      continue;
    }
    const row = grants[known];
    row.keys += 1;
    // 同一分发下各把 Key 可能被单独改过白名单：取并集才是「我实际能调用的模型」。
    if (allowed.length === 0) {
      row.modelsText = "不限";
    } else if (row.modelsText !== "不限") {
      const merged = Array.from(new Set([...row.modelsText.split(", "), ...allowed]));
      row.modelsText = merged.join(", ");
    }
  }

  // 连接器要连的是老板的控制台（不是网关）：用它做 --server，网关地址由它自己推断/覆盖。
  //
  // 优先用部署方注入的对外地址（局域网模式下桌面版注入的是本机局域网 IP），
  // 否则退回浏览器当前 origin。只用 origin 的问题：老板在本机用
  // http://127.0.0.1:8443 打开页面时，页面上印出来的就是 127.0.0.1，
  // 员工照抄必然连不上自己的机器。
  const [controlOrigin, setControlOrigin] = useState("");
  useEffect(() => {
    setControlOrigin(consoleUrl ? trimOrigin(consoleUrl) : window.location.origin);
  }, [consoleUrl]);
  const server = controlOrigin || "https://<你的控制台地址>";

  return (
    <div className="page">
      <div className="page-head">
        <div>
          <h1 className="page-title">我的接口</h1>
          <p className="page-sub">
            装一个本机连接器，Claude Code、Codex、Gemini CLI 一键接公司网关；不想装也可以直接填下面的地址。
          </p>
        </div>
        <div className="page-actions">
          <button type="button" className="btn btn-sm" onClick={() => void load()} disabled={loading}>
            刷新
          </button>
        </div>
      </div>

      {error ? <div className="alert error" role="alert">{error}</div> : null}

      <section className="card panel">
        <div className="panel-head">
          <div>
            <h2 className="panel-title">第一步 · 装本机连接器</h2>
            <p className="panel-sub">
              连接器只在本机 127.0.0.1:3426 监听，把 Agent 的请求转给公司网关；厂商凭据只在老板机器上。
            </p>
          </div>
        </div>
        <div className="toolbar">
          <a className="btn btn-sm" href="/downloads/kkconnect-windows-amd64.exe" download>
            Windows
          </a>
          <a className="btn btn-sm" href="/downloads/kkconnect-darwin-arm64" download>
            macOS (Apple 芯片)
          </a>
          <a className="btn btn-sm" href="/downloads/kkconnect-linux-amd64" download>
            Linux x86_64
          </a>
        </div>
        <div className="alert warn" role="status">
          <strong>下载后按顺序跑三条命令（Key 从下面「我的密钥」拿明文，或找老板重新签发）：</strong>
          <pre className="copy-block">{`# 1. 登录（只做一次；Key 按提示粘贴，不会留在命令历史里）
kkconnect login --server ${server} --key kk_你的密钥

# 2. 启动本机连接器（保持运行）
kkconnect serve

# 3. 另开一个终端，接入 Agent（claude / codex / gemini 三选一）
kkconnect link claude`}</pre>
        </div>
        <p className="copy-hint">
          连接器启动后打开 <code className="mono">http://127.0.0.1:3426/</code> 能看到连接状态；
          <code className="mono">kkconnect unlink claude</code> 可随时还原配置。
        </p>
      </section>

      <section className="card panel">
        <div className="panel-head">
          <div>
            <h2 className="panel-title">不想装连接器 · 直接填地址</h2>
            <p className="panel-sub">
              当前身份：{session.displayName}（{roleLabel(session.role)} · {session.account}）
            </p>
          </div>
        </div>

        <Copyable
          label="Base URL（Anthropic 协议）"
          value={anthropicBase}
          hint="Claude Code 填 ANTHROPIC_BASE_URL（不要带 /v1）；SDK 自己会拼 /v1/messages。"
        />
        <Copyable
          label="Base URL（OpenAI 协议）"
          value={openaiBase}
          hint="DeepSeek / Cherry Studio / 任意 OpenAI SDK 填 base_url（要带 /v1）。"
        />
        <Copyable
          label="模型名"
          // 白名单为空 = 网关放行全部模型（policy/whitelist.go），所以这里不能说「未绑定模型」，
          // 否则和下方密钥表里的「不限」自相矛盾，员工会以为自己一把模型都没有。
          value={models.length > 0 ? models.join(", ") : "不限（全部模型可用）"}
          hint="留空表示不限，由老板按需限制。"
        />
        <Copyable
          label="API Key"
          value="kk_…（签发时只显示一次，见下方密钥列表）"
          hint="Key 明文不会存库，也不会再显示；忘记就找老板重新签发一把，旧的可以撤销。"
        />

        <div className="alert warn" role="status">
          <strong>两条环境变量写法，挑一种即可：</strong>
          <pre className="copy-block">{origin ? `# Anthropic 协议（Claude Code 等）
export ANTHROPIC_BASE_URL="${anthropicBase}"
export ANTHROPIC_API_KEY="kk_你的密钥"

# OpenAI 协议（DeepSeek 客户端、OpenAI SDK 等）
export OPENAI_BASE_URL="${openaiBase}"
export OPENAI_API_KEY="kk_你的密钥"` : "正在解析访问地址…"}</pre>
        </div>
      </section>

      <section className="card panel">
        <div className="panel-head">
          <div>
            <h2 className="panel-title">老板给我开的 API</h2>
            <p className="panel-sub">
              每条是你被分配到的分发 API 与它允许的模型；换工作范围找老板重新分配，不要自己改。
            </p>
          </div>
          <span className="pill">{grants.length} 条</span>
        </div>
        {loading ? (
          <div className="stack">
            <Skeleton height={16} />
            <Skeleton height={16} />
          </div>
        ) : grants.length === 0 ? (
          <p className="empty">
            还没有分到你名下的分发 API。老板在「分发 API」页分配后会出现在这里。
          </p>
        ) : (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th scope="col">分发 API</th>
                  <th scope="col">可用模型</th>
                  <th scope="col">密钥</th>
                  <th scope="col">状态</th>
                </tr>
              </thead>
              <tbody>
                {grants.map((grant) => (
                  <tr key={grant.name}>
                    <td>{grant.name}</td>
                    <td className="mono">{grant.modelsText}</td>
                    <td className="mono">{grant.keys} 把</td>
                    <td>
                      <span className="status success">可用</span>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>

      <section className="card panel">
        <div className="panel-head">
          <div>
            <h2 className="panel-title">我的密钥</h2>
            <p className="panel-sub">
              只显示指纹（前 4 位与后 6 位）；撤销后立即失效，不需要等任何缓存。
            </p>
          </div>
          <span className="pill">{active.length} 把可用</span>
        </div>

        {loading ? (
          <div className="stack">
            <Skeleton height={16} />
            <Skeleton height={16} />
            <Skeleton height={16} />
          </div>
        ) : (data?.keys ?? []).length === 0 ? (
          <p className="empty">
            你名下还没有密钥。找老板在「API 密钥」页给你签发一把，签发时会把明文给你一次。
          </p>
        ) : (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th scope="col">指纹</th>
                  <th scope="col">来源</th>
                  <th scope="col">可用模型</th>
                  <th scope="col">限额</th>
                  <th scope="col">状态</th>
                  <th scope="col">签发时间</th>
                </tr>
              </thead>
              <tbody>
                {(data?.keys ?? []).map((key) => (
                  <tr key={key.id}>
                    <td className="mono">{key.fingerprint}</td>
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
      </section>

      <section className="card panel">
        <div className="panel-head">
          <div>
            <h2 className="panel-title">还能做什么</h2>
            <p className="panel-sub">员工只有这两件事：拿接口、看自己用了多少。</p>
          </div>
        </div>
        <div className="toolbar">
          <a className="btn btn-sm" href="/me/usage">
            <Icon name="chart" size={13} /> 看我的用量
          </a>
          <a className="btn btn-sm" href="/me/settings">
            <Icon name="gear" size={13} /> 外观与动效
          </a>
        </div>
      </section>
    </div>
  );
}
