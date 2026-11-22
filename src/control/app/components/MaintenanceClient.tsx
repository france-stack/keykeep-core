"use client";

import { useCallback, useEffect, useState } from "react";

type Ticket = {
  id: string;
  vendorName: string;
  reason: string;
  approvedAt: string;
  expiresAt: string;
  revokedAt: string | null;
  createdBy: { id: string; account: string; displayName: string };
  sessions: Array<{
    id: string;
    credFp6: string;
    host: string;
    account: string;
    revokedAt: string | null;
    endedAt: string | null;
    expiresAt: string;
  }>;
};

type Session = {
  id: string;
  ticketId: string;
  host: string;
  account: string;
  commandScope: string[];
  expiresAt: string;
  credential?: string;
};

type Approval = {
  id: string;
  sessionId: string;
  command: string;
  commandKey: string;
  kind: string;
  state: string;
  requestedAt: string;
  decidedAt: string | null;
  session?: { host: string; credFp6: string };
};

type TimelineRow = {
  at: string;
  action: string;
  actorId: string | null;
  ticketId: string | null;
  sessionId: string | null;
  host: string | null;
  commandKey: string | null;
  reason: string | null;
  decision: string | null;
};

const COMMAND_CHOICES = [
  { key: "diag.tables", label: "表行数计数（只读）" },
  { key: "diag.migrations", label: "迁移清单（只读）" },
  { key: "diag.env", label: "环境变量存在性（只读）" },
  { key: "diag.version", label: "版本与脱敏模式（只读）" },
  { key: "diag.dump_config", label: "导出配置差异（写·需审批）" },
];

function formatTime(value: string | null): string {
  if (!value) return "—";
  return new Date(value).toLocaleString("zh-CN", { hour12: false });
}

