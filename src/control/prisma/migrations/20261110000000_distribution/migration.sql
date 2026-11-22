-- 「分发 API」：老板先建具名分发入口，再把分发出来的虚拟 Key 交给员工。
--
-- 背景：原模型只有 ApiKey（按 empId 分配 + allowedModels 白名单），老板每给一个
-- 员工签一次 Key 就要重填一遍模型名单，谁是「同一个分发」在库里没有痕迹。
-- 本迁移只做加法：新增 Distribution 表，并给 ApiKey 加两个可空/带默认值的列。
-- 兼容性：既有 ApiKey 行 distributionId 为 NULL、label 为空串，行为完全不变
-- （白名单仍读 ApiKey.allowedModels，鉴权路径未改）。

-- CreateTable
CREATE TABLE "Distribution" (
    "id" UUID NOT NULL,
    "name" TEXT NOT NULL,
    "description" TEXT NOT NULL DEFAULT '',
    "allowedModels" TEXT[] DEFAULT ARRAY[]::TEXT[],
    "disabled" BOOLEAN NOT NULL DEFAULT false,
    "revokedAt" TIMESTAMPTZ(6),
    "createdById" UUID,
    "createdAt" TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updatedAt" TIMESTAMPTZ(6) NOT NULL,

    CONSTRAINT "Distribution_pkey" PRIMARY KEY ("id")
);

-- CreateIndex
CREATE UNIQUE INDEX "Distribution_name_key" ON "Distribution"("name");

-- CreateIndex
CREATE INDEX "Distribution_revokedAt_idx" ON "Distribution"("revokedAt");

-- AlterTable
ALTER TABLE "ApiKey" ADD COLUMN "distributionId" UUID,
ADD COLUMN "label" TEXT NOT NULL DEFAULT '';

-- CreateIndex
CREATE INDEX "ApiKey_distributionId_idx" ON "ApiKey"("distributionId");

-- AddForeignKey
ALTER TABLE "Distribution" ADD CONSTRAINT "Distribution_createdById_fkey" FOREIGN KEY ("createdById") REFERENCES "User"("id") ON DELETE SET NULL ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "ApiKey" ADD CONSTRAINT "ApiKey_distributionId_fkey" FOREIGN KEY ("distributionId") REFERENCES "Distribution"("id") ON DELETE SET NULL ON UPDATE CASCADE;
