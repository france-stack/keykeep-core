-- CreateSchema
CREATE SCHEMA IF NOT EXISTS "public";

-- CreateTable
CREATE TABLE "Dept" (
    "id" UUID NOT NULL,
    "name" TEXT NOT NULL,
    "parentId" UUID,
    "createdAt" TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updatedAt" TIMESTAMPTZ(6) NOT NULL,

    CONSTRAINT "Dept_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "Role" (
    "id" UUID NOT NULL,
    "name" TEXT NOT NULL,

    CONSTRAINT "Role_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "User" (
    "id" UUID NOT NULL,
    "account" TEXT NOT NULL,
    "passwordHash" TEXT NOT NULL,
    "displayName" TEXT NOT NULL,
    "deptId" UUID,
    "roleId" UUID NOT NULL,
    "disabled" BOOLEAN NOT NULL DEFAULT false,
    "failedAttempts" INTEGER NOT NULL DEFAULT 0,
    "lockedUntil" TIMESTAMPTZ(6),
    "passwordChangedAt" TIMESTAMPTZ(6),
    "createdAt" TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updatedAt" TIMESTAMPTZ(6) NOT NULL,

    CONSTRAINT "User_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "ApiKey" (
    "id" UUID NOT NULL,
    "keyHash" BYTEA NOT NULL,
    "prefix4" VARCHAR(4) NOT NULL,
    "fp6" VARCHAR(6) NOT NULL,
    "empId" UUID NOT NULL,
    "allowedModels" TEXT[] DEFAULT ARRAY[]::TEXT[],
    "revokedAt" TIMESTAMPTZ(6),
    "createdAt" TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT "ApiKey_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "ProviderCredential" (
    "id" UUID NOT NULL,
    "provider" TEXT NOT NULL,
    "kid" TEXT NOT NULL,
    "keyFp6" VARCHAR(6) NOT NULL,
    "ct" BYTEA NOT NULL,
    "nonce" BYTEA NOT NULL,
    "disabled" BOOLEAN NOT NULL DEFAULT false,
    "createdAt" TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updatedAt" TIMESTAMPTZ(6) NOT NULL,

    CONSTRAINT "ProviderCredential_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "UsageRecord" (
    "rid" TEXT NOT NULL,
    "ts" TIMESTAMPTZ(6) NOT NULL,
    "keyId" UUID NOT NULL,
    "empId" UUID NOT NULL,
    "deptId" UUID,
    "provider" TEXT NOT NULL,
    "modelReq" TEXT NOT NULL,
    "modelServed" TEXT NOT NULL,
    "inputTokens" BIGINT NOT NULL,
    "outputTokens" BIGINT NOT NULL,
    "cacheCreationInputTokens" BIGINT NOT NULL DEFAULT 0,
    "cacheReadInputTokens" BIGINT NOT NULL DEFAULT 0,
    "cacheCreation5m" BIGINT NOT NULL DEFAULT 0,
    "cacheCreation1h" BIGINT NOT NULL DEFAULT 0,
    "costCents" BIGINT NOT NULL,
    "status" TEXT NOT NULL,
    "requestId" TEXT NOT NULL,
    "prevHash" TEXT NOT NULL,
    "rowHash" TEXT NOT NULL,

    CONSTRAINT "UsageRecord_pkey" PRIMARY KEY ("rid")
);

-- CreateTable
CREATE TABLE "UsageDaily" (
    "day" DATE NOT NULL,
    "empId" UUID NOT NULL,
    "deptId" UUID NOT NULL,
    "model" TEXT NOT NULL,
    "calls" BIGINT NOT NULL DEFAULT 0,
    "errors" BIGINT NOT NULL DEFAULT 0,
    "inputTokens" BIGINT NOT NULL DEFAULT 0,
    "outputTokens" BIGINT NOT NULL DEFAULT 0,
    "cacheReadTokens" BIGINT NOT NULL DEFAULT 0,
    "cacheWriteTokens" BIGINT NOT NULL DEFAULT 0,
    "costCents" BIGINT NOT NULL DEFAULT 0,

    CONSTRAINT "UsageDaily_pkey" PRIMARY KEY ("day","empId","deptId","model")
);

-- CreateTable
CREATE TABLE "Budget" (
    "id" UUID NOT NULL,
    "scope" TEXT NOT NULL,
    "scopeId" TEXT NOT NULL,
    "period" TEXT NOT NULL,
    "limitCents" BIGINT NOT NULL,
    "usedCents" BIGINT NOT NULL DEFAULT 0,
    "reservedCents" BIGINT NOT NULL DEFAULT 0,
    "updatedAt" TIMESTAMPTZ(6) NOT NULL,

    CONSTRAINT "Budget_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "BudgetLedger" (
    "id" UUID NOT NULL,
    "budgetId" UUID NOT NULL,
    "rid" TEXT NOT NULL,
    "kind" TEXT NOT NULL,
    "deltaCents" BIGINT NOT NULL,
    "at" TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT "BudgetLedger_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "AuditEvent" (
    "id" UUID NOT NULL,
    "at" TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "actorId" TEXT,
    "action" TEXT NOT NULL,
    "detail" JSONB NOT NULL,
    "requestId" TEXT,

    CONSTRAINT "AuditEvent_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "RedactRule" (
    "id" UUID NOT NULL,
    "name" TEXT NOT NULL,
    "pattern" TEXT NOT NULL,
    "enabled" BOOLEAN NOT NULL DEFAULT true,
    "priority" INTEGER NOT NULL DEFAULT 100,
    "createdAt" TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT "RedactRule_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "ModelCatalog" (
    "provider" TEXT NOT NULL,
    "modelId" TEXT NOT NULL,
    "displayName" TEXT NOT NULL,
    "contextLength" INTEGER,
    "maxInputTokens" INTEGER,
    "maxOutputTokens" INTEGER,
    "pricingPromptCents" BIGINT NOT NULL DEFAULT 0,
    "pricingCompletionCents" BIGINT NOT NULL DEFAULT 0,
    "pricingCacheReadCents" BIGINT NOT NULL DEFAULT 0,
    "type" TEXT NOT NULL DEFAULT 'chat',
    "reasoningLevels" TEXT[] DEFAULT ARRAY[]::TEXT[],

    CONSTRAINT "ModelCatalog_pkey" PRIMARY KEY ("provider","modelId")
);

