"use client";

import { useState } from "react";

type Status = {
  status: string;
  writable: boolean;
  schemaReady: boolean;
  licenseId: string | null;
  customer: string | null;
  machineCode: string;
  machineFingerprint: string;
  expiresAt: string | null;
  daysLeft: number | null;
  trialDaysLeft: number | null;
  maxEmployees: number;
  revocationVersion: number;
  message: string;
};

function fmt(value: string | null): string {
  if (!value) return "—";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString();
}

export default function LicenseClient({ status, role }: { status: Status; role: string }) {
  const [licenseText, setLicenseText] = useState("");
  const [revocationText, setRevocationText] = useState("");
  const [error, setError] = useState("");
  const [ok, setOk] = useState("");
  const [busy, setBusy] = useState(false);
  const isAdmin = role === "admin";

  async function submit(kind: "license" | "revocations") {
    setError("");
    setOk("");
    const raw = kind === "license" ? licenseText : revocationText;
    if (!raw.trim()) {
      setError("请先粘贴 JSON 内容。");
      return;
    }
    let parsed: unknown;
    try {
      parsed = JSON.parse(raw);
    } catch {
      setError("JSON 格式无效。");
      return;
    }
    setBusy(true);
    try {
      const response = await fetch(
        kind === "license" ? "/api/admin/license" : "/api/admin/license/revocations",
        {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(kind === "license" ? { license: parsed } : { revocations: parsed }),
        },
      );
      const data = (await response.json().catch(() => ({}))) as { error?: { code?: string }; status?: Status };
      if (!response.ok) {
        setError(data.error?.code ?? "导入失败。");
        return;
      }
      setOk(`已导入。状态：${data.status?.status ?? "已更新"}。`);
      window.location.reload();
    } finally {
      setBusy(false);
    }
  }

  async function readFile(kind: "license" | "revocations", file: File | null) {
    if (!file) return;
    const text = await file.text();
    if (kind === "license") setLicenseText(text);
    else setRevocationText(text);
  }

  return (
    <>
      {error ? <div className="alert error" role="alert">{error}</div> : null}
      {ok ? <div className="alert ok" role="status">{ok}</div> : null}

      <section className="card panel">
        <div className="panel-head">
          <div>
            <h2 className="section-title">状态：{status.status}</h2>
            <p className="section-sub">{status.message}{status.writable ? " 允许写入操作。" : " 控制台为只读；转发不受影响。"}</p>
          </div>
          <span className="tag-warn">FR-35</span>
        </div>
        <dl className="kv">
          <dt>机器码</dt><dd><code>{status.machineCode || "未配置"}</code></dd>
          <dt>指纹</dt><dd><code>{status.machineFingerprint || "—"}</code></dd>
          <dt>授权 ID</dt><dd>{status.licenseId ?? "试用"}</dd>
          <dt>客户</dt><dd>{status.customer ?? "—"}</dd>
          <dt>到期时间</dt><dd>{fmt(status.expiresAt)}{status.daysLeft !== null ? `（剩余 ${status.daysLeft} 天）` : ""}</dd>
          <dt>试用期</dt><dd>{status.trialDaysLeft !== null ? `剩余 ${status.trialDaysLeft} 天` : "—"}</dd>
          <dt>员工上限</dt><dd>{status.maxEmployees || "不限"}</dd>
          <dt>吊销列表</dt><dd>v{status.revocationVersion}</dd>
        </dl>
      </section>

      {isAdmin ? (
        <>
          <section className="card panel">
            <div className="panel-head"><div><h2 className="section-title">导入授权</h2><p className="section-sub">粘贴 license.json 或选择文件。签名在本地校验。</p></div></div>
            <div className="field">
              <label htmlFor="license-file">授权文件</label>
              <input id="license-file" type="file" accept=".json,application/json" onChange={(e) => void readFile("license", e.target.files?.[0] ?? null)} />
            </div>
            <div className="field">
              <label htmlFor="license-json">license.json</label>
              <textarea id="license-json" rows={8} value={licenseText} onChange={(e) => setLicenseText(e.target.value)} placeholder='{"alg":"Ed25519","kid":"v1","payload":"…","sig":"…"}' />
            </div>
            <button className="btn btn-primary" type="button" disabled={busy} onClick={() => void submit("license")}>导入授权</button>
          </section>

          <section className="card panel">
            <div className="panel-head"><div><h2 className="section-title">导入吊销列表</h2><p className="section-sub">离线签名列表；命中授权 ID 或机器码后转为只读。</p></div></div>
            <div className="field">
              <label htmlFor="revocation-file">吊销文件</label>
              <input id="revocation-file" type="file" accept=".json,application/json" onChange={(e) => void readFile("revocations", e.target.files?.[0] ?? null)} />
            </div>
            <div className="field">
              <label htmlFor="revocation-json">revocations.json</label>
              <textarea id="revocation-json" rows={6} value={revocationText} onChange={(e) => setRevocationText(e.target.value)} placeholder='{"alg":"Ed25519","kid":"v1","payload":"…","sig":"…"}' />
            </div>
            <button className="btn" type="button" disabled={busy} onClick={() => void submit("revocations")}>导入吊销列表</button>
          </section>
        </>
      ) : (
        <section className="card panel"><p className="section-sub">只有老板可以导入授权，请联系管理员。</p></section>
      )}
    </>
  );
}
