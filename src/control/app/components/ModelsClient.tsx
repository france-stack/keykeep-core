"use client";

import { useCallback, useEffect, useState } from "react";

import PricesClient from "./PricesClient";
import { SegmentedTabs } from "./SegmentedTabs";
import { SkeletonTable } from "./Skeleton";

type ModelRow = {
  id: string;
  alias: string;
  provider: string;
  modelId: string;
  credential: { id: string; name: string; providerType: string } | null;
};

type Provider = { id: string; name: string; provider: string; providerType: string; disabled: boolean };

export default function ModelsClient({ role }: { role: string }) {
  const isAdmin = role === "admin";
  const [tab, setTab] = useState<"models" | "prices">("models");
  const [models, setModels] = useState<ModelRow[]>([]);
  const [providers, setProviders] = useState<Provider[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [ok, setOk] = useState("");
  const [busy, setBusy] = useState(false);
  const [form, setForm] = useState({ credentialId: "", alias: "", modelId: "" });
  const [syncing, setSyncing] = useState(false);
  const [catalogInfo, setCatalogInfo] = useState<{ catalogRows: number; versions: Array<{ version: string; models: number }> } | null>(null);
  // 上游模型下拉：按所选凭据的 providerType 从 models.dev 目录里筛。
  const [catalogModels, setCatalogModels] = useState<string[]>([]);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const response = await fetch("/api/admin/models");
      if (response.status === 401) {
        window.location.assign("/login");
        return;
      }
      if (response.ok) {
        const data = (await response.json()) as { models: ModelRow[] };
        setModels(data.models ?? []);
      }
      if (isAdmin) {
        const providerRes = await fetch("/api/admin/providers");
        if (providerRes.ok) {
          const data = (await providerRes.json()) as { providers: Provider[] };
          setProviders((data.providers ?? []).filter((provider) => !provider.disabled));
        }
      }
    } finally {
      setLoading(false);
    }
  }, [isAdmin]);

  useEffect(() => {
    void load();
  }, [load]);

  useEffect(() => {
    if (!isAdmin) return;
    void fetch("/api/admin/models/sync")
      .then((response) => (response.ok ? response.json() : null))
      .then((data) => {
        if (data) setCatalogInfo(data as { catalogRows: number; versions: Array<{ version: string; models: number }> });
      })
      .catch(() => undefined);
  }, [isAdmin]);

  // 选中凭据后，用该目录条目在 models.dev 的模型名填充下拉；拉不到就退回自由输入。
  useEffect(() => {
    const credential = providers.find((item) => item.id === form.credentialId);
    if (!credential) {
      setCatalogModels([]);
      return;
    }
    const query = new URLSearchParams({
      catalogId: credential.provider ?? "",
      protocol: credential.providerType,
    });
    void fetch(`/api/admin/models/catalog?${query.toString()}`)
      .then((response) => (response.ok ? response.json() : null))
      .then((data: { models?: string[] } | null) => setCatalogModels(data?.models ?? []))
      .catch(() => setCatalogModels([]));
  }, [form.credentialId, providers]);

  async function syncCatalog() {
    setError("");
    setOk("");
    setSyncing(true);
    try {
      const response = await fetch("/api/admin/models/sync?force=1", { method: "POST" });
      const data = (await response.json()) as { models?: number; providers?: number; version?: string; error?: { code?: string } };
      if (!response.ok) {
        setError(data.error?.code === "catalog_sync_failed" ? "无法连接 models.dev，仍在使用上一版目录。" : "同步失败。");
        return;
      }
      setOk(`目录已同步：${data.providers ?? 0} 家供应商共 ${data.models ?? 0} 个模型（${data.version ?? ""}）。`);
      const info = await fetch("/api/admin/models/sync").then((res) => (res.ok ? res.json() : null));
      if (info) setCatalogInfo(info as { catalogRows: number; versions: Array<{ version: string; models: number }> });
    } catch {
      setError("网络错误，目录未刷新。");
    } finally {
      setSyncing(false);
    }
  }

  async function create(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setError("");
    setOk("");
    setBusy(true);
    try {
      const response = await fetch("/api/admin/models", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(form),
      });
      const data = (await response.json()) as { error?: { code?: string } };
      if (!response.ok) {
        setError(data.error?.code === "invalid_request" ? "请选择凭据，并填写 alias 与上游模型。" : "创建失败。");
        return;
      }
      setOk("映射已保存。");
      setForm({ credentialId: "", alias: "", modelId: "" });
      await load();
    } catch {
      setError("网络错误，映射未保存。");
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="page">
      <div className="page-head">
        <div>
          <h1 className="page-title">模型与价格</h1>
          <p className="page-sub">逻辑 alias 映射到上游模型；价格按版本管理。</p>
        </div>
        <div className="page-actions">
          {isAdmin ? (
            <button type="button" className="btn btn-sm" onClick={() => void syncCatalog()} disabled={syncing}>
              {syncing ? "同步中…" : "同步模型目录"}
            </button>
          ) : null}
          <SegmentedTabs
            label="模型视图"
            value={tab}
            onChange={(id) => setTab(id as "models" | "prices")}
            items={isAdmin
              ? [{ id: "models", label: "映射" }, { id: "prices", label: "价格" }]
              : [{ id: "models", label: "映射" }]}
          />
        </div>
      </div>

      {error ? <div className="alert error" role="alert">{error}</div> : null}
      {ok ? <div className="alert ok" role="status">{ok}</div> : null}
      {isAdmin && catalogInfo ? (
        <p className="page-sub">
          models.dev 目录：{catalogInfo.catalogRows} 行
          {catalogInfo.versions.length > 0 ? ` · 最新版本 ${catalogInfo.versions[0].version}（${catalogInfo.versions[0].models} 条价格）` : " · 尚未同步"}
        </p>
      ) : null}

      {tab === "prices" && isAdmin ? (
        <div id="panel-prices" role="tabpanel" aria-labelledby="tab-prices">
          <PricesClient />
        </div>
      ) : (
        <div id="panel-models" role="tabpanel" aria-labelledby="tab-models">
          {isAdmin ? (
            <section className="card panel">
              <div className="panel-head">
                <div>
                  <h2 className="panel-title">新建映射</h2>
                  <p className="panel-sub">一个 alias 可以挂多条凭据；停用其中一条，流量自动转到其他凭据。</p>
                </div>
              </div>
              <form onSubmit={create}>
                <div className="form-row">
                  <div className="field">
                    <label htmlFor="model-credential">凭据</label>
                    <select
                      id="model-credential"
                      value={form.credentialId}
                      onChange={(event) => setForm({ ...form, credentialId: event.target.value })}
                      required
                    >
                      <option value="">选择一条凭据</option>
                      {providers.map((provider) => (
                        <option value={provider.id} key={provider.id}>
                          {provider.name}（{provider.providerType}）
                        </option>
                      ))}
                    </select>
                  </div>
                  <div className="field">
                    <label htmlFor="model-alias">逻辑 alias</label>
                    <input
                      id="model-alias"
                      value={form.alias}
                      onChange={(event) => setForm({ ...form, alias: event.target.value })}
                      placeholder="deepseek-flash"
                      required
                    />
                  </div>
                  <div className="field">
                    <label htmlFor="model-id">上游模型 ID</label>
                    <input
                      id="model-id"
                      list={catalogModels.length > 0 ? "model-id-options" : undefined}
                      value={form.modelId}
                      onChange={(event) => setForm({ ...form, modelId: event.target.value })}
                      placeholder="deepseek-v4.1-flash"
                      required
                    />
                    {catalogModels.length > 0 ? (
                      <datalist id="model-id-options">
                        {catalogModels.map((model) => (
                          <option key={model} value={model} />
                        ))}
                      </datalist>
                    ) : null}
                  </div>
                </div>
                <div className="toolbar">
                  <button className="btn btn-primary" type="submit" disabled={busy}>
                    {busy ? "保存中…" : "保存映射"}
                  </button>
                </div>
              </form>
            </section>
          ) : (
            <div className="alert warn" role="status">只读：新建映射与价格版本仅管理员可操作。</div>
          )}

          <section className="card panel">
            <div className="panel-head">
              <div>
                <h2 className="panel-title">模型映射</h2>
                <p className="panel-sub">{models.length} 条 · alias → 上游模型</p>
              </div>
            </div>
            {loading ? <SkeletonTable rows={6} columns={4} /> : models.length === 0 ? (
              <p className="empty">还没有模型映射。</p>
            ) : (
              <div className="table-wrap">
                <table>
                  <thead>
                    <tr><th scope="col">逻辑 alias</th><th scope="col">供应商</th><th scope="col">上游模型</th><th scope="col">凭据</th></tr>
                  </thead>
                  <tbody>
                    {models.map((model) => (
                      <tr key={model.id}>
                        <td className="mono">{model.alias}</td>
                        <td><span className="chip">{model.provider}</span></td>
                        <td className="mono">{model.modelId}</td>
                        <td>{model.credential?.name || "未绑定"}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </section>
        </div>
      )}
    </div>
  );
}
