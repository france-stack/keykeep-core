import type { NextRequest } from "next/server";

import { readSession } from "@/lib/api-auth";
import { apiError, json } from "@/lib/http";

export const runtime = "nodejs";

export async function GET(request: NextRequest) {
  const session = readSession(request);
  if (!session) return apiError("invalid_session", 401);
  return json({ user: session });
}
