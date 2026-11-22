import { createHash, randomBytes, timingSafeEqual } from "node:crypto";

// 维护平面 P0 核心（FR-60/61/62/64/65/66/67）。
//
// 结构决定安全性质，这里把四条不变量钉在一个文件里：
// 1. 默认关闭（FR-60/RM-A01）：维护 API 与会话逻辑只在 KEYKEEP_MAINTENANCE_ENABLED=1
//    时工作；网关侧没有维护监听，控制面也不为维护开新端口——「入口」只可能是
//    控制台本身的已鉴权 API，缺省时全部返回 maintenance_disabled。
// 2. 工单闸（FR-61/RM-A02）：会话签发与续期都要求一张此刻有效的工单——存在、未撤销、
//    在窗口内。无工单 / 过期 / 他人（归属校验失败）三种连接全部拒绝，由调用方各记审计。
// 3. 短期凭据（FR-62/RM-A03/A04）：凭据 mk_… 明文只出现一次；库中只存 SHA-256；
//    TTL 与主机/账号/命令范围绑在会话行上，撤销（会话或工单级）即时生效——
//    判定是每次命令执行时查库，不做本地缓存，撤销 ≤1s 是查询语义而不是轮询语义。
// 4. 命令策略（FR-67/RM-A05）：白名单外命令直接拒绝（不进队列）；白名单内只读命令
//    直接执行；写命令（WRITE_KEYS 命中）进 PENDING 审批，未批准不执行。
//
// 「命令执行」本身在 P0 骨架里是一个受白名单约束的诊断通道（复用诊断包的脱敏口径），
// 不提供任意 shell——那属于设计文档 M4「不做」。

/** 维护平面总开关：缺省 = 关。只认显式值 "1" / "true"。 */
export function maintenanceEnabled(source: NodeJS.ProcessEnv = process.env): boolean {
  const value = source.KEYKEEP_MAINTENANCE_ENABLED ?? "";
  return value === "1" || value.toLowerCase() === "true";
}

// ---- 工单（FR-61）----

export type TicketValidity =
  | { ok: true }
  | { ok: false; reason: "no_ticket" | "not_found" | "expired" | "window_not_open" | "revoked" | "foreign_ticket" };

/**
 * 工单有效性：此刻「存在 + 已到窗口 + 未过期 + 未撤销 + 归属客户管理员」。
 * RM-A02 的三种拒绝情形（无/过期/他人）分别落在 not_found/expired/foreign_ticket；
 * 其余（未撤销、未开窗）是同一闸上的前置态，同样拒绝。
 */
export function checkTicket(
  ticket: {
    approvedAt: Date;
    expiresAt: Date;
    revokedAt: Date | null;
    createdById: string;
  } | null,
  now: Date,
  customerId: string,
): TicketValidity {
  if (!ticket) return { ok: false, reason: "not_found" };
  if (ticket.createdById !== customerId) return { ok: false, reason: "foreign_ticket" };
  if (ticket.revokedAt) return { ok: false, reason: "revoked" };
  if (now < ticket.approvedAt) return { ok: false, reason: "window_not_open" };
  if (now >= ticket.expiresAt) return { ok: false, reason: "expired" };
  return { ok: true };
}

// ---- 短期凭据（FR-62）----

/** 维护凭据明文前缀。与虚拟 Key（kk_）区分开，便于日志与审计里的形状识别。 */
export const MAINT_CRED_PREFIX = "mk_";
/** 凭据默认有效期上限（秒）：设计文档「任务级短期凭据」，最长 8 小时。 */
export const MAINT_CRED_TTL_MAX = 8 * 60 * 60;

export type IssuedMaintCred = {
  plaintext: string;
  credHash: Buffer;
  fp6: string;
};

export function issueMaintenanceCred(): IssuedMaintCred {
  const secret = randomBytes(32).toString("base64url");
  const plaintext = `${MAINT_CRED_PREFIX}${secret}`;
  return {
    plaintext,
    credHash: credHashOf(plaintext),
    fp6: createHash("sha256").update(plaintext, "utf8").digest("hex").slice(-6),
  };
}

export function credHashOf(plaintext: string): Buffer {
  return createHash("sha256").update(plaintext, "utf8").digest();
}

/** 凭据形状：mk_ + 43 位 base64url（与 kk_ 同长度口径，方便复用掩码规则）。 */
export function isCredFormat(value: string): boolean {
  return /^mk_[A-Za-z0-9_-]{43}$/.test(value);
}

export type SessionValidity =
  | { ok: true; sessionId: string; commandScope: string[]; host: string; account: string }
  | { ok: false; reason: SessionInvalidReason };

/** 会话拒绝原因：ticket_ 前缀 = 工单闸拒绝（原因透传 checkTicket）。 */
export type SessionInvalidReason =
  | "unknown_credential"
  | "expired"
  | "revoked"
  | "ended"
  | `ticket_${string}`;

/**
 * 会话（凭据）有效性：查库即时判定，撤销/过期立即生效（RM-A04 的 ≤1s）。
 * 工单在签发后仍可能被撤销或过期——会话随工单失效（工单撤销 = 一键撤销全部）。
 */
