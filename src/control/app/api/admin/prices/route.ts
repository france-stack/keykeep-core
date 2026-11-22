import type { NextRequest } from "next/server";
import { NextResponse } from "next/server";

import { requireRoles, requireWritable } from "@/lib/api-auth";
import { auditRequestId, recordAudit } from "@/lib/audit";
import { prisma } from "@/lib/db";
import { apiError, json } from "@/lib/http";
import { num } from "@/lib/usage";

export const runtime = "nodejs";

const VERSION_RE = /^[A-Za-z0-9._-]{1,32}$/;
const MAX_PRICE = 1_000_000_000; // 分 / 1M token

type PriceBody = {
  provider?: unknown;
  modelId?: unknown;
  version?: unknown;
  effectiveFrom?: unknown;
  promptPerM?: unknown;
  completionPerM?: unknown;
  cacheReadPerM?: unknown;
  cacheWritePerM?: unknown;
};

function cents(value: unknown): number | null {
  const n = Math.floor(Number(value ?? 0));
  if (!Number.isFinite(n) || n < 0 || n > MAX_PRICE) return null;
  return n;
}

export async function GET(request: NextRequest) {
  const auth = await requireRoles(request, ["admin"]);
  if (auth instanceof NextResponse) return auth;

  // 来源筛选必须在服务端做：ModelPrice 有 8000+ 行目录数据，
  // 客户端拿 LIMIT 500 的窗口再过滤，手工发布的行会被目录数据挤出窗口，看起来像"没配过"。
  const rawSource = request.nextUrl.searchParams.get("source") ?? "all";
  const source = rawSource === "manual" || rawSource === "catalog" ? rawSource : "all";

  // 时间用数据库 epoch 读，绕开驱动适配器的时区偏移。
  const rows = await prisma.$queryRaw<Array<{
    provider: string;
    modelId: string;
    version: string;
    effective_epoch: bigint;
    promptPerM: bigint;
    completionPerM: bigint;
    cacheReadPerM: bigint;
    cacheWritePerM: bigint;
  }>>`SELECT provider, "modelId", version,
           (extract(epoch from "effectiveFrom"))::bigint AS effective_epoch,
           "promptPerM", "completionPerM", "cacheReadPerM", "cacheWritePerM"
      FROM "ModelPrice"
     WHERE (${source === "all"} OR (${source === "catalog"}) = (version LIKE 'models.dev-%'))
     ORDER BY "modelId", "effectiveFrom" DESC
     LIMIT 500`;
  const totals = await prisma.$queryRaw<Array<{ catalog: bigint; manual: bigint }>>`
      SELECT COUNT(*) FILTER (WHERE version LIKE 'models.dev-%') AS catalog,
             COUNT(*) FILTER (WHERE version NOT LIKE 'models.dev-%') AS manual
        FROM "ModelPrice"`;
  const now = Math.floor(Date.now() / 1000);
  const catalogCount = Number(totals[0]?.catalog ?? 0);
  const manualCount = Number(totals[0]?.manual ?? 0);
  return json({
    total: { catalog: catalogCount, manual: manualCount, all: catalogCount + manualCount },
    prices: rows.map((row) => ({
      provider: row.provider,
      modelId: row.modelId,
      version: row.version,
      effectiveFrom: new Date(Number(row.effective_epoch) * 1000).toISOString(),
      effective: Number(row.effective_epoch) <= now,
      promptPerM: num(row.promptPerM),
      completionPerM: num(row.completionPerM),
      cacheReadPerM: num(row.cacheReadPerM),
      cacheWritePerM: num(row.cacheWritePerM),
    })),
  });
}

export async function POST(request: NextRequest) {
  const auth = await requireRoles(request, ["admin"]);
  if (auth instanceof NextResponse) return auth;
  const readOnly = await requireWritable(request, auth.sub);
  if (readOnly) return readOnly;

  let body: PriceBody;
  try {
    body = (await request.json()) as PriceBody;
  } catch {
    return apiError("invalid_request", 400);
  }

  const provider = typeof body.provider === "string" ? body.provider.trim() : "";
  const modelId = typeof body.modelId === "string" ? body.modelId.trim() : "";
  const version = typeof body.version === "string" ? body.version.trim() : "";
  if (!provider || !modelId || !VERSION_RE.test(version)) return apiError("invalid_request", 400);

  const prices = {
    promptPerM: cents(body.promptPerM),
    completionPerM: cents(body.completionPerM),
    cacheReadPerM: cents(body.cacheReadPerM),
    cacheWritePerM: cents(body.cacheWritePerM),
  };
  if (Object.values(prices).some((value) => value === null)) return apiError("invalid_request", 400);

  let effectiveFrom = new Date();
  if (typeof body.effectiveFrom === "string" && body.effectiveFrom) {
    const parsed = new Date(body.effectiveFrom);
    if (Number.isNaN(parsed.getTime())) return apiError("invalid_request", 400);
    effectiveFrom = parsed;
  }

  // 用 SQL 原生 now()/timestamptz 写入：Prisma 适配器序列化 Date 时带时区偏移，
  // 直接传 Date 会把生效时间写成 8 小时前/后。
  const effectiveIso = body.effectiveFrom ? effectiveFrom.toISOString() : null;
  try {
    await prisma.$executeRaw`INSERT INTO "ModelPrice"
      ("provider", "modelId", "version", "effectiveFrom",
       "promptPerM", "completionPerM", "cacheReadPerM", "cacheWritePerM")
      VALUES (${provider}, ${modelId}, ${version}, COALESCE(${effectiveIso}::timestamptz, now()),
              ${prices.promptPerM ?? 0}, ${prices.completionPerM ?? 0},
              ${prices.cacheReadPerM ?? 0}, ${prices.cacheWritePerM ?? 0})`;
    const price = { provider, modelId, version, effectiveFrom };
    await recordAudit({
      actorId: auth.sub,
      action: "price.publish",
      detail: { provider, modelId, version, effectiveFrom: effectiveFrom.toISOString(), ...prices },
      requestId: auditRequestId(request),
    });
    return json(
      {
        price: {
          provider: price.provider,
          modelId: price.modelId,
          version: price.version,
          effectiveFrom: price.effectiveFrom,
        },
      },
      201,
    );
  } catch {
    return apiError("conflict", 409, { reason: "version_exists" });
  }
}