-- CreateTable
CREATE TABLE "ModelMap" (
    "id" UUID NOT NULL,
    "alias" TEXT NOT NULL,
    "provider" TEXT NOT NULL,
    "modelId" TEXT NOT NULL,

    CONSTRAINT "ModelMap_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "Limit" (
    "id" UUID NOT NULL,
    "scope" TEXT NOT NULL,
    "scopeId" TEXT NOT NULL,
    "dim" TEXT NOT NULL,
    "limitValue" BIGINT NOT NULL,
    "usedValue" BIGINT NOT NULL DEFAULT 0,
    "period" TEXT NOT NULL,
    "updatedAt" TIMESTAMPTZ(6) NOT NULL,

    CONSTRAINT "Limit_pkey" PRIMARY KEY ("id")
);

-- CreateIndex
CREATE INDEX "Dept_parentId_idx" ON "Dept"("parentId");

-- CreateIndex
CREATE UNIQUE INDEX "Role_name_key" ON "Role"("name");

-- CreateIndex
CREATE UNIQUE INDEX "User_account_key" ON "User"("account");

-- CreateIndex
CREATE INDEX "User_deptId_idx" ON "User"("deptId");

-- CreateIndex
CREATE UNIQUE INDEX "ApiKey_keyHash_key" ON "ApiKey"("keyHash");

-- CreateIndex
CREATE INDEX "ApiKey_empId_idx" ON "ApiKey"("empId");

-- CreateIndex
CREATE INDEX "ApiKey_revokedAt_idx" ON "ApiKey"("revokedAt");

-- CreateIndex
CREATE INDEX "ProviderCredential_provider_idx" ON "ProviderCredential"("provider");

-- CreateIndex
CREATE UNIQUE INDEX "ProviderCredential_kid_keyFp6_key" ON "ProviderCredential"("kid", "keyFp6");

-- CreateIndex
CREATE INDEX "UsageRecord_ts_idx" ON "UsageRecord"("ts");

-- CreateIndex
CREATE INDEX "UsageRecord_keyId_ts_idx" ON "UsageRecord"("keyId", "ts");

-- CreateIndex
CREATE INDEX "UsageRecord_empId_ts_idx" ON "UsageRecord"("empId", "ts");

-- CreateIndex
CREATE INDEX "UsageRecord_prevHash_idx" ON "UsageRecord"("prevHash");

-- CreateIndex
CREATE INDEX "UsageDaily_deptId_day_idx" ON "UsageDaily"("deptId", "day");

-- CreateIndex
CREATE UNIQUE INDEX "Budget_scope_scopeId_period_key" ON "Budget"("scope", "scopeId", "period");

-- CreateIndex
CREATE INDEX "BudgetLedger_budgetId_at_idx" ON "BudgetLedger"("budgetId", "at");

-- CreateIndex
CREATE UNIQUE INDEX "BudgetLedger_budgetId_rid_kind_key" ON "BudgetLedger"("budgetId", "rid", "kind");

-- CreateIndex
CREATE INDEX "AuditEvent_at_idx" ON "AuditEvent"("at");

-- CreateIndex
CREATE INDEX "AuditEvent_actorId_at_idx" ON "AuditEvent"("actorId", "at");

-- CreateIndex
CREATE INDEX "AuditEvent_action_at_idx" ON "AuditEvent"("action", "at");

-- CreateIndex
CREATE INDEX "RedactRule_enabled_priority_idx" ON "RedactRule"("enabled", "priority");

-- CreateIndex
CREATE UNIQUE INDEX "ModelMap_alias_provider_key" ON "ModelMap"("alias", "provider");

-- CreateIndex
CREATE UNIQUE INDEX "Limit_scope_scopeId_dim_key" ON "Limit"("scope", "scopeId", "dim");

-- AddForeignKey
ALTER TABLE "Dept" ADD CONSTRAINT "Dept_parentId_fkey" FOREIGN KEY ("parentId") REFERENCES "Dept"("id") ON DELETE SET NULL ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "User" ADD CONSTRAINT "User_deptId_fkey" FOREIGN KEY ("deptId") REFERENCES "Dept"("id") ON DELETE SET NULL ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "User" ADD CONSTRAINT "User_roleId_fkey" FOREIGN KEY ("roleId") REFERENCES "Role"("id") ON DELETE RESTRICT ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "ApiKey" ADD CONSTRAINT "ApiKey_empId_fkey" FOREIGN KEY ("empId") REFERENCES "User"("id") ON DELETE RESTRICT ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "UsageRecord" ADD CONSTRAINT "UsageRecord_keyId_fkey" FOREIGN KEY ("keyId") REFERENCES "ApiKey"("id") ON DELETE RESTRICT ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "UsageRecord" ADD CONSTRAINT "UsageRecord_empId_fkey" FOREIGN KEY ("empId") REFERENCES "User"("id") ON DELETE RESTRICT ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "BudgetLedger" ADD CONSTRAINT "BudgetLedger_budgetId_fkey" FOREIGN KEY ("budgetId") REFERENCES "Budget"("id") ON DELETE RESTRICT ON UPDATE CASCADE;

