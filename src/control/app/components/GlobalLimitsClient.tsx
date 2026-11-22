"use client";

import { useCallback, useEffect, useState } from "react";

const DIMS = [
  { key: "rpm", label: "RPM（每分钟请求数）" },
  { key: "tpm", label: "TPM（每分钟总 Token）" },
  { key: "itpm", label: "ITPM（每分钟输入 Token）" },
  { key: "otpm", label: "OTPM（每分钟输出 Token）" },
] as const;

type DimKey = (typeof DIMS)[number]["key"];

export default function GlobalLimitsClient() {
  const [values, setValues] = useState<Record<DimKey, string>>({ rpm: "", tpm: "", itpm: "", otpm: "" });
  const [error, setError] = useState("");
  const [ok, setOk] = useState("");
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    const response = await fetch("/api/admin/limits/global");
    if (response.status === 401) {
      window.location.assign("/login");
      return;
    }
    if (response.status === 403) {
      setError("只有管理员可以配置全局限流。");
      return;
    }
    if (!response.ok) {
      setError("加载全局限流失败。");
      return;
    }
    const data = (await response.json()) as { limits: Record<string, number> };
    setValues({
      rpm: data.limits.rpm ? String(data.limits.rpm) : "",
      tpm: data.limits.tpm ? String(data.limits.tpm) : "",
      itpm: data.limits.itpm ? String(data.limits.itpm) : "",
      otpm: data.limits.otpm ? String(data.limits.otpm) : "",
    });
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  async function save(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setError("");
    setOk("");
    setBusy(true);
    try {
      const payload: Record<string, number> = {};
      for (const dim of DIMS) {
        const raw = values[dim.key].trim();
        payload[dim.key] = raw === "" ? 0 : Number(raw);
      }
      const response = await fetch("/api/admin/limits/global", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(payload),
      });
      if (!response.ok) {
        setError("保存失败，请检查数值（0 或留空表示不限）。");
        return;
      }
      setOk("已保存，网关将在 2 秒内生效。");
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
            <h2 className="section-title">全局限流（第二级）</h2>
            <p className="section-sub">
              与每把 Key 的限额同时生效，取更严的那个。只填需要的维度；0 或留空表示不限。
            </p>
          </div>
          <span className="tag-warn">FR-23/24</span>
        </div>
        <form onSubmit={save}>
          <div className="form-row">
            {DIMS.map((dim) => (
              <div className="field" key={dim.key} style={{ maxWidth: 220 }}>
                <label htmlFor={`global-${dim.key}`}>{dim.label}</label>
                <input
                  id={`global-${dim.key}`}
                  type="number"
                  min={0}
                  value={values[dim.key]}
                  onChange={(event) => setValues({ ...values, [dim.key]: event.target.value })}
                  placeholder="0 = 不限"
                />
              </div>
            ))}
          </div>
          <button className="btn btn-primary" type="submit" disabled={busy}>
            {busy ? "保存中…" : "保存全局限流"}
          </button>
        </form>
      </section>
    </>
  );
}
