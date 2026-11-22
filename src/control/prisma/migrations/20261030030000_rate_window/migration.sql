-- 跨实例限流计数：每把 Key 每秒每个维度一行，网关按 60 秒窗口求和。
-- 过期行由网关每分钟清理（second < now-120）；控制面不读写本表。

CREATE TABLE "RateWindow" (
    "keyId" UUID NOT NULL,
    "second" BIGINT NOT NULL,
    "dim" VARCHAR(8) NOT NULL,
    "amount" BIGINT NOT NULL DEFAULT 0,

    CONSTRAINT "RateWindow_pkey" PRIMARY KEY ("keyId", "second", "dim")
);

CREATE INDEX "RateWindow_second_idx" ON "RateWindow"("second");
