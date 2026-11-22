import type { NextRequest } from "next/server";
import { NextResponse } from "next/server";

import { requireRoles, requireWritable } from "@/lib/api-auth";
import { auditRequestId, recordAudit } from "@/lib/audit";
import { prisma } from "@/lib/db";
import { apiError, json } from "@/lib/http";
import {
  MAINT_AUDIT_ACTIONS,
  READONLY_COMMANDS,
  checkSession,
  commandAllowedByScope,
  credMatches,
  decideCommand,
  maintenanceEnabled,
} from "@/lib/maintenance";

export const runtime = "nodejs";

/**
 * 维护命令通道（RM-A05 判定对象）。
 *
 * 鉴权顺序：维护开关 → 会话凭据（mk_…）查库即时校验（撤销/过期/工单失效即拒）
 * → 会话 scope（commandScope 空数组 = 工单给全量白名单；非空 = 只允许列出的键）
 * → 命令策略（白名单外直接拒绝；只读直接执行；写进待审批）。
 * 全流程不出现 shell 文本执行——命令是登记过的键，不是任意字符串。
 */
export async function POST(request: NextRequest) {
  const auth = await requireRoles(request, ["admin"]);
  if (auth instanceof NextResponse) return auth;
  if (!maintenanceEnabled()) return apiError("maintenance_disabled", 403);
  const readOnly = await requireWritable(request, auth.sub);
  if (readOnly) return readOnly;

  let body: { credential?: unknown; command?: unknown };
  try {
    body = (await request.json()) as typeof body;
  } catch {
    return apiError("invalid_request", 400);
  }
  const credential = typeof body.credential === "string" ? body.credential : "";
  const command = typeof body.command === "string" ? body.command : "";
  if (!credential || !command) return apiError("invalid_request", 400);

  // 1) 凭据 → 会话（查库即时判定：撤销/过期/工单失效 ≤1s 生效）。
  const session = await findSessionByCred(credential);
  const now = new Date();
  const validity = checkSession(session, now, auth.sub);
  if (!validity.ok) {
    await recordAudit({
      action: MAINT_AUDIT_ACTIONS.commandDenied,
      detail: { reason: validity.reason, credFp6: session?.credFp6 ?? fpOf(credential) },
      requestId: auditRequestId(request),
    });
    return apiError(`session_${validity.reason}`, 401);
  }

  // 2) 会话范围：commandScope 非空时命令键必须在列。
  if (!commandAllowedByScope(command, validity.commandScope)) {
    await recordAudit({
      action: MAINT_AUDIT_ACTIONS.commandDenied,
      detail: { sessionId: validity.sessionId, commandKey: command.slice(0, 64), reason: "scope" },
      requestId: auditRequestId(request),
    });
    return apiError("command_out_of_scope", 403);
  }

  // 3) 命令策略：白名单外直接拒绝（不进审批队列）。
  const decision = decideCommand(command);
  if (decision.verdict === "reject") {
    await recordAudit({
      action: MAINT_AUDIT_ACTIONS.commandDenied,
      detail: { sessionId: validity.sessionId, command: command.slice(0, 200), reason: decision.reason },
      requestId: auditRequestId(request),
    });
    return apiError(decision.reason, 403);
  }

  if (decision.verdict === "execute") {
    // 4a) 白名单内只读命令：直接执行（P0 骨架 = 从诊断包数据源取同族脱敏数据）。
    const result = await runReadonlyCommand(decision.commandKey);
    await recordAudit({
      action: MAINT_AUDIT_ACTIONS.commandRun,
      detail: { sessionId: validity.sessionId, commandKey: decision.commandKey, kind: "readonly", outcome: "ok" },
      requestId: auditRequestId(request),
    });
    return json({ status: "executed", commandKey: decision.commandKey, result });
  }

  // 4b) 白名单内写命令：进待审批队列，未批准不执行（RM-A05）。
  const approval = await prisma.maintenanceApproval.create({
    data: {
      sessionId: validity.sessionId,
      command,
      kind: "write",
      state: "pending",
      commandKey: decision.commandKey,
    },
  });
  await recordAudit({
    action: MAINT_AUDIT_ACTIONS.commandQueued,
    detail: { sessionId: validity.sessionId, approvalId: approval.id, commandKey: decision.commandKey, kind: "write" },
    requestId: auditRequestId(request),
  });
  return json({ status: "pending_approval", approvalId: approval.id }, 202);
}

function fpOf(credential: string): string {
  // 无效凭据也只记指纹前 6 位（形状校验不过时可能是任意文本，截断防滥用）。
  return credential.slice(0, 6);
}

async function findSessionByCred(credential: string) {
  const candidates = await prisma.maintenanceSession.findMany({
    where: { revokedAt: null, endedAt: null, expiresAt: { gt: new Date() } },
    include: { ticket: true },
    take: 50,
  });
  for (const session of candidates) {
    if (credMatches(credential, session.credHash)) return session;
  }
  return null;
}

async function runReadonlyCommand(commandKey: string): Promise<Record<string, unknown>> {
  switch (commandKey) {
    case "diag.tables": {
      const [users, keys, tickets, sessions] = await Promise.all([
        prisma.user.count(),
        prisma.apiKey.count(),
        prisma.maintenanceTicket.count(),
        prisma.maintenanceSession.count(),
      ]);
      return { users, keys, tickets, sessions };
    }
    case "diag.migrations": {
      const rows = await prisma.$queryRawUnsafe<Array<{ migration_name: string }>>(
        'SELECT migration_name FROM "_prisma_migrations" WHERE finished_at IS NOT NULL ORDER BY finished_at',
      ).catch(() => []);
      return { migrations: rows.map((row) => row.migration_name) };
    }
    case "diag.env": {
      return {
        env: ["DATABASE_URL", "KEYKEEP_KEK", "SESSION_SECRET", "KEYKEEP_MAINTENANCE_ENABLED"].map((name) => ({
          name,
          present: Boolean(process.env[name]),
        })),
      };
    }
    case "diag.version":
      return {
        version: process.env.KEYKEEP_VERSION ?? "0.1.0",
        maskMode: process.env.KEYKEEP_MASK_MODE ?? "rules-only",
      };
    default:
      return {};
  }
}
