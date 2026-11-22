-- AlterTable
ALTER TABLE "ProviderCredential" ADD COLUMN     "authHeaderName" TEXT NOT NULL DEFAULT 'Authorization',
ADD COLUMN     "authScheme" TEXT NOT NULL DEFAULT 'Bearer',
ADD COLUMN     "baseUrl" TEXT NOT NULL DEFAULT '',
ADD COLUMN     "extraHeaders" JSONB NOT NULL DEFAULT '{}',
ADD COLUMN     "limitDims" JSONB NOT NULL DEFAULT '[]',
ADD COLUMN     "name" TEXT NOT NULL DEFAULT '',
ADD COLUMN     "providerType" TEXT NOT NULL DEFAULT 'openai',
ADD COLUMN     "streamTerminator" TEXT NOT NULL DEFAULT '';

-- AlterTable
ALTER TABLE "ModelMap" ADD COLUMN     "credentialId" UUID;

-- CreateIndex
CREATE INDEX "ModelMap_credentialId_idx" ON "ModelMap"("credentialId");

-- AddForeignKey
ALTER TABLE "ModelMap" ADD CONSTRAINT "ModelMap_credentialId_fkey" FOREIGN KEY ("credentialId") REFERENCES "ProviderCredential"("id") ON DELETE SET NULL ON UPDATE CASCADE;

