-- ADR-009：同一把上游 Key 允许按不同协议各建一条凭据。
-- 背景：一家厂商的 OpenAI 兼容端点与 Anthropic 兼容端点需要两条 ProviderCredential
-- （如 DeepSeek 的 /v1 与 /anthropic），原唯一键 (kid, keyFp6) 把同一把 Key 锁成一条。
-- 约束：同协议重复接入仍返回 409 credential_exists。

DROP INDEX "ProviderCredential_kid_keyFp6_key";

CREATE UNIQUE INDEX "ProviderCredential_kid_keyFp6_providerType_key"
    ON "ProviderCredential"("kid", "keyFp6", "providerType");
