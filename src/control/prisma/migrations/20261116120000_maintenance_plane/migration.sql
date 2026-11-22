-- 维护平面 P0（FR-60/61/62/64/65/66/67）。
--
-- 三张表只做加法，不改任何既有表：MaintenanceTicket（客户批准的维护工单）、
-- MaintenanceSession（一次已批准的维护连接，凭据只存 SHA-256）、
-- MaintenanceApproval（写命令审批队列）。
-- 表的创建不构成维护入口：维护会话完全在控制面进程内、由
-- KEYKEEP_MAINTENANCE_ENABLED 显式开启，网关侧零监听（RM-A01）。

-- CreateTable
CREATE TABLE "MaintenanceTicket" (
    "id" UUID NOT NULL,
    "vendorName" TEXT NOT NULL,
    "reason" TEXT NOT NULL DEFAULT '',
    "approvedAt" TIMESTAMPTZ(6) NOT NULL,
    "expiresAt" TIMESTAMPTZ(6) NOT NULL,
    "revokedAt" TIMESTAMPTZ(6),
    "createdById" UUID NOT NULL,
    "createdAt" TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT "MaintenanceTicket_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "MaintenanceSession" (
    "id" UUID NOT NULL,
    "ticketId" UUID NOT NULL,
    "credHash" BYTEA NOT NULL,
    "credFp6" VARCHAR(6) NOT NULL,
    "host" TEXT NOT NULL,
    "account" TEXT NOT NULL,
    "commandScope" TEXT[] DEFAULT ARRAY[]::TEXT[],
    "expiresAt" TIMESTAMPTZ(6) NOT NULL,
    "revokedAt" TIMESTAMPTZ(6),
    "endedAt" TIMESTAMPTZ(6),
    "endReason" TEXT NOT NULL DEFAULT '',
    "createdAt" TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT "MaintenanceSession_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "MaintenanceApproval" (
    "id" UUID NOT NULL,
    "sessionId" UUID NOT NULL,
    "command" TEXT NOT NULL,
    "kind" VARCHAR(16) NOT NULL,
    "state" VARCHAR(16) NOT NULL,
    "commandKey" TEXT NOT NULL DEFAULT '',
    "result" TEXT NOT NULL DEFAULT '',
    "exitCode" INTEGER,
    "requestedAt" TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "decidedAt" TIMESTAMPTZ(6),
    "decidedById" UUID,

    CONSTRAINT "MaintenanceApproval_pkey" PRIMARY KEY ("id")
);

-- CreateIndex
CREATE INDEX "MaintenanceTicket_revokedAt_idx" ON "MaintenanceTicket"("revokedAt");

-- CreateIndex
CREATE INDEX "MaintenanceTicket_expiresAt_idx" ON "MaintenanceTicket"("expiresAt");

-- CreateIndex
CREATE UNIQUE INDEX "MaintenanceSession_credHash_key" ON "MaintenanceSession"("credHash");

-- CreateIndex
CREATE INDEX "MaintenanceSession_ticketId_idx" ON "MaintenanceSession"("ticketId");

-- CreateIndex
CREATE INDEX "MaintenanceSession_revokedAt_idx" ON "MaintenanceSession"("revokedAt");

-- CreateIndex
CREATE INDEX "MaintenanceSession_expiresAt_idx" ON "MaintenanceSession"("expiresAt");

-- CreateIndex
CREATE INDEX "MaintenanceApproval_sessionId_state_idx" ON "MaintenanceApproval"("sessionId", "state");

-- CreateIndex
CREATE INDEX "MaintenanceApproval_state_requestedAt_idx" ON "MaintenanceApproval"("state", "requestedAt");

-- AddForeignKey
ALTER TABLE "MaintenanceTicket" ADD CONSTRAINT "MaintenanceTicket_createdById_fkey" FOREIGN KEY ("createdById") REFERENCES "User"("id") ON DELETE RESTRICT ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "MaintenanceSession" ADD CONSTRAINT "MaintenanceSession_ticketId_fkey" FOREIGN KEY ("ticketId") REFERENCES "MaintenanceTicket"("id") ON DELETE RESTRICT ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "MaintenanceApproval" ADD CONSTRAINT "MaintenanceApproval_sessionId_fkey" FOREIGN KEY ("sessionId") REFERENCES "MaintenanceSession"("id") ON DELETE RESTRICT ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "MaintenanceApproval" ADD CONSTRAINT "MaintenanceApproval_decidedById_fkey" FOREIGN KEY ("decidedById") REFERENCES "User"("id") ON DELETE SET NULL ON UPDATE CASCADE;