export function checkSession(
  session: {
    id: string;
    expiresAt: Date;
    revokedAt: Date | null;
    endedAt: Date | null;
    commandScope: string[];
    host: string;
    account: string;
    ticket: { approvedAt: Date; expiresAt: Date; revokedAt: Date | null; createdById: string } | null;
  } | null,
  now: Date,
  customerId: string,
): SessionValidity {
  if (!session) return { ok: false, reason: "unknown_credential" };
  if (session.revokedAt) return { ok: false, reason: "revoked" };
  if (session.endedAt) return { ok: false, reason: "ended" };
  if (now >= session.expiresAt) return { ok: false, reason: "expired" };
  const ticket = checkTicket(session.ticket, now, customerId);
  if (!ticket.ok) return { ok: false, reason: `ticket_${ticket.reason}` };
  return {
    ok: true,
    sessionId: session.id,
    commandScope: session.commandScope,
    host: session.host,
    account: session.account,
  };
}

// ---- 命令策略（FR-67 / RM-A05）----

/**
 * P0 白名单诊断命令：键 → 展示名。只读、无参数、输出走诊断脱敏口径。
 * 范围刻意与 buildDiagnostics() 的数据来源同族：表计数、迁移清单、环境变量存在性。
 */
export const READONLY_COMMANDS: Readonly<Record<string, string>> = {
  "diag.tables": "表行数计数（脱敏）",
  "diag.migrations": "迁移清单",
  "diag.env": "环境变量名与存在性（无原值）",
  "diag.version": "版本与脱敏模式",
};

/** 写命令键：白名单内但需要审批。P0 只登记形状，不提供执行器。 */
export const WRITE_COMMANDS: Readonly<Record<string, string>> = {
  "diag.dump_config": "导出脱敏后的配置差异（写诊断归档）",
};

export const ALL_COMMAND_KEYS: readonly string[] = [...Object.keys(READONLY_COMMANDS), ...Object.keys(WRITE_COMMANDS)];

export type CommandDecision =
  | { verdict: "execute"; commandKey: string; kind: "readonly" }
  | { verdict: "queue_approval"; commandKey: string; kind: "write" }
  | { verdict: "reject"; reason: "not_in_allowlist" | "unknown_command" };

/**
 * 命令策略判定（纯函数）：
 * - 白名单外（含未知键、任意 shell 文本）→ 直接拒绝，不进队列（RM-A05 硬判据）；
 * - 白名单内只读 → 直接执行；
 * - 白名单内写 → 待审批。
 */
export function decideCommand(rawCommand: string): CommandDecision {
  const commandKey = rawCommand.trim();
  if (commandKey in READONLY_COMMANDS) {
    return { verdict: "execute", commandKey, kind: "readonly" };
  }
  if (commandKey in WRITE_COMMANDS) {
    return { verdict: "queue_approval", commandKey, kind: "write" };
  }
  // 形状像白名单键但不在名单里 → unknown_command；其他任何文本（含 shell）→ not_in_allowlist。
  return { verdict: "reject", reason: /^[a-z.]+$/i.test(commandKey) ? "unknown_command" : "not_in_allowlist" };
}

/** 会话的命令范围（commandScope）校验：空数组 = 不限；否则要求命中其一。 */
export function commandAllowedByScope(commandKey: string, scope: string[]): boolean {
  if (scope.length === 0) return true;
  return scope.includes(commandKey);
}

// ---- 审计（FR-64 / RM-A07）----

/** 审计动作（登记进 lib/audit-actions.ts 的取值集合）。 */
export const MAINT_AUDIT_ACTIONS = {
  ticketApprove: "maintenance.ticket_approve",
  ticketRevoke: "maintenance.ticket_revoke",
  sessionConnect: "maintenance.session_connect",
  sessionConnectDenied: "maintenance.connect_denied",
  sessionRevoke: "maintenance.session_revoke",
  commandRun: "maintenance.command",
  commandDenied: "maintenance.command_denied",
  commandQueued: "maintenance.command_queued",
  approvalDecide: "maintenance.approval_decide",
  cleanup: "maintenance.cleanup",
} as const;

export type MaintAuditAction = (typeof MAINT_AUDIT_ACTIONS)[keyof typeof MAINT_AUDIT_ACTIONS];

/** 审计 detail 白名单（RM-A07 五类可关联：连接/审批人/命令/结果/撤销）。 */
export type MaintAuditDetail = {
  ticketId?: string;
  sessionId?: string;
  approvalId?: string;
  vendorName?: string;
  host?: string;
  account?: string;
  credFp6?: string;
  commandKey?: string;
  kind?: string;
  reason?: string;
  decision?: string;
  outcome?: string;
  exitCode?: number | null;
  decidedBy?: string;
  latencyMs?: number;
};

/**
 * 凭据校验（恒时比较）：先形状后哈希，避免无效输入进数据库查询。
 * 校验失败的具体原因只回「不匹配」，不区分「不存在」与「哈希不同」。
 */
export function credMatches(plaintext: string, credHash: Uint8Array): boolean {
  if (!isCredFormat(plaintext)) return false;
  const computed = credHashOf(plaintext);
  if (computed.length !== credHash.length) return false;
  return timingSafeEqual(computed, Buffer.from(credHash));
}
