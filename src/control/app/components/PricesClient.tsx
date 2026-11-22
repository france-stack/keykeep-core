"use client";

import { useCallback, useEffect, useState } from "react";

type PriceRow = {
  provider: string;
  modelId: string;
  version: string;
  effectiveFrom: string;
  effective: boolean;
  promptPerM: number;
  completionPerM: number;
  cacheReadPerM: number;
  cacheWritePerM: number;
};

type Payload = {
  prices: PriceRow[];
  total?: { catalog: number; manual: number; all: number };
};

type SourceKind = "catalog" | "manual";

/** 版本号前缀决定这条价格是谁写进来的，别再让老板猜「我没配过这个模型哪来的」。 */
function sourceOf(version: string): SourceKind {
  return version.startsWith("models.dev-") ? "catalog" : "manual";
}

const SOURCE_LABEL: Record<SourceKind, string> = {
  catalog: "目录同步",
  manual: "手工发布",
};

function money(cents: number): string {
  return `¥${(cents / 100).toFixed(2)}`;
}

export default function PricesClient() {
  const [data, setData] = useState<Payload>({ prices: [] });
  const [form, setForm] = useState({
    // 不再预填 anthropic / 300 / 1500 这类"看起来像已配置"的默认值：
    // 之前的默认值让一次随手点「发布」就往库里写了一条 anthropic 价格（qa-test-20261109）。
    provider: "",
    modelId: "",
    version: "",
    effectiveFrom: "",
    promptPerM: "",
    completionPerM: "",
    cacheReadPerM: "",
    cacheWritePerM: "",
  });
  const [error, setError] = useState("");
  const [ok, setOk] = useState("");
  const [busy, setBusy] = useState(false);
  // 默认只看「手工发布」：目录同步那几千行是 models.dev 的机器数据，
  // 混在一起会让人以为「我没配过的模型怎么在这儿」。
  const [source, setSource] = useState<SourceKind | "all">("manual");

  const load = useCallback(async () => {
    const response = await fetch(`/api/admin/prices?source=${source}`);
    if (response.status === 401) {
      window.location.assign("/login");
      return;
    }
    if (!response.ok) {
      setError("加载价格表失败。");
      return;
    }
    setData((await response.json()) as Payload);
  }, [source]);

  useEffect(() => {
    void load();
  }, [load]);

  function update(key: keyof typeof form, value: string) {
    setForm((prev) => ({ ...prev, [key]: value }));
  }

  // 服务端已按来源过滤，这里只做一道保险（避免旧响应在新筛选下露出别的来源）。
  // 「目录同步」一次能返回 500 行，整页渲染会拉到两万多像素高，先只铺前 100 行。
  const ROW_CAP = 100;
  const matched = data.prices.filter((row) => source === "all" || sourceOf(row.version) === source);
  const visible = matched.slice(0, ROW_CAP);
  const total = data.total?.[source] ?? data.prices.length;

  async function publish(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setError("");
    setOk("");
    setBusy(true);
    try {
      const response = await fetch("/api/admin/prices", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          provider: form.provider,
          modelId: form.modelId,
          version: form.version,
          effectiveFrom: form.effectiveFrom || undefined,
          promptPerM: Number(form.promptPerM),
          completionPerM: Number(form.completionPerM),
          cacheReadPerM: Number(form.cacheReadPerM),
          cacheWritePerM: Number(form.cacheWritePerM),
        }),
      });
      if (response.status === 409) {
        setError("该版本已存在，请换一个版本号。");
        return;
      }
      if (!response.ok) {
        setError("发布失败，请检查模型、版本和价格。");
        return;
      }
      setOk(`已发布 ${form.modelId} @ ${form.version}`);
      await load();
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      {error ? <div className="alert error" role="alert">{error}</div> : null}
      {ok ? <div className="alert ok" role="status">{ok}</div> : null}

      <section className="card panel">
        <div className="panel-head">
          <div>
            <h2 className="section-title">发布价格版本</h2>
            <p className="section-sub">
              价格单位是「分 / 100 万 Token」。生效时间一过，新请求按新价格计费；历史金额不变。
            </p>
          </div>
          <span className="tag-warn">FR-52</span>
        </div>
        <form onSubmit={publish}>
          <div className="form-row">
            <div className="field">
              <label htmlFor="provider">供应商</label>
              <input
                id="provider"
                value={form.provider}
                onChange={(e) => update("provider", e.target.value)}
                placeholder="anthropic / deepseek"
                required
              />
            </div>
            <div className="field">
              <label htmlFor="modelId">上游模型 ID</label>
              <input
                id="modelId"
                value={form.modelId}
                onChange={(e) => update("modelId", e.target.value)}
                placeholder="deepseek-v4.1-flash"
                required
              />
            </div>
            <div className="field">
              <label htmlFor="version">版本</label>
              <input
                id="version"
                value={form.version}
                onChange={(e) => update("version", e.target.value)}
                placeholder="v2 / 2026-11"
                required
              />
            </div>
            <div className="field">
              <label htmlFor="effectiveFrom">生效时间（留空 = 立即）</label>
              <input
                id="effectiveFrom"
                type="datetime-local"
                value={form.effectiveFrom}
                onChange={(e) => update("effectiveFrom", e.target.value)}
              />
            </div>
          </div>
          <div className="form-row">
            <div className="field">
              <label htmlFor="promptPerM">输入（分/100万）</label>
              <input id="promptPerM" type="number" min={0} value={form.promptPerM} onChange={(e) => update("promptPerM", e.target.value)} placeholder="0" required />
            </div>
            <div className="field">
              <label htmlFor="completionPerM">输出（分/100万）</label>
              <input id="completionPerM" type="number" min={0} value={form.completionPerM} onChange={(e) => update("completionPerM", e.target.value)} placeholder="0" required />
            </div>
            <div className="field">
              <label htmlFor="cacheReadPerM">缓存读取（分/100万）</label>
              <input id="cacheReadPerM" type="number" min={0} value={form.cacheReadPerM} onChange={(e) => update("cacheReadPerM", e.target.value)} placeholder="0" required />
            </div>
            <div className="field">
              <label htmlFor="cacheWritePerM">缓存写入（分/100万）</label>
              <input id="cacheWritePerM" type="number" min={0} value={form.cacheWritePerM} onChange={(e) => update("cacheWritePerM", e.target.value)} placeholder="0" required />
            </div>
          </div>
          <button className="btn btn-primary" type="submit" disabled={busy}>
            {busy ? "发布中…" : "发布"}
          </button>
        </form>
      </section>

      <section className="card panel">
        <div className="panel-head">
          <div>
            <h2 className="section-title">价格版本</h2>
            <p className="section-sub">同一模型可以有多个版本；网关使用当前生效的最新一版。</p>
          </div>
          <div className="toolbar">
            <select
              aria-label="价格来源"
              className="table-filter"
              value={source}
              onChange={(e) => setSource(e.target.value as SourceKind | "all")}
            >
              <option value="manual">只看手工发布</option>
              <option value="catalog">只看目录同步</option>
              <option value="all">全部来源</option>
            </select>
            <span className="pill">
              {visible.length} / {total} 条{total > visible.length ? `（最多显示 ${ROW_CAP} 条）` : ""}
            </span>
          </div>
        </div>
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>模型</th>
                <th>版本</th>
                <th>来源</th>
                <th>状态</th>
                <th>生效时间</th>
                <th>输入 / 输出</th>
                <th>缓存读取 / 写入</th>
              </tr>
            </thead>
            <tbody>
              {visible.map((row) => (
                <tr key={`${row.provider}:${row.modelId}:${row.version}`}>
                  <td>
                    <div className="model-name">{row.modelId}</div>
                    <div className="model-meta"><span className="provider-chip">{row.provider}</span></div>
                  </td>
                  <td className="mono">{row.version}</td>
                  <td>
                    <span className={`status ${sourceOf(row.version) === "catalog" ? "limited" : "success"}`}>
                      {SOURCE_LABEL[sourceOf(row.version)]}
                    </span>
                  </td>
                  <td>
                    <span className={`status ${row.effective ? "success" : "limited"}`}>
                      {row.effective ? "已生效" : "待生效"}
                    </span>
                  </td>
                  <td className="mono">{new Date(row.effectiveFrom).toLocaleString("zh-CN")}</td>
                  <td className="mono">{money(row.promptPerM)} / {money(row.completionPerM)}</td>
                  <td className="mono">{money(row.cacheReadPerM)} / {money(row.cacheWritePerM)}</td>
                </tr>
              ))}
              {visible.length === 0 ? (
                <tr>
                  <td colSpan={7} className="section-sub">
                    {total === 0 && source === "manual"
                      ? "还没有手工发布的价格。目录同步导入的机器数据可以切「只看目录同步」查看。"
                      : total === 0
                        ? "还没有价格版本。已有 ModelCatalog 价格已回填为 v1。"
                        : "当前筛选下没有价格版本；切到「全部来源」看看目录同步导入的机器数据。"}
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
