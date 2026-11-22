-- M6 脱敏：RedactRule 增加匹配类型、作用域、替换文本与更新时间。
-- 只加列/索引，不删除历史数据。
ALTER TABLE "RedactRule" ADD COLUMN "kind" TEXT NOT NULL DEFAULT 'regex';
ALTER TABLE "RedactRule" ADD COLUMN "target" TEXT NOT NULL DEFAULT 'all';
ALTER TABLE "RedactRule" ADD COLUMN "replacement" TEXT NOT NULL DEFAULT '';
ALTER TABLE "RedactRule" ADD COLUMN "updatedAt" TIMESTAMPTZ(6) NOT NULL DEFAULT CURRENT_TIMESTAMP;

CREATE UNIQUE INDEX "RedactRule_name_key" ON "RedactRule"("name");
