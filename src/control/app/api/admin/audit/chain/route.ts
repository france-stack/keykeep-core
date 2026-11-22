import type { NextRequest } from "next/server";
import { NextResponse } from "next/server";

import { requireRoles } from "@/lib/api-auth";
import { auditChainHead, auditWriteHealth, verifyStoredAuditChain } from "@/lib/audit";
import { json } from "@/lib/http";

export const runtime = "nodejs";
// 全量走链要分页读表；百万行时 15 s 默认值会掐断，给出上限。
export const maxDuration = 120;

/**
 * GET：链头锚点 + 写入失败计数（轻量，随时可看）。
 * 锚点必须能被抄到链外（工单/归档），否则"删掉尾巴重算"这件事永远看不出来。
 */
export async function GET(request: NextRequest) {
  const auth = await requireRoles(request, ["admin"]);
  if (auth instanceof NextResponse) return auth;

  return json({ head: await auditChainHead(), writes: auditWriteHealth() });
}

/**
 * POST：全量校验。走链是 O(表大小) 的读+哈希，放 GET 等于让任意一次图片加载
 * 都能触发最重的管理端查询，所以留在带 CSRF 保护的 POST 上。
 */
export async function POST(request: NextRequest) {
  const auth = await requireRoles(request, ["admin"]);
  if (auth instanceof NextResponse) return auth;

  const report = await verifyStoredAuditChain();
  return json({ report, writes: auditWriteHealth() });
}
