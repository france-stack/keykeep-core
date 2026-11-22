-- C11：审计事件防篡改链。
-- 背景：AuditEvent 是"一切操作都有据可查"这句话的唯一凭据，但原表既没有可信的
-- 写入顺序（at 只有毫秒精度，同毫秒排不出先后），也没有任何完整性绑定——能连上
-- 数据库的人可以改 action、抹 actorId、删整行，事后无从发现。
-- 本迁移只做三件事：给一个可信序号、给三个链列、把存量行如实标成"未链"。
-- 摘要本身一律由应用层算（lib/audit-chain.ts），不引入 pgcrypto：
-- 链的输入必须与 detail 的 JSONB 归一化结果一致，而"从库里读回来再规范化"这件事
-- 只有应用层做得到，SQL 侧硬拼 canonical JSON 会随版本漂。

-- 1) 序号列。先可空，编号完成后再收紧约束，避免空表/非空表两条路径写法不同。
ALTER TABLE "AuditEvent" ADD COLUMN "seq" BIGINT;

CREATE SEQUENCE IF NOT EXISTS "AuditEvent_seq_seq";

-- 2) 存量行按 (at, id) 定序补号。at 同毫秒时用 id（uuid）兜底，顺序是确定的。
--    只补号不回链：回链要在 SQL 里复算 canonical JSON，见上面的说明。
WITH ordered AS (
    SELECT "id", row_number() OVER (ORDER BY "at", "id") AS rn FROM "AuditEvent"
)
UPDATE "AuditEvent" AS a
SET "seq" = ordered.rn
FROM ordered
WHERE a."id" = ordered."id";

-- 3) 序列必须排在已用号之后，否则下一条记录就会撞进历史区间（unique 立刻炸）。
--    空表要单独走一条路：max(seq) 为 NULL 时 COALESCE 出来的 0 低于 setval 的
--    合法下限 1，会以 "value 0 is out of bounds" 让整批迁移回滚——首装必然命中
--    （种子数据在这批迁移之后才写，此刻表一定是空的）。空表用 is_called=false
--    把首号留给下一条 nextval，非空表照旧从 max 之后接。
SELECT setval(
    '"AuditEvent_seq_seq"',
    COALESCE((SELECT max("seq") FROM "AuditEvent"), 1),
    (SELECT max("seq") FROM "AuditEvent") IS NOT NULL
);

ALTER TABLE "AuditEvent" ALTER COLUMN "seq" SET DEFAULT nextval('"AuditEvent_seq_seq"'::regclass);
ALTER SEQUENCE "AuditEvent_seq_seq" OWNED BY "AuditEvent"."seq";

ALTER TABLE "AuditEvent" ALTER COLUMN "seq" SET NOT NULL;

CREATE UNIQUE INDEX "AuditEvent_seq_key" ON "AuditEvent"("seq");

-- 4) 链列。可空是设计而不是偷懒：NULL 的诚实含义是"这一行没有链证据"，
--    校验时会单独计数（unchainedBefore/unchainedInside），不会被算成"校验通过"。
ALTER TABLE "AuditEvent" ADD COLUMN "prevHash"  TEXT;
ALTER TABLE "AuditEvent" ADD COLUMN "rowHash" TEXT;
ALTER TABLE "AuditEvent" ADD COLUMN "hashAlgo" TEXT;

-- 未链行的计数与"链头回溯"都靠这个偏索引，否则每次管理端查看链状态都要全表扫。
CREATE INDEX "AuditEvent_seq_where_rowHash_null" ON "AuditEvent"("seq") WHERE "rowHash" IS NULL;
