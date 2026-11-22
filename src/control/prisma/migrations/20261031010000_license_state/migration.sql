-- FR-35 离线授权：单例授权状态表。
-- 只新增表，不修改既有数据；firstSeenAt 用于 30 天试用，lastSeenAt 用于时钟回拨检测。
CREATE TABLE "LicenseState" (
    "id" TEXT NOT NULL,
    "licenseId" TEXT,
    "customer" TEXT,
    "machineCode" TEXT,
    "payload" TEXT,
    "sig" TEXT,
    "kid" TEXT,
    "expiresAt" TIMESTAMPTZ(6),
    "features" JSONB,
    "status" TEXT NOT NULL DEFAULT 'missing',
    "importedAt" TIMESTAMPTZ(6),
    "firstSeenAt" TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "lastSeenAt" TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "revocation" JSONB,
    "revocationVersion" INTEGER NOT NULL DEFAULT 0,
    "updatedAt" TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT "LicenseState_pkey" PRIMARY KEY ("id")
);
