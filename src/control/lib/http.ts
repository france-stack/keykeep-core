import { NextResponse } from "next/server";

export function json(data: unknown, status = 200): NextResponse {
  return NextResponse.json(data, { status });
}

/** 统一错误形状：{ error: { code, ...extra } }，只回分类码，不回上游原文。 */
export function apiError(code: string, status: number, extra: Record<string, unknown> = {}): NextResponse {
  return NextResponse.json({ error: { code, ...extra } }, { status });
}
