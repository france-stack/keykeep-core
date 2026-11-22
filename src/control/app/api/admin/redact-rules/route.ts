import type { NextRequest } from "next/server";
import { NextResponse } from "next/server";

import { requireRoles, requireWritable } from "@/lib/api-auth";
import { auditRequestId, recordAudit } from "@/lib/audit";
import { prisma } from "@/lib/db";
import { apiError, json } from "@/lib/http";
import { unsafeRegexShape } from "@/lib/regex-shape";

export const runtime = "nodejs";

const KINDS = new Set(["regex", "literal"]);
const TARGETS = new Set(["request", "log", "diagnostic", "all"]);
const MAX_PATTERN = 512;

type Payload = {
  name?: unknown;
  pattern?: unknown;
  kind?: unknown;
  target?: unknown;
  replacement?: unknown;
  priority?: unknown;
  enabled?: unknown;
};

function parsePayload(body: Payload): { ok: true; value: Record<string, unknown> } | { ok: false } {
  const name = typeof body.name === "string" ? body.name.trim() : "";
  const pattern = typeof body.pattern === "string" ? body.pattern : "";
  const kind = typeof body.kind === "string" ? body.kind : "regex";
  const target = typeof body.target === "string" ? body.target : "all";
  const replacement = typeof body.replacement === "string" ? body.replacement.slice(0, 64) : "";
  const priority = Number(body.priority ?? 100);
  const enabled = body.enabled === undefined ? true : Boolean(body.enabled);

  if (!name || name.length > 64) return { ok: false };
  if (!pattern || pattern.length > MAX_PATTERN) return { ok: false };
  if (!KINDS.has(kind) || !TARGETS.has(target)) return { ok: false };
  if (!Number.isInteger(priority) || priority < 1 || priority > 1000) return { ok: false };
  if (kind === "regex") {
    // 执行方是 Go 网关的 regexp（RE2 语义：线性时间，免疫回溯型 ReDoS）。
    // 这里只做入库前的形状闸（见 lib/regex-shape.ts）：编译校验由网关
    // 加载时完成，非法规则被跳过并记日志。
    if (unsafeRegexShape(pattern) !== null) return { ok: false };
  }
  return { ok: true, value: { name, pattern, kind, target, replacement, priority, enabled } };
}

export async function GET(request: NextRequest) {
  const auth = await requireRoles(request, ["admin"]);
  if (auth instanceof NextResponse) return auth;

  const rules = await prisma.redactRule.findMany({ orderBy: [{ priority: "asc" }, { name: "asc" }] });
  return json({ rules });
}

export async function POST(request: NextRequest) {
  const auth = await requireRoles(request, ["admin"]);
  if (auth instanceof NextResponse) return auth;
  const readOnly = await requireWritable(request, auth.sub);
  if (readOnly) return readOnly;

  let body: Payload;
  try {
    body = (await request.json()) as Payload;
  } catch {
    return apiError("invalid_request", 400);
  }
  const parsed = parsePayload(body);
  if (!parsed.ok) return apiError("invalid_request", 400);

  try {
    const rule = await prisma.redactRule.create({ data: parsed.value as never });
    await recordAudit({
      actorId: auth.sub,
      action: "redact.rule_create",
      detail: { ruleId: rule.id, name: rule.name, kind: rule.kind, target: rule.target },
      requestId: auditRequestId(request),
    });
    return json({ rule }, 201);
  } catch {
    return apiError("rule_name_conflict", 409);
  }
}
