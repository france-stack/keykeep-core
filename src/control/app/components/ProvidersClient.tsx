"use client";

import { useEffect, useMemo, useState } from "react";

import { describeTestStatus } from "@/lib/provider-test-status";

import { Icon } from "./Icon";
import { Skeleton } from "./Skeleton";
import {
  GROUP_LABEL,
  PROTOCOL_LABEL,
  catalogGroups,
  endpointFor,
  findCatalogEntry,
  protocolsOf,
  type CatalogEntry,
  type Protocol,
} from "@/lib/provider-catalog";
import { hasProviderIcon } from "@/lib/provider-icons";
import { iconTileKind } from "@/lib/provider-icon-contrast";

type Provider = {
  id: string;
  provider: string;
  name: string;
  providerType: string;
  baseUrl: string;
  authHeaderName: string;
  authScheme: string;
  keyFp6: string;
  disabled: boolean;
  createdAt: string;
  models: Array<{ alias: string; modelId: string }>;
};

type TestResult = { ok: boolean; status: number | null; latency_ms: number; error?: string };

type Draft = {
  entry: CatalogEntry;
  name: string;
  baseUrl: string;
  apiKey: string;
  models: string;
  authHeaderName: string;
  authScheme: string;
  /** 选中的协议；多端点条目切换协议时同时换 baseUrl */
  protocol: Protocol;
  /** 选中的区域（国内站 / 国际站）；空串表示条目自身的默认端点 */
  regionId: string;
};

function hostOf(url: string): string {
  try {
    return new URL(url).host;
  } catch {
    return url || "未配置端点";
  }
}

function parseModels(text: string): Array<{ alias: string; modelId: string }> {
  return text
    .split("\n")
    .map((line) => line.trim())
    .filter(Boolean)
    .map((line) => {
      const [alias, modelId] = line.split("=").map((part) => part.trim());
      return { alias: alias ?? "", modelId: modelId ?? "" };
    })
    .filter((model) => model.alias && model.modelId);
}

function toDraft(entry: CatalogEntry): Draft {
  const regionId = entry.regions?.[0]?.id ?? "";
  return {
    entry,
    name: entry.name,
    baseUrl: endpointFor(entry, entry.protocol, regionId),
    apiKey: "",
    models: (entry.modelsHint ?? []).map((model) => `${model}=${model}`).join("\n"),
    authHeaderName: entry.auth.header,
    authScheme: entry.auth.scheme,
    protocol: entry.protocol,
    regionId,
  };
}

/** 切换协议或区域时同步 baseUrl 与模型提示，不覆盖用户已手改的内容。 */
function retarget(draft: Draft, next: { protocol?: Protocol; regionId?: string }): Draft {
  const protocol = next.protocol ?? draft.protocol;
  const regionId = next.regionId ?? draft.regionId;
  const region = draft.entry.regions?.find((item) => item.id === regionId);
  const wasUntouched = draft.baseUrl === endpointFor(draft.entry, draft.protocol, draft.regionId);
  const hintUntouched = draft.models === (draft.entry.modelsHint ?? []).map((m) => `${m}=${m}`).join("\n");
  const modelsHint = region?.modelsHint ?? draft.entry.modelsHint ?? [];
  return {
    ...draft,
    protocol,
    regionId,
    baseUrl: wasUntouched ? endpointFor(draft.entry, protocol, regionId) : draft.baseUrl,
    models: hintUntouched ? modelsHint.map((model) => `${model}=${model}`).join("\n") : draft.models,
  };
}

