-- 价格表版本：同模型多版价格，按 effectiveFrom 取生效版本。
-- 回填现有 ModelCatalog 价格为 v1；缓存写入价沿用输入价（与旧实现一致）。

CREATE TABLE "ModelPrice" (
    "provider" TEXT NOT NULL,
    "modelId" TEXT NOT NULL,
    "version" TEXT NOT NULL,
    "effectiveFrom" TIMESTAMP(6) WITH TIME ZONE NOT NULL,
    "promptPerM" BIGINT NOT NULL DEFAULT 0,
    "completionPerM" BIGINT NOT NULL DEFAULT 0,
    "cacheReadPerM" BIGINT NOT NULL DEFAULT 0,
    "cacheWritePerM" BIGINT NOT NULL DEFAULT 0,
    "createdAt" TIMESTAMP(6) WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT "ModelPrice_pkey" PRIMARY KEY ("provider", "modelId", "version")
);

CREATE INDEX "ModelPrice_modelId_effectiveFrom_idx" ON "ModelPrice"("modelId", "effectiveFrom");

INSERT INTO "ModelPrice"("provider", "modelId", "version", "effectiveFrom",
                         "promptPerM", "completionPerM", "cacheReadPerM", "cacheWritePerM")
SELECT provider, "modelId", 'v1', now(),
       "pricingPromptCents", "pricingCompletionCents", "pricingCacheReadCents", "pricingPromptCents"
  FROM "ModelCatalog"
ON CONFLICT DO NOTHING;
