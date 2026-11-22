-- 请求幂等声明：同一虚拟 Key + X-Request-Id 只允许一次上游执行。
-- 网关读写、控制面只读；不保存请求/响应正文。
-- status: in_progress | completed | failed | outcome_unknown
-- 终态保留 24 小时（expires_at），过期行由网关清理。

CREATE TABLE "RequestClaim" (
    "id" UUID NOT NULL,
    "keyId" UUID NOT NULL,
    "empId" UUID NOT NULL,
    "requestId" VARCHAR(128) NOT NULL,
    "payloadHash" VARCHAR(64) NOT NULL,
    "path" TEXT NOT NULL,
    "model" TEXT NOT NULL DEFAULT '',
    "stream" BOOLEAN NOT NULL DEFAULT false,
    "status" VARCHAR(16) NOT NULL,
    "statusCode" INTEGER,
    "errorCode" TEXT NOT NULL DEFAULT '',
    "createdAt" TIMESTAMP(6) WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updatedAt" TIMESTAMP(6) WITH TIME ZONE NOT NULL,
    "expiresAt" TIMESTAMP(6) WITH TIME ZONE NOT NULL,

    CONSTRAINT "RequestClaim_pkey" PRIMARY KEY ("id")
);

CREATE UNIQUE INDEX "RequestClaim_keyId_requestId_key" ON "RequestClaim"("keyId", "requestId");
CREATE INDEX "RequestClaim_expiresAt_idx" ON "RequestClaim"("expiresAt");
CREATE INDEX "RequestClaim_empId_createdAt_idx" ON "RequestClaim"("empId", "createdAt");
CREATE INDEX "RequestClaim_status_updatedAt_idx" ON "RequestClaim"("status", "updatedAt");