function ProviderLogo({ entry, label }: { entry?: CatalogEntry; label: string }) {
  const [failed, setFailed] = useState(false);
  const tone = entry?.monogram.tone ?? "#6b7280";
  // 没有图标文件就不发请求：国内厂商与中转大多不在 Simple Icons / Iconify 里，
  // 硬发只会给控制台刷一串 404。
  const showImage = Boolean(entry && !failed && hasProviderIcon(entry.id));
  // 图标是厂商原件、不改配色，撞色的那些垫一层底片（清单由脚本生成，见 provider-icon-contrast.ts）
  const tile = showImage && entry ? iconTileKind(entry.id) : null;
  const tileClass = tile === "light" ? " provider-logo--on-light" : tile === "dark" ? " provider-logo--on-dark" : "";
  return (
    <span className={`provider-logo${tileClass}`} style={{ ["--tone" as string]: tone }}>
      {showImage && entry ? (
        <img
          src={`/provider-icons/${entry.id}.svg`}
          alt=""
          width={18}
          height={18}
          loading="lazy"
          onError={() => setFailed(true)}
        />
      ) : null}
      {!showImage ? <span className="provider-logo-text">{entry?.monogram.text ?? label.slice(0, 2).toUpperCase()}</span> : null}
    </span>
  );
}

