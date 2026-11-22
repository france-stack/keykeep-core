"use client";

import { useRouter } from "next/navigation";
import { useEffect, useMemo, useState } from "react";

import { Icon } from "./Icon";

type Step = 1 | 2 | 3;

type TestResult = { ok: boolean; status: number | null; latency_ms: number; preview?: string; error?: string };

type ProviderDraft = {
  name: string;
  baseUrl: string;
  apiKey: string;
  alias: string;
  modelId: string;
};

const EMPTY_PROVIDER: ProviderDraft = {
  name: "我的供应商",
  baseUrl: "",
  apiKey: "",
  alias: "my-model",
  modelId: "",
};

const STEPS: Array<{ id: Step; title: string }> = [
  { id: 1, title: "设置管理员密码" },
  { id: 2, title: "接入首个供应商" },
  { id: 3, title: "签发第一把虚拟 Key" },
];

export default function OnboardingWizard({
  initialStep,
  demoAvailable,
  passwordChanged,
  hasProvider,
}: {
  initialStep: Step;
  demoAvailable: boolean;
  passwordChanged: boolean;
  hasProvider: boolean;
}) {
  const router = useRouter();
  const [step, setStep] = useState<Step>(initialStep);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [busy, setBusy] = useState(false);

  // step 1
  const [currentPassword, setCurrentPassword] = useState("");
  const [newPassword, setNewPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");

  // step 2
  const [provider, setProvider] = useState<ProviderDraft>(EMPTY_PROVIDER);
  const [providerDone, setProviderDone] = useState(hasProvider);

  // step 3
  const [employees, setEmployees] = useState<Array<{ id: string; displayName: string; account: string }>>([]);
  const [empId, setEmpId] = useState("");
  const [issuedKey, setIssuedKey] = useState("");
  const [testModel, setTestModel] = useState("");
  const [testResult, setTestResult] = useState<TestResult | null>(null);
  const [completed, setCompleted] = useState(false);

  useEffect(() => {
    if (step !== 3) return;
    void (async () => {
      const response = await fetch("/api/admin/users");
      if (!response.ok) return;
      const data = (await response.json()) as { users: Array<{ id: string; displayName: string; account: string; disabled: boolean; role: string }> };
      setEmployees(data.users.filter((user) => !user.disabled && user.role === "employee"));
    })();
  }, [step]);

  useEffect(() => {
    if (step === 1 && passwordChanged) setStep(2);
    if (step === 2 && hasProvider) setStep(3);
  }, [passwordChanged, hasProvider, step]);

  const canFinish = useMemo(() => passwordChanged && (providerDone || hasProvider), [passwordChanged, providerDone, hasProvider]);

  async function changePassword(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setError("");
    setNotice("");
    if (newPassword !== confirmPassword) {
      setError("两次输入的新密码不一致。");
      return;
    }
    if (newPassword.length < 12) {
      setError("新密码至少 12 个字符。");
      return;
    }
    setBusy(true);
    try {
      const response = await fetch("/api/me/password", {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ currentPassword, newPassword }),
      });
      if (!response.ok) {
        const data = (await response.json()) as { error?: { code?: string; reason?: string } };
        setError(
          data.error?.code === "invalid_credentials"
            ? "当前密码不正确。"
            : data.error?.reason === "password_too_short"
              ? "新密码至少 12 个字符。"
              : "修改失败，请重试。",
        );
        return;
      }
      // 改密即本会话失效（安全模型的既定行为）。引导用户重新登录后回到第 2 步。
      setNotice("密码已更新。请用新密码重新登录，登录后会直接回到第 2 步。");
      setCurrentPassword("");
      setNewPassword("");
      setConfirmPassword("");
      const logout = await fetch("/api/auth/logout", { method: "POST" });
      void logout;
      window.location.assign("/login?next=/admin/onboarding%3Fstep%3D2");
    } finally {
      setBusy(false);
    }
  }

  async function saveProvider(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setError("");
    setNotice("");
    setBusy(true);
    try {
      const models = provider.modelId.trim() ? [{ alias: provider.alias.trim(), modelId: provider.modelId.trim() }] : [];
      const response = await fetch("/api/admin/providers", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          name: provider.name.trim() || "我的供应商",
          providerType: "openai",
          baseUrl: provider.baseUrl.trim(),
          apiKey: provider.apiKey,
          models,
        }),
      });
      const data = (await response.json()) as { provider?: { id: string }; error?: { code?: string } };
      if (!response.ok) {
        const code = data.error?.code;
        setError(
          code === "upstream_not_allowed"
            ? "Base URL 未通过安全校验（内网/元数据地址默认禁止）。"
            : code === "kek_not_configured"
              ? "未配置 KEYKEEP_KEK，无法加密凭据。"
              : code === "credential_exists"
                ? "这把密钥已经接入过（同协议去重）。"
                : "保存失败，请检查名称、Base URL 与密钥。",
        );
        return;
      }
      setProviderDone(true);
      setNotice("供应商已保存并加密。");
      // 连通性测试顺手跑一次；失败不阻塞——用户可以稍后在供应商页重试。
      if (data.provider?.id) {
        const test = await fetch(`/api/admin/providers/${data.provider.id}/test`, { method: "POST" });
        const result = (await test.json()) as TestResult;
        setTestResult(result);
      }
      setStep(3);
    } finally {
      setBusy(false);
    }
  }

  async function issueKey(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setError("");
    setBusy(true);
    try {
      const response = await fetch("/api/admin/keys", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ empId, count: 1 }),
      });
      const data = (await response.json()) as { keys?: Array<{ plaintext: string }>; error?: { code?: string } };
      if (!response.ok || !data.keys?.[0]) {
        setError("签发失败，请选择员工后重试。");
        return;
      }
      setIssuedKey(data.keys[0].plaintext);
      setTestResult(null);
    } finally {
      setBusy(false);
    }
  }

  async function sendTest() {
    setError("");
    setTestResult({ ok: false, status: null, latency_ms: 0, error: "testing" });
    const response = await fetch("/api/admin/onboarding/test-request", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ key: issuedKey, model: testModel.trim() }),
    });
    const data = (await response.json()) as TestResult & { error?: { code?: string } };
    if (!response.ok) {
      setTestResult({ ok: false, status: null, latency_ms: 0, error: data.error?.code ?? "invalid_request" });
      return;
    }
    setTestResult(data);
  }

  async function finish() {
    setError("");
    setBusy(true);
    try {
      const response = await fetch("/api/admin/onboarding/complete", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          skippedProvider: !providerDone && !hasProvider,
          keyIssued: Boolean(issuedKey),
          testOk: Boolean(testResult?.ok),
        }),
      });
      if (!response.ok) {
        const data = (await response.json()) as { error?: { code?: string } };
        setError(data.error?.code === "onboarding_incomplete" ? "前两步还没完成。" : "完成失败，请重试。");
        return;
      }
      setCompleted(true);
      router.refresh();
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="page">
      <div className="page-head">
        <div>
          <h1 className="page-title">欢迎使用 KeyKeep</h1>
          <p className="page-sub">三步完成首次配置，全程无需编辑任何配置文件。</p>
        </div>
      </div>

      <ol className="onboarding-steps" aria-label="向导进度">
        {STEPS.map((item) => (
          <li
            key={item.id}
            className={`onboarding-step${step === item.id ? " now" : ""}${step > item.id ? " done" : ""}`}
            aria-current={step === item.id ? "step" : undefined}
          >
            <span className="onboarding-step-no">{step > item.id ? "✓" : item.id}</span>
            <span>{item.title}</span>
          </li>
        ))}
      </ol>

      {error ? <div className="alert error" role="alert">{error}</div> : null}
      {notice ? <div className="alert ok" role="status">{notice}</div> : null}

      {completed ? (
        <section className="card panel">
          <div className="panel-head">
            <div>
              <h2 className="panel-title">全部完成 🎉</h2>
              <p className="panel-sub">配置已写入，审计事件已记录。下面是可以继续去的地方。</p>
            </div>
          </div>
          <div className="toolbar">
            <a className="btn btn-primary" href="/admin/keys">管理密钥</a>
            <a className="btn" href="/admin/audit?action=onboarding.complete">查看审计事件</a>
            {!providerDone ? <a className="btn" href="/admin/providers">接入真实供应商</a> : null}
          </div>
        </section>
      ) : step === 1 ? (
        <section className="card panel">
          <div className="panel-head">
            <div>
              <h2 className="panel-title">第 1 步 · 设置管理员密码</h2>
              <p className="panel-sub">初始口令是安装时随机生成的一次性密码，改完它才算接管这台网关。</p>
            </div>
          </div>
          <form onSubmit={changePassword}>
            <div className="field" style={{ maxWidth: 360 }}>
              <label htmlFor="ob-current">当前密码</label>
              <input
                id="ob-current"
                type="password"
                autoComplete="current-password"
                value={currentPassword}
                onChange={(event) => setCurrentPassword(event.target.value)}
                required
              />
            </div>
            <div className="form-row" style={{ maxWidth: 560 }}>
              <div className="field">
                <label htmlFor="ob-new">新密码（至少 12 个字符）</label>
                <input
                  id="ob-new"
                  type="password"
                  autoComplete="new-password"
                  value={newPassword}
                  onChange={(event) => setNewPassword(event.target.value)}
                  minLength={12}
                  required
                />
              </div>
              <div className="field">
                <label htmlFor="ob-confirm">确认新密码</label>
                <input
                  id="ob-confirm"
                  type="password"
                  autoComplete="new-password"
                  value={confirmPassword}
                  onChange={(event) => setConfirmPassword(event.target.value)}
                  minLength={12}
                  required
                />
              </div>
            </div>
            <button className="btn btn-primary" type="submit" disabled={busy}>
              {busy ? "保存中…" : "保存并继续"}
            </button>
            <p className="page-sub" style={{ marginTop: 8 }}>
              保存后本会话会立即失效（改密即全端下线），重新登录后向导会带你回到第 2 步。
            </p>
          </form>
        </section>
      ) : step === 2 ? (
        <>
          <section className="card panel">
            <div className="panel-head">
              <div>
                <h2 className="panel-title">第 2 步 · 接入首个供应商</h2>
                <p className="panel-sub">
                  填上游的 OpenAI 兼容端点与 API Key；凭据加密存储，页面上不再回显。
                </p>
              </div>
              <span className="tag-warn">可跳过</span>
            </div>
            <form onSubmit={saveProvider}>
              <div className="form-row">
                <div className="field">
                  <label htmlFor="ob-p-name">名称</label>
                  <input
                    id="ob-p-name"
                    value={provider.name}
                    onChange={(event) => setProvider({ ...provider, name: event.target.value })}
                    required
                  />
                </div>
                <div className="field" style={{ flex: 2 }}>
                  <label htmlFor="ob-p-base">Base URL（OpenAI 兼容）</label>
                  <input
                    id="ob-p-base"
                    value={provider.baseUrl}
                    onChange={(event) => setProvider({ ...provider, baseUrl: event.target.value })}
                    placeholder="https://api.example.com/v1"
                    required
                  />
                </div>
                <div className="field" style={{ flex: 2 }}>
                  <label htmlFor="ob-p-key">API Key</label>
                  <input
                    id="ob-p-key"
                    type="password"
                    value={provider.apiKey}
                    onChange={(event) => setProvider({ ...provider, apiKey: event.target.value })}
                    placeholder="加密存储，保存后不再显示"
                    required
                  />
                </div>
              </div>
              <div className="form-row">
                <div className="field">
                  <label htmlFor="ob-p-alias">模型别名（员工在客户端填的名字）</label>
                  <input
                    id="ob-p-alias"
                    value={provider.alias}
                    onChange={(event) => setProvider({ ...provider, alias: event.target.value })}
                    required
                  />
                </div>
                <div className="field" style={{ flex: 2 }}>
                  <label htmlFor="ob-p-model">上游模型名</label>
                  <input
                    id="ob-p-model"
                    value={provider.modelId}
                    onChange={(event) => setProvider({ ...provider, modelId: event.target.value })}
                    placeholder="例如 gpt-4o-mini / deepseek-chat"
                    required
                  />
                </div>
              </div>
              <div className="toolbar">
                <button className="btn btn-primary" type="submit" disabled={busy}>
                  {busy ? "保存中…" : "保存供应商"}
                </button>
                <button
                  type="button"
                  className="btn"
                  disabled={busy}
                  onClick={() => {
                    setProviderDone(true);
                    setNotice("已跳过供应商接入。" + (demoAvailable ? "内置演示上游已可用，第 3 步的模型别名填 demo。" : "稍后可在「供应商」页接入。"));
                    setTestModel(demoAvailable ? "demo" : testModel);
                    setStep(3);
                  }}
                >
                  跳过{demoAvailable ? "（演示上游已内置）" : ""}
                </button>
              </div>
            </form>
          </section>
          {testResult && testResult.error !== "testing" ? (
            <div className={`alert ${testResult.ok ? "ok" : "warn"}`} role="status">
              {testResult.ok
                ? `连通性测试通过，耗时 ${testResult.latency_ms} ms`
                : `连通性测试未通过（${testResult.error ?? "connection_failed"}）；不影响保存，稍后可在供应商页重试。`}
            </div>
          ) : null}
        </>
      ) : (
        <>
          <section className="card panel">
            <div className="panel-head">
              <div>
                <h2 className="panel-title">第 3 步 · 签发第一把虚拟 Key</h2>
                <p className="panel-sub">把 Key 发给自己（或演示用员工账号），马上用它跑通一次真实调用。</p>
              </div>
            </div>
            <form onSubmit={issueKey}>
              <div className="form-row" style={{ alignItems: "flex-end" }}>
                <div className="field">
                  <label htmlFor="ob-emp">员工</label>
                  <select id="ob-emp" value={empId} onChange={(event) => setEmpId(event.target.value)} required>
                    <option value="">选择员工</option>
                    {employees.map((user) => (
                      <option key={user.id} value={user.id}>
                        {user.displayName}（{user.account}）
                      </option>
                    ))}
                  </select>
                </div>
                <div className="field" style={{ maxWidth: 160 }}>
                  <label htmlFor="ob-model-alias">测试用模型别名</label>
                  <input
                    id="ob-model-alias"
                    value={testModel}
                    onChange={(event) => setTestModel(event.target.value)}
                    placeholder={demoAvailable ? "demo" : "第 2 步里填的别名"}
                  />
                </div>
                <div className="field" style={{ maxWidth: 140 }}>
                  <button className="btn btn-primary" type="submit" disabled={busy || !empId}>
                    <Icon name="key" size={14} />
                    {busy ? "签发中…" : "签发"}
                  </button>
                </div>
              </div>
            </form>
            {employees.length === 0 ? (
              <div className="alert warn" role="status">
                还没有员工账号，无法签发 Key。请先在
                <a href="/admin/org"> 人员管理 </a>
                新建一个员工（或直接运行安装包里的演示数据：桌面首装已预置「演示员工」，
                若这里为空说明该账号被停用了）。
              </div>
            ) : null}
            {issuedKey ? (
              <div className="alert warn" role="status">
                <strong>已签发一把 Key，请立即保存（明文只显示这一次）。</strong>
                <div className="key-plain">{issuedKey}</div>
                <div className="toolbar" style={{ marginTop: 8 }}>
                  <input
                    aria-label="测试用模型别名"
                    className="search"
                    style={{ maxWidth: 260 }}
                    value={testModel}
                    onChange={(event) => setTestModel(event.target.value)}
                    placeholder={demoAvailable ? "demo" : "第 2 步里填的别名"}
                  />
                  <button className="btn btn-primary" type="button" disabled={!testModel.trim() || testResult?.error === "testing"} onClick={() => void sendTest()}>
                    {testResult?.error === "testing" ? "发送中…" : "发送测试请求"}
                  </button>
                </div>
              </div>
            ) : null}
            {testResult && testResult.error !== "testing" ? (
              <div className={`alert ${testResult.ok ? "ok" : "error"}`} role="status">
                {testResult.ok
                  ? `测试请求成功（HTTP ${testResult.status}，${testResult.latency_ms} ms）——计量账本里已经能看到这条请求。`
                  : `测试请求失败（${testResult.error ?? `HTTP ${testResult.status}`}` +
                    (testResult.preview ? `：${testResult.preview}` : "") +
                    "）。检查模型别名与供应商配置后重试。"}
              </div>
            ) : null}
          </section>

          <section className="card panel">
            <div className="toolbar">
              <button className="btn btn-primary" type="button" disabled={busy || !canFinish} onClick={() => void finish()}>
                {busy ? "处理中…" : "完成向导"}
              </button>
              {!canFinish ? (
                <span className="page-sub">完成条件：已改密，且接入了供应商（或走了跳过）。</span>
              ) : (
                <span className="page-sub">完成后登录直接落在工作台，本页保留在 /admin/onboarding 可随时回看。</span>
              )}
            </div>
          </section>
        </>
      )}
    </div>
  );
}
