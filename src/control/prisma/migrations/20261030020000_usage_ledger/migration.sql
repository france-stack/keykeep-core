-- AlterTable
ALTER TABLE "UsageRecord" ADD COLUMN     "alias" TEXT NOT NULL DEFAULT '',
ADD COLUMN     "authId" TEXT NOT NULL DEFAULT '',
ADD COLUMN     "failed" BOOLEAN NOT NULL DEFAULT false,
ADD COLUMN     "latencyMs" INTEGER,
ADD COLUMN     "priceVersion" TEXT NOT NULL DEFAULT 'v1',
ADD COLUMN     "seq" BIGSERIAL NOT NULL,
ADD COLUMN     "stream" BOOLEAN NOT NULL DEFAULT false,
ADD COLUMN     "ttftMs" INTEGER,
ADD COLUMN     "unpriced" BOOLEAN NOT NULL DEFAULT false;

-- CreateIndex
CREATE UNIQUE INDEX "UsageRecord_seq_key" ON "UsageRecord"("seq");