export default function ProvidersClient() {
  const [providers, setProviders] = useState<Provider[]>([]);
  const [tests, setTests] = useState<Record<string, TestResult>>({});
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [busy, setBusy] = useState(false);
  const [query, setQuery] = useState("");
  const [catalogQuery, setCatalogQuery] = useState("");
  const [draft, setDraft] = useState<Draft | null>(null);
  const [expanded, setExpanded] = useState<string | null>(null);
  const [showDisabled, setShowDisabled] = useState(false);

  async function load() {
    const response = await fetch("/api/admin/providers");
    if (response.status === 401 || response.status === 403) {
      window.location.assign(response.status === 401 ? "/login" : "/");
      return;
    }
    if (response.ok) {
      const data = (await response.json()) as { providers: Provider[] };
      setProviders(data.providers);
    }
    setLoading(false);
  }

  useEffect(() => {
    void load();
  }, []);

  const active = providers.filter((provider) => !provider.disabled);
  const inactive = providers.filter((provider) => provider.disabled);

  const filteredActive = useMemo(() => {
    const keyword = query.trim().toLowerCase();
    if (!keyword) return active;
    return active.filter((provider) =>
      [provider.name, provider.provider, hostOf(provider.baseUrl)].join(" ").toLowerCase().includes(keyword),
    );
  }, [active, query]);

  const filteredInactive = useMemo(() => {
    const keyword = query.trim().toLowerCase();
    if (!keyword) return inactive;
    return inactive.filter((provider) =>
      [provider.name, provider.provider, hostOf(provider.baseUrl)].join(" ").toLowerCase().includes(keyword),
    );
  }, [inactive, query]);

  const groups = useMemo(() => {
    const keyword = catalogQuery.trim().toLowerCase();
    return catalogGroups().map((group) => ({
      ...group,
      entries: keyword
        ? group.entries.filter((entry) => `${entry.name} ${entry.id}`.toLowerCase().includes(keyword))
        : group.entries,
    }));
  }, [catalogQuery]);

  const configuredIds = new Set(providers.map((provider) => provider.provider));

  async function toggle(provider: Provider) {
    setError("");
    const response = await fetch(`/api/admin/providers/${provider.id}`, {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ disabled: !provider.disabled }),
    });
    if (!response.ok) {
      setError("更新失败，请重试。");
      return;
    }
    await load();
  }

  async function test(provider: Provider) {
    setTests((prev) => ({ ...prev, [provider.id]: { ok: false, status: null, latency_ms: 0, error: "testing" } }));
    const response = await fetch(`/api/admin/providers/${provider.id}/test`, { method: "POST" });
    const data = (await response.json()) as TestResult & { error?: { code?: string } };
    setTests((prev) => ({ ...prev, [provider.id]: data }));
  }

  function openDraft(entry: CatalogEntry) {
    if (entry.status === "unsupported") return;
    setError("");
    setNotice("");
    setDraft(toDraft(entry));
  }

  async function save(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!draft) return;
    setError("");
    setNotice("");
    setBusy(true);
    try {
      const response = await fetch("/api/admin/providers", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          catalogId: draft.entry.id,
          protocol: draft.protocol,
          name: draft.name.trim(),
          baseUrl: draft.baseUrl.trim(),
          apiKey: draft.apiKey,
          authHeaderName: draft.authHeaderName,
          authScheme: draft.authScheme,
          models: parseModels(draft.models),
        }),
      });
      const data = (await response.json()) as { error?: { code?: string } };
      if (!response.ok) {
        const code = data.error?.code;
        setError(
          code === "kek_not_configured"
            ? "未配置 KEYKEEP_KEK（32 字节的 base64），无法加密凭据。"
            : code === "base_url_required"
              ? "这家供应商需要填写 Base URL。"
              : code === "provider_unsupported"
                ? "这家供应商暂不支持。"
                : "保存失败，请检查名称、Base URL 和密钥。",
        );
        return;
      }
      setDraft(null);
      setNotice(`${draft.entry.name} 已保存并加密。`);
      await load();
    } finally {
      setBusy(false);
    }
  }

  function renderProvider(provider: Provider) {
    const entry = findCatalogEntry(provider.provider);
    const result = tests[provider.id];
    const testStatus = describeTestStatus(result);
    const open = expanded === provider.id;
    return (
      <div className={`provider-row${provider.disabled ? " off" : ""}`} key={provider.id}>
        <ProviderLogo entry={entry} label={provider.name} />
        <button type="button" className="provider-main" onClick={() => setExpanded(open ? null : provider.id)}>
          <strong>{provider.name}</strong>
          <small>
            {entry?.name ?? provider.provider} · {hostOf(provider.baseUrl)} · {provider.models.length} 个模型
            {provider.keyFp6 ? ` · ${provider.keyFp6}` : ""}
          </small>
        </button>
        <span className={`status ${provider.disabled ? "error" : "success"}`}>{provider.disabled ? "已停用" : "已启用"}</span>
        <button
          type="button"
          className="btn btn-sm"
          onClick={() => void test(provider)}
          disabled={testStatus.testing}
        >
          {testStatus.testing ? "测试中…" : "测试"}
        </button>
        {/* 行内状态位：详情折叠时也要能看到测试结论（F4）。 */}
        {testStatus.label ? (
          <span className={`status provider-test-status ${testStatus.tone}`} role="status" title={testStatus.title}>
            {testStatus.label}
          </span>
        ) : null}
        <button
          type="button"
          role="switch"
          aria-checked={!provider.disabled}
          aria-label={`${provider.disabled ? "启用" : "停用"} ${provider.name}`}
          className={`switch${provider.disabled ? "" : " on"}`}
          onClick={() => void toggle(provider)}
        >
          <i />
        </button>
        <button type="button" className="provider-chev" aria-label="展开或收起详情" aria-expanded={open} onClick={() => setExpanded(open ? null : provider.id)}>
          <Icon name={open ? "up" : "down"} size={14} />
        </button>
        {open ? (
          <div className="provider-detail">
            <div className="kv">协议 <strong>{PROTOCOL_LABEL[(provider.providerType === "custom" ? "openai" : provider.providerType) as keyof typeof PROTOCOL_LABEL] ?? provider.providerType}</strong> · 端点 <span className="mono">{provider.baseUrl}</span> · 鉴权头 <span className="mono">{provider.authHeaderName}</span></div>
            <div className="provider-models">
              {provider.models.length === 0 ? <span className="page-sub">还没有模型映射</span> : provider.models.map((model) => (
                <span className="chip" key={`${model.alias}-${model.modelId}`}>{model.alias} → {model.modelId}</span>
              ))}
            </div>
            {result && result.error !== "testing" ? (
              <div className={`alert ${result.ok ? "ok" : "error"}`} role="status">
                {result.ok ? `连接成功，耗时 ${result.latency_ms} ms` : `连接失败：${result.error ?? "connection_failed"}`}
              </div>
            ) : null}
            {testStatus.label ? <div className="page-sub">最近一次测试：{testStatus.title}</div> : null}
          </div>
        ) : null}
      </div>
    );
  }

  return (
    <div className="page">
      <div className="page-head">
        <div>
          <h1 className="page-title">供应商</h1>
          <p className="page-sub">上游厂商与中转聚合。凭据加密存储；停用后 2 秒内退出路由。</p>
        </div>
        <div className="provider-toolbar">
          <input
            className="provider-search"
            placeholder="搜索供应商"
            aria-label="搜索已配置的供应商"
            value={query}
            onChange={(event) => setQuery(event.target.value)}
          />
        </div>
      </div>

      {error ? <div className="alert error" role="alert">{error}</div> : null}
      {notice ? <div className="alert ok" role="status">{notice}</div> : null}

      <section className="card panel">
        <div className="panel-head">
          <div>
            <h2 className="panel-title">已启用</h2>
            <p className="panel-sub">{active.length} 家供应商在路由中</p>
          </div>
        </div>
        {loading ? <Skeleton height={120} /> : filteredActive.length === 0 ? (
          <p className="empty">还没有启用任何供应商。在下方目录里添加一家。</p>
        ) : (
          <div className="provider-list">{filteredActive.map(renderProvider)}</div>
        )}

        {inactive.length > 0 ? (
          <>
            <button type="button" className="provider-fold" onClick={() => setShowDisabled(!showDisabled)} aria-expanded={showDisabled}>
              <Icon name={showDisabled ? "up" : "down"} size={13} /> 已停用（{inactive.length}）
            </button>
            {showDisabled ? <div className="provider-list">{filteredInactive.map(renderProvider)}</div> : null}
          </>
        ) : null}
      </section>

      <section className="card panel">
        <div className="panel-head">
          <div>
            <h2 className="panel-title">添加供应商</h2>
            <p className="panel-sub">按 API Key 接入的厂商与聚合中转；订阅登录类不在其中。</p>
          </div>
          <div className="provider-toolbar">
            <input
              className="provider-search"
              placeholder="搜索供应商"
              aria-label="搜索供应商目录"
              value={catalogQuery}
              onChange={(event) => setCatalogQuery(event.target.value)}
            />
          </div>
        </div>

        {draft ? (
          <form className="provider-sheet" onSubmit={save}>
            <div className="provider-sheet-head">
              <ProviderLogo entry={draft.entry} label={draft.entry.name} />
              <div>
                <strong>{draft.entry.name}</strong>
                <div className="page-sub">{PROTOCOL_LABEL[draft.protocol]} · {draft.entry.note ?? "填写凭据后保存"}</div>
              </div>
              <button type="button" className="btn btn-sm" style={{ marginLeft: "auto" }} onClick={() => setDraft(null)}>取消</button>
            </div>

            {protocolsOf(draft.entry).length > 1 || (draft.entry.regions?.length ?? 0) > 1 ? (
              <div className="form-row">
                {protocolsOf(draft.entry).length > 1 ? (
                  <div className="field">
                    <label htmlFor="provider-protocol">协议</label>
                    <select
                      id="provider-protocol"
                      value={draft.protocol}
                      onChange={(event) => setDraft(retarget(draft, { protocol: event.target.value as Protocol }))}
                    >
                      {protocolsOf(draft.entry).map((protocol) => (
                        <option key={protocol} value={protocol}>
                          {PROTOCOL_LABEL[protocol]}
                        </option>
                      ))}
                    </select>
                  </div>
                ) : null}
                {(draft.entry.regions?.length ?? 0) > 1 ? (
                  <div className="field" style={{ flex: 2 }}>
                    <label htmlFor="provider-region">区域</label>
                    <select
                      id="provider-region"
                      value={draft.regionId}
                      onChange={(event) => setDraft(retarget(draft, { regionId: event.target.value }))}
                    >
                      {draft.entry.regions!.map((region) => (
                        <option key={region.id} value={region.id}>
                          {region.name}
                        </option>
                      ))}
                    </select>
                  </div>
                ) : null}
              </div>
            ) : null}

            <div className="form-row">
              <div className="field">
                <label htmlFor="provider-name">名称</label>
                <input id="provider-name" value={draft.name} onChange={(event) => setDraft({ ...draft, name: event.target.value })} required />
              </div>
              <div className="field" style={{ flex: 2 }}>
                <label htmlFor="provider-base">Base URL</label>
                <input
                  id="provider-base"
                  value={draft.baseUrl}
                  onChange={(event) => setDraft({ ...draft, baseUrl: event.target.value })}
                  placeholder="https://api.example.com/v1"
                  required
                />
              </div>
              <div className="field" style={{ flex: 2 }}>
                <label htmlFor="provider-key">API Key</label>
                <input
                  id="provider-key"
                  type="password"
                  value={draft.apiKey}
                  onChange={(event) => setDraft({ ...draft, apiKey: event.target.value })}
                  placeholder="加密存储，保存后不再显示"
                  required
                />
              </div>
            </div>

            <div className="field">
              <label htmlFor="provider-models">模型映射（每行一条：<span className="mono">alias=upstream-model</span>）</label>
              <textarea
                id="provider-models"
                rows={4}
                value={draft.models}
                onChange={(event) => setDraft({ ...draft, models: event.target.value })}
                placeholder={"claude-sonnet=claude-sonnet-4-5-20250929\ngpt-5=gpt-5.1"}
              />
            </div>

            <details className="provider-advanced">
              <summary>高级：鉴权头</summary>
              <div className="form-row">
                <div className="field">
                  <label htmlFor="provider-auth-header">头名称</label>
                  <input id="provider-auth-header" value={draft.authHeaderName} onChange={(event) => setDraft({ ...draft, authHeaderName: event.target.value })} />
                </div>
                <div className="field">
                  <label htmlFor="provider-auth-scheme">前缀</label>
                  <input id="provider-auth-scheme" value={draft.authScheme} onChange={(event) => setDraft({ ...draft, authScheme: event.target.value })} placeholder="Bearer 或留空" />
                </div>
              </div>
            </details>

            <div className="toolbar">
              <button className="btn btn-primary" type="submit" disabled={busy}>{busy ? "保存中…" : "保存供应商"}</button>
              {draft.entry.docsUrl ? (
                <a className="btn btn-sm" href={draft.entry.docsUrl} target="_blank" rel="noreferrer">官方文档 ↗</a>
              ) : null}
            </div>
          </form>
        ) : null}

        {groups.map((group) => (
          <div className="provider-group" key={group.group}>
            <h3 className="provider-group-title">{GROUP_LABEL[group.group]}</h3>
            <div className="provider-grid">
              {group.entries.map((entry) => {
                const configured = configuredIds.has(entry.id);
                const disabled = entry.status === "unsupported";
                return (
                  <button
                    key={entry.id}
                    type="button"
                    className={`provider-tile${disabled ? " disabled" : ""}${configured ? " configured" : ""}`}
                    onClick={() => openDraft(entry)}
                    disabled={disabled}
                    title={entry.note ?? (configured ? "已配置过；可再添加一条独立凭据" : `添加 ${entry.name}`)}
                  >
                    <ProviderLogo entry={entry} label={entry.name} />
                    <span className="provider-tile-name">{entry.name}</span>
                    {entry.status === "needs-config" ? <span className="tag-warn">需填端点</span> : null}
                    {disabled ? <span className="tag-warn">暂不支持</span> : null}
                    {configured && !disabled ? <span className="provider-dot" aria-label="已配置" /> : null}
                  </button>
                );
              })}
            </div>
          </div>
        ))}
      </section>
    </div>
  );
}