export default function MaintenanceClient({ enabled }: { enabled: boolean }) {
  const [tickets, setTickets] = useState<Ticket[]>([]);
  const [approvals, setApprovals] = useState<Approval[]>([]);
  const [timeline, setTimeline] = useState<TimelineRow[]>([]);
  const [error, setError] = useState("");
  const [issued, setIssued] = useState<Session | null>(null);

  // 工单表单
  const [vendorName, setVendorName] = useState("");
  const [reason, setReason] = useState("");
  // 会话表单
  const [sessionTicketId, setSessionTicketId] = useState("");
  const [sessionHost, setSessionHost] = useState("");
  const [sessionScope, setSessionScope] = useState<string[]>([]);
  // 命令
  const [credential, setCredential] = useState("");
  const [command, setCommand] = useState(COMMAND_CHOICES[0].key);
  const [commandResult, setCommandResult] = useState("");
  // 审计过滤
  const [auditSessionId, setAuditSessionId] = useState("");

  const load = useCallback(async () => {
    if (!enabled) return;
    const [ticketsRes, approvalsRes, auditRes] = await Promise.all([
      fetch("/api/admin/maintenance/tickets"),
      fetch("/api/admin/maintenance/approvals?state=all"),
      fetch("/api/admin/maintenance/audit"),
    ]);
    if (ticketsRes.status === 403 || approvalsRes.status === 403) {
      setError("维护通道未开启（KEYKEEP_MAINTENANCE_ENABLED）。");
      return;
    }
    if (!ticketsRes.ok || !approvalsRes.ok || !auditRes.ok) {
      setError("加载维护数据失败。");
      return;
    }
    setError("");
    const ticketsPayload = (await ticketsRes.json()) as { tickets: Ticket[] };
    const approvalsPayload = (await approvalsRes.json()) as { approvals: Approval[] };
    const auditPayload = (await auditRes.json()) as { timeline: TimelineRow[] };
    setTickets(ticketsPayload.tickets);
    setApprovals(approvalsPayload.approvals);
    setTimeline(auditPayload.timeline);
  }, [enabled]);

  useEffect(() => {
    void load();
  }, [load]);

  if (!enabled) {
    return (
      <div className="card">
        <h2 className="page-title">维护通道未开启</h2>
        <p className="page-sub">
          维护平面默认关闭：全新安装没有任何维护监听或入口。需要厂商远程维护时，
          由管理员在控制面配置 <code>KEYKEEP_MAINTENANCE_ENABLED=1</code> 显式开启；
          网关进程不包含维护组件，关闭期间转发面不受影响。
        </p>
      </div>
    );
  }

  async function approveTicket() {
    const response = await fetch("/api/admin/maintenance/tickets", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ vendorName, reason }),
    });
    if (!response.ok) {
      const payload = (await response.json().catch(() => ({}))) as { error?: { code?: string } };
      setError(`签发工单失败：${payload.error?.code ?? response.status}`);
      return;
    }
    setVendorName("");
    setReason("");
    await load();
  }

  async function issueSession() {
    setIssued(null);
    const response = await fetch("/api/admin/maintenance/sessions", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({
        ticketId: sessionTicketId,
        host: sessionHost,
        commandScope: sessionScope,
      }),
    });
    const payload = (await response.json().catch(() => ({}))) as {
      session?: Session;
      error?: { code?: string };
    };
    if (!response.ok) {
      setError(`签发会话失败：${payload.error?.code ?? response.status}`);
      return;
    }
    setCredential(payload.session?.credential ?? "");
    setIssued(payload.session ?? null);
    await load();
  }

  async function revokeTicket(id: string) {
    const response = await fetch(`/api/admin/maintenance/tickets/${id}/revoke`, { method: "POST" });
    if (!response.ok) {
      setError("撤销工单失败。");
      return;
    }
    await load();
  }

  async function revokeSession(id: string) {
    const response = await fetch(`/api/admin/maintenance/sessions/${id}/revoke`, { method: "POST" });
    if (!response.ok) {
      setError("撤销会话失败。");
      return;
    }
    await load();
  }

  async function runCommand() {
    setCommandResult("");
    const response = await fetch("/api/admin/maintenance/commands", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ credential, command }),
    });
    const payload = (await response.json().catch(() => ({}))) as {
      status?: string;
      result?: Record<string, unknown>;
      approvalId?: string;
      error?: { code?: string };
    };
    if (payload.status === "executed") {
      setCommandResult(JSON.stringify(payload.result, null, 2));
    } else if (payload.status === "pending_approval") {
      setCommandResult(`写命令已进入待审批队列（approvalId=${payload.approvalId}）。`);
    } else {
      setCommandResult(`命令被拒绝：${payload.error?.code ?? response.status}`);
    }
    await load();
  }

  async function decideApproval(approvalId: string, decision: "approve" | "reject") {
    const response = await fetch("/api/admin/maintenance/approvals", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ approvalId, decision }),
    });
    if (!response.ok) {
      setError("审批操作失败。");
      return;
    }
    await load();
  }

  async function runCleanup() {
    const response = await fetch("/api/admin/maintenance/cleanup", { method: "POST" });
    if (!response.ok) {
      setError("清理失败。");
      return;
    }
    const payload = (await response.json()) as { endedSessions: number; activeSessions: number };
    setCommandResult(`清理完成：结束 ${payload.endedSessions} 个过期会话，当前活跃 ${payload.activeSessions} 个。`);
    await load();
  }

  return (
    <>
      {error && <div className="card" style={{ borderColor: "var(--danger, #d33)" }}>{error}</div>}

      <div className="card">
        <h2 className="page-title">维护工单</h2>
        <p className="page-sub">批准一张工单 = 授权某位厂商运维人员在窗口期内建立维护会话。撤销工单立即终结其下全部会话。</p>
        <div className="toolbar">
          <input
            aria-label="厂商运维人员"
            placeholder="厂商运维人员"
            value={vendorName}
            onChange={(event) => setVendorName(event.target.value)}
          />
          <input
            aria-label="工单事由"
            placeholder="工单事由"
            value={reason}
            onChange={(event) => setReason(event.target.value)}
          />
          <button type="button" onClick={approveTicket} disabled={!vendorName.trim()}>
            批准工单（默认 4 小时窗口）
          </button>
        </div>
        <table className="table">
          <thead>
            <tr>
              <th>工单</th>
              <th>厂商</th>
              <th>窗口</th>
              <th>状态</th>
              <th>操作</th>
            </tr>
          </thead>
          <tbody>
            {tickets.map((ticket) => {
              const state = ticket.revokedAt ? "已撤销" : new Date(ticket.expiresAt) < new Date() ? "已过期" : "有效";
              return (
                <tr key={ticket.id}>
                  <td title={ticket.reason}>{ticket.id.slice(0, 8)}…</td>
                  <td>{ticket.vendorName}</td>
                  <td>{formatTime(ticket.approvedAt)} — {formatTime(ticket.expiresAt)}</td>
                  <td>{state}</td>
                  <td>
                    {!ticket.revokedAt && (
                      <button type="button" onClick={() => revokeTicket(ticket.id)}>
                        一键撤销
                      </button>
                    )}
                  </td>
                </tr>
              );
            })}
            {tickets.length === 0 && (
              <tr><td colSpan={5}>暂无工单。</td></tr>
            )}
          </tbody>
        </table>
      </div>

      <div className="card">
        <h2 className="page-title">维护会话（短期凭据）</h2>
        <p className="page-sub">凭据绑定工单、主机、账号、命令范围与 TTL；明文只显示一次；撤销后 ≤1 秒失效。</p>
        <div className="toolbar">
          <select aria-label="工单" value={sessionTicketId} onChange={(event) => setSessionTicketId(event.target.value)}>
            <option value="">选择工单…</option>
            {tickets.filter((ticket) => !ticket.revokedAt).map((ticket) => (
              <option key={ticket.id} value={ticket.id}>
                {ticket.vendorName}（{ticket.id.slice(0, 8)}…）
              </option>
            ))}
          </select>
          <input
            aria-label="限定主机"
            placeholder="限定主机（如 127.0.0.1）"
            value={sessionHost}
            onChange={(event) => setSessionHost(event.target.value)}
          />
          {COMMAND_CHOICES.filter((choice) => choice.key.startsWith("diag.")).map((choice) => (
            <label key={choice.key} style={{ display: "inline-flex", gap: 4, alignItems: "center" }}>
              <input
                type="checkbox"
                checked={sessionScope.includes(choice.key)}
                onChange={(event) => {
                  setSessionScope((current) =>
                    event.target.checked
                      ? [...current, choice.key]
                      : current.filter((key) => key !== choice.key),
                  );
                }}
              />
              {choice.key}
            </label>
          ))}
          <button type="button" onClick={issueSession} disabled={!sessionTicketId || !sessionHost.trim()}>
            签发会话凭据
          </button>
        </div>
        {credential && (
          <p className="page-sub">
            维护凭据（只显示一次，请立即交给厂商）：<code>{credential}</code>
          </p>
        )}
        {tickets.flatMap((ticket) =>
          ticket.sessions.map((session) => (
            <div key={session.id} className="toolbar">
              <span>{session.credFp6}</span>
              <span>{session.host}</span>
              <span>{session.account}</span>
              <span>到期 {formatTime(session.expiresAt)}</span>
              {!session.revokedAt && !session.endedAt && (
                <button type="button" onClick={() => revokeSession(session.id)}>
                  撤销会话
                </button>
              )}
            </div>
          )),
        )}
      </div>

      <div className="card">
        <h2 className="page-title">维护命令（白名单）</h2>
        <p className="page-sub">只读命令直接执行；写命令进入待审批；白名单外命令直接拒绝（RM-A05）。</p>
        <div className="toolbar">
          <input
            aria-label="会话凭据"
            placeholder="mk_…（会话凭据）"
            value={credential}
            onChange={(event) => setCredential(event.target.value)}
          />
          <select aria-label="命令" value={command} onChange={(event) => setCommand(event.target.value)}>
            {COMMAND_CHOICES.map((choice) => (
              <option key={choice.key} value={choice.key}>{choice.label}</option>
            ))}
          </select>
          <button type="button" onClick={runCommand} disabled={!credential.trim()}>
            执行
          </button>
        </div>
        {commandResult && <pre style={{ whiteSpace: "pre-wrap" }}>{commandResult}</pre>}
      </div>

      <div className="card">
        <h2 className="page-title">写命令审批</h2>
        <p className="page-sub">未批准的写命令不执行；拒绝也记录在案。</p>
        <table className="table">
          <thead>
            <tr>
              <th>命令</th>
              <th>会话主机</th>
              <th>状态</th>
              <th>申请时间</th>
              <th>操作</th>
            </tr>
          </thead>
          <tbody>
            {approvals.map((approval) => (
              <tr key={approval.id}>
                <td><code>{approval.commandKey}</code></td>
                <td>{approval.session?.host ?? "—"}</td>
                <td>{approval.state}</td>
                <td>{formatTime(approval.requestedAt)}</td>
                <td>
                  {approval.state === "pending" && (
                    <>
                      <button type="button" onClick={() => decideApproval(approval.id, "approve")}>批准</button>
                      <button type="button" onClick={() => decideApproval(approval.id, "reject")}>拒绝</button>
                    </>
                  )}
                </td>
              </tr>
            ))}
            {approvals.length === 0 && (
              <tr><td colSpan={5}>暂无审批记录。</td></tr>
            )}
          </tbody>
        </table>
      </div>

      <div className="card">
        <div className="toolbar">
          <h2 className="page-title">维护审计</h2>
          <button type="button" onClick={runCleanup}>清理过期会话</button>
          <input
            aria-label="按会话过滤"
            placeholder="按 sessionId 过滤"
            value={auditSessionId}
            onChange={(event) => setAuditSessionId(event.target.value)}
          />
        </div>
        <p className="page-sub">连接 / 审批 / 命令 / 结果 / 撤销五类事件可关联查询（RM-A07）。</p>
        <table className="table">
          <thead>
            <tr>
              <th>时间</th>
              <th>动作</th>
              <th>会话</th>
              <th>主机</th>
              <th>命令/原因</th>
            </tr>
          </thead>
          <tbody>
            {timeline
              .filter((row) => !auditSessionId || row.sessionId === auditSessionId)
              .map((row, index) => (
                <tr key={`${row.at}-${index}`}>
                  <td>{formatTime(row.at)}</td>
                  <td>{row.action}</td>
                  <td>{row.sessionId ? row.sessionId.slice(0, 8) + "…" : "—"}</td>
                  <td>{row.host ?? "—"}</td>
                  <td>{row.commandKey ?? row.reason ?? row.decision ?? "—"}</td>
                </tr>
              ))}
            {timeline.length === 0 && (
              <tr><td colSpan={5}>暂无维护审计。</td></tr>
            )}
          </tbody>
        </table>
      </div>
    </>
  );
}
