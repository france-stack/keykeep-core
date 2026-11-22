import type { NextRequest } from "next/server";
import { NextResponse } from "next/server";

import { requireRoles, requireWritable } from "@/lib/api-auth";
import { auditRequestId, recordAudit } from "@/lib/audit";
import { prisma } from "@/lib/db";
import { apiError, json } from "@/lib/http";

export const runtime = "nodejs";

export async function GET(request: NextRequest) {
  const auth = await requireRoles(request, ["admin"]);
  if (auth instanceof NextResponse) return auth;

  const models = await prisma.modelMap.findMany({
    include: { credential: { select: { id: true, name: true, providerType: true } } },
    orderBy: [{ provider: "asc" }, { alias: "asc" }],
  });
  return json({
    models: models.map((model) => ({
      id: model.id,
      alias: model.alias,
      provider: model.provider,
      modelId: model.modelId,
      credential: model.credential,
    })),
  });
}

export async function POST(request: NextRequest) {
  const auth = await requireRoles(request, ["admin"]);
  if (auth instanceof NextResponse) return auth;
  const readOnly = await requireWritable(request, auth.sub);
  if (readOnly) return readOnly;

  let body: { credentialId?: string; alias?: string; modelId?: string };
  try {
    body = (await request.json()) as typeof body;
  } catch {
    return apiError("invalid_request", 400);
  }
  const alias = (body.alias ?? "").trim();
  const modelId = (body.modelId ?? "").trim();
  if (!body.credentialId || !alias || !modelId) return apiError("invalid_request", 400);

  const credential = await prisma.providerCredential.findUnique({
    where: { id: body.credentialId },
  });
  if (!credential) return apiError("invalid_request", 400);

  const mapping = await prisma.modelMap.upsert({
    where: {
      alias_provider_credentialId: {
        alias,
        provider: credential.providerType,
        credentialId: credential.id,
      },
    },
    update: { modelId },
    create: {
      alias,
      modelId,
      provider: credential.providerType,
      credentialId: credential.id,
    },
  });

  await recordAudit({
    actorId: auth.sub,
    action: "model.create",
    detail: { alias: mapping.alias, modelId: mapping.modelId, credentialId: credential.id },
    requestId: auditRequestId(request),
  });

  return json(
    {
      model: {
        id: mapping.id,
        alias: mapping.alias,
        provider: mapping.provider,
        modelId: mapping.modelId,
      },
    },
    201,
  );
}
