-- 同一 (alias, provider) 允许多条凭据：唯一键加上 credentialId。
-- 背景：FR-10 / FA-10 需要两条可用凭据服务同一逻辑模型，停用其一后流量改走另一条。
DROP INDEX "ModelMap_alias_provider_key";

CREATE UNIQUE INDEX "ModelMap_alias_provider_credentialId_key"
    ON "ModelMap"("alias", "provider", "credentialId");
