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

type Payload = {
  name?: unknown;
  pattern?: unknown;
  kind?: unknown;
  target?: unknown;
  replacement?: unknown;
  priority?: unknown;
  enabled?: unknown;
};

function parsePatch(body: Payload): { ok: true; value: Record<string, unknown> } | { ok: false } {
  const value: Record<string, unknown> = {};
  if (body.name !== undefined) {
    if (typeof body.name !== "string" || !body.name.trim() || body.name.length > 64) return { ok: false };
    value.name = body.name.trim();
  }
  if (body.pattern !== undefined) {
    if (typeof body.pattern !== "string" || !body.pattern || body.pattern.length > 512) return { ok: false };
    if ((body.kind ?? "regex") === "regex") {
      // 执行方是 Go 网关的 regexp（RE2，免疫回溯型 ReDoS）；这里只做形状闸。
      if (unsafeRegexShape(body.pattern) !== null) return { ok: false };
    }
    value.pattern = body.pattern;
  }
  if (body.kind !== undefined) {
    if (typeof body.kind !== "string" || !KINDS.has(body.kind)) return { ok: false };
    value.kind = body.kind;
  }
  if (body.target !== undefined) {
    if (typeof body.target !== "string" || !TARGETS.has(body.target)) return { ok: false };
    value.target = body.target;
  }
  if (body.replacement !== undefined) {
    if (typeof body.replacement !== "string") return { ok: false };
    value.replacement = body.replacement.slice(0, 64);
  }
  if (body.priority !== undefined) {
    const priority = Number(body.priority);
    if (!Number.isInteger(priority) || priority < 1 || priority > 1000) return { ok: false };
    value.priority = priority;
  }
  if (body.enabled !== undefined) value.enabled = Boolean(body.enabled);
  if (Object.keys(value).length === 0) return { ok: false };
  return { ok: true, value };
}

export async function PATCH(request: NextRequest, context: { params: Promise<{ id: string }> }) {
  const auth = await requireRoles(request, ["admin"]);
  if (auth instanceof NextResponse) return auth;
  const readOnly = await requireWritable(request, auth.sub);
  if (readOnly) return readOnly;

  const { id } = await context.params;
  let body: Payload;
  try {
    body = (await request.json()) as Payload;
  } catch {
    return apiError("invalid_request", 400);
  }
  const parsed = parsePatch(body);
  if (!parsed.ok) return apiError("invalid_request", 400);

  try {
    const rule = await prisma.redactRule.update({ where: { id }, data: parsed.value as never });
    await recordAudit({
      actorId: auth.sub,
      action: "redact.rule_update",
      detail: { ruleId: rule.id, name: rule.name, enabled: rule.enabled },
      requestId: auditRequestId(request),
    });
    return json({ rule });
  } catch {
    return apiError("not_found", 404);
  }
}

export async function DELETE(request: NextRequest, context: { params: Promise<{ id: string }> }) {
  const auth = await requireRoles(request, ["admin"]);
  if (auth instanceof NextResponse) return auth;
  const readOnly = await requireWritable(request, auth.sub);
  if (readOnly) return readOnly;

  const { id } = await context.params;
  try {
    const rule = await prisma.redactRule.delete({ where: { id } });
    await recordAudit({
      actorId: auth.sub,
      action: "redact.rule_delete",
      detail: { ruleId: rule.id, name: rule.name },
      requestId: auditRequestId(request),
    });
    return json({ ok: true });
  } catch {
    return apiError("not_found", 404);
  }
}
