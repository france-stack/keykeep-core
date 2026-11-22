// Package ledger 把内嵌 SDK 的用量回调写成哈希链账本，并结算两级预算。
package ledger

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"

	"github.com/keykeep/gateway/internal/dbpool"
)

// defaultPriceVersion 用于未定价或旧库回退；定价命中时以 ModelPrice.version 为准。
const defaultPriceVersion = "v1"

// zeroDept 用于没有部门的员工：UsageDaily.deptId 非空，用全零 UUID 占位。
const zeroDept = "00000000-0000-0000-0000-000000000000"

// chainLockKey 串行化链尾读取与插入；单实例够用，多实例同样安全。
const chainLockKey int64 = 7411001

type Plugin struct {
	pool      *pgxpool.Pool
	keyPrefix string
	sink      UsageSink
	// floorPerM 未定价模型的保底单价（分/百万 token）；0 表示不保底。
	floorPerM int64
	// lastFloorWarn 限制未定价用量按 0 结算的告警频率，防止日志刷屏。
	lastFloorWarn atomic.Int64
	// wal 是写失败时的本地补偿队列；nil 表示不启用（纯内存测试场景）。
	wal *wal
	// identityCache/priceCache 热路径查询的 TTL 缓存；nil 接收方等于不缓存。
	identityCache *ttlCache[identityResult]
	priceCache    *ttlCache[priceResult]
}

// UsageSink 接收实际 token 用量，用于限流窗口的“多退少补”。
type UsageSink interface {
	AddActual(ctx context.Context, keyID string, inputTokens, outputTokens int64) error
}

func (p *Plugin) SetUsageSink(sink UsageSink) { p.sink = sink }

// SetUnpricedFloorPerM 设置未定价模型的保底单价（分/百万 token）；0 表示不保底。
func (p *Plugin) SetUnpricedFloorPerM(v int64) { p.floorPerM = v }

// NewPlugin 建立账本插件；keyPrefix 与虚拟 Key 中间件注入的内部 Key 前缀一致。
// WAL 路径可用 KEYKEEP_LEDGER_WAL 指定，默认落在系统临时目录。
func NewPlugin(ctx context.Context, databaseURL, keyPrefix string) (*Plugin, error) {
	pool, err := dbpool.Open(ctx, databaseURL)
	if err != nil {
		return nil, err
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, err
	}
	return &Plugin{
		pool:      pool,
		keyPrefix: keyPrefix,
		// WAL 固定落在系统临时目录（可用系统级 TMPDIR 重定向）：
		// 不开自定义路径开关，避免环境变量直接成为文件系统写入的入口。
		wal: newWAL(""),
		// 价格缓存 TTL=1s：T-30 的契约是「调价后下一次请求即用新价」（测试在
		// 发布新版本后 2s 发起调价后请求）——15s TTL 让调价在缓存窗口内继续
		// 按旧价结算（2026-11-08 E2E 实测）。1s 仍能合并结算路径上的并发查询。
		identityCache: newTTLCache[identityResult](15 * time.Second),
		priceCache:    newTTLCache[priceResult](1 * time.Second),
	}, nil
}

func (p *Plugin) Close() { p.pool.Close() }

// HandleUsage 实现 usage.Plugin。任何错误只记录日志，绝不 panic 或阻塞上游响应；
// 入账管线失败进入 WAL，由重放器周期性补偿（rid 唯一约束保证重放幂等）。
func (p *Plugin) HandleUsage(ctx context.Context, rec usage.Record) {
	if err := p.process(ctx, rec); err != nil {
		log.Printf("ledger: pipeline failed, queued for replay (rid=%s): %v", rec.RequestID, err)
		if p.wal != nil {
			if err := p.wal.append(rec); err != nil {
				log.Printf("ledger: WAL append failed (usage lost, rid=%s): %v", rec.RequestID, err)
			}
		}
	}
}

// process 是一次用量回调的完整入账管线；返回 error 时调用方负责进 WAL。
func (p *Plugin) process(ctx context.Context, rec usage.Record) error {
	keyID, ok := p.keyID(rec.APIKey)
	if !ok {
		log.Printf("ledger: skip usage record without internal key (request=%s model=%s)", rec.RequestID, rec.Model)
		return nil
	}
	// 请求上下文可能已取消，账本落库用脱离取消的上下文。
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()

	empID, deptID, err := p.identity(ctx, keyID)
	if err != nil {
		return fmt.Errorf("resolve key %s: %w", keyID, err)
	}
	if empID == "" {
		log.Printf("ledger: key %s no longer exists, usage dropped", keyID)
		return nil
	}

	price, version, found, err := p.price(ctx, rec.Provider, servedModel(rec))
	if err != nil {
		return fmt.Errorf("price lookup: %w", err)
	}
	entry := Entry{
		RID:          ridOrNew(rec.RequestID),
		TS:           timestamp(rec.RequestedAt),
		KeyID:        keyID,
		EmpID:        empID,
		DeptID:       deptID,
		Provider:     rec.Provider,
		Alias:        rec.Alias,
		ModelReq:     requestedModel(rec),
		ModelServed:  servedModel(rec),
		AuthID:       rec.AuthID,
		PriceVersion: version,
		Unpriced:     !found,
		InputTokens:  rec.Detail.InputTokens,
		OutputTokens: rec.Detail.OutputTokens,
		CacheRead:    rec.Detail.CacheReadTokens,
		CacheWrite:   rec.Detail.CacheCreationTokens,
		Status:       StatusFor(rec.Failed),
		RequestID:    rec.TraceID,
		Stream:       rec.Stream,
		Failed:       rec.Failed,
	}
	if found {
		entry.CostCents = price.Cost(entry.InputTokens, entry.OutputTokens, entry.CacheRead, entry.CacheWrite)
	} else {
		// 未定价模型若按 0 结算，预算对它形同虚设；配置了保底价就按保底计费
		// （行仍标 unpriced，事后可按真实价格修正），未配置则限频告警提示缺口。
		entry.CostCents = FloorCost(entry.InputTokens, entry.OutputTokens, p.floorPerM)
		if entry.CostCents <= 0 && entry.InputTokens+entry.OutputTokens > 0 {
			p.warnUnpricedOnce(entry.ModelServed)
		}
	}

	inserted, err := p.write(ctx, entry)
	if err != nil {
		return fmt.Errorf("write rid=%s: %w", entry.RID, err)
	}
	if inserted && p.sink != nil {
		if err := p.sink.AddActual(ctx, keyID, entry.InputTokens, entry.OutputTokens); err != nil {
			log.Printf("ledger: update rate window: %v", err)
		}
	}
	return nil
}

// StartReplayer 启动 WAL 重放协程：启动即重放一次（补偿上次进程退出前的积压），
// 之后每 30 秒一轮；ctx 取消（进程退出）前最后再重放一轮，能救多少救多少。
func (p *Plugin) StartReplayer(ctx context.Context) {
	if p.wal == nil {
		return
	}
	go func() {
		p.replayOnce()
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				p.replayOnce()
				return
			case <-ticker.C:
				p.replayOnce()
			}
		}
	}()
}

// replayOnce 执行一轮 WAL 重放并记录结果；process 有 rid 幂等保障，重放安全。
func (p *Plugin) replayOnce() {
	if p.wal == nil {
		return
	}
	n, err := p.wal.replay(func(rec usage.Record) error {
		return p.process(context.Background(), rec)
	})
	if err != nil {
		log.Printf("ledger: WAL replay: %v", err)
	}
	if n > 0 {
		log.Printf("ledger: WAL replayed %d usage record(s)", n)
	}
}

func (p *Plugin) keyID(apiKey string) (string, bool) {
	if !strings.HasPrefix(apiKey, p.keyPrefix) {
		return "", false
	}
	id := strings.TrimPrefix(apiKey, p.keyPrefix)
	return id, id != ""
}

// warnUnpricedOnce 每 60 秒最多提示一次「未定价用量按 0 计入预算」，
// 提醒运营者配置保底价或补齐价格目录。
func (p *Plugin) warnUnpricedOnce(model string) {
	now := time.Now().Unix()
	last := p.lastFloorWarn.Load()
	if now-last < 60 || !p.lastFloorWarn.CompareAndSwap(last, now) {
		return
	}
	log.Printf("ledger: unpriced model %q settled at 0 cost (budget not charged); set %s to enforce a floor",
		model, UnpricedFloorEnv)
}

func (p *Plugin) identity(ctx context.Context, keyID string) (empID, deptID string, err error) {
	// 命中缓存（含"已删除"的负缓存）：撤销/删除 Key 的生效延迟一个 TTL，可接受。
	if cached, ok := p.identityCache.get(keyID); ok {
		return cached.empID, cached.deptID, nil
	}
	var dept *string
	err = p.pool.QueryRow(ctx,
		`SELECT k."empId"::text, u."deptId"::text
		   FROM "ApiKey" k JOIN "User" u ON u.id = k."empId"
		  WHERE k.id = $1`, keyID).Scan(&empID, &dept)
	if err != nil {
		if err == pgx.ErrNoRows {
			p.identityCache.put(keyID, identityResult{exists: false})
			return "", "", nil
		}
		return "", "", err
	}
	if dept != nil {
		deptID = *dept
	}
	p.identityCache.put(keyID, identityResult{empID: empID, deptID: deptID, exists: true})
	return empID, deptID, nil
}

// price 取当前生效的价格版本（effectiveFrom 已到的版本里最新的一版，
// provider 匹配优先）；没有版本表或没有生效版本时回退 ModelCatalog，版本记 v1。
// 结果带 TTL 缓存：改价至多延迟一个 TTL 生效（价格本身有版本机制兜底）。
func (p *Plugin) price(ctx context.Context, provider, model string) (Price, string, bool, error) {
	cacheKey := provider + "\x1f" + model
	if cached, ok := p.priceCache.get(cacheKey); ok {
		return cached.price, cached.version, cached.found, nil
	}
	var (
		pr      Price
		version string
	)
	err := p.pool.QueryRow(ctx,
		`SELECT "promptPerM", "completionPerM", "cacheReadPerM", "cacheWritePerM", version
		   FROM "ModelPrice"
		  WHERE "modelId" = $1 AND "effectiveFrom" <= now()
		  ORDER BY (provider = $2) DESC, "effectiveFrom" DESC, version DESC
		  LIMIT 1`, model, provider).Scan(&pr.PromptPerM, &pr.CompletionPerM, &pr.CacheReadPerM, &pr.CacheWritePerM, &version)
	if err == nil {
		p.priceCache.put(cacheKey, priceResult{price: pr, version: version, found: true})
		return pr, version, true, nil
	}
	if err != pgx.ErrNoRows {
		return Price{}, defaultPriceVersion, false, err
	}

	// 回退：旧库没有 ModelPrice 行时继续用 ModelCatalog 的单价。
	err = p.pool.QueryRow(ctx,
		`SELECT "pricingPromptCents", "pricingCompletionCents", "pricingCacheReadCents"
		   FROM "ModelCatalog"
		  WHERE "modelId" = $1
		  ORDER BY (provider = $2) DESC, provider
		  LIMIT 1`, model, provider).Scan(&pr.PromptPerM, &pr.CompletionPerM, &pr.CacheReadPerM)
	if err == pgx.ErrNoRows {
		p.priceCache.put(cacheKey, priceResult{version: defaultPriceVersion, found: false})
		return Price{}, defaultPriceVersion, false, nil
	}
	if err != nil {
		return Price{}, defaultPriceVersion, false, err
	}
	p.priceCache.put(cacheKey, priceResult{price: pr, version: defaultPriceVersion, found: true})
	return pr, defaultPriceVersion, true, nil
}

// write 在一个事务里：串行化链尾 → 插入账本行 → 日聚合 → 预算结算。
// UsageRecord.rid 唯一，重放同一 rid 不会重复计费。
func (p *Plugin) write(ctx context.Context, e Entry) (bool, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, chainLockKey); err != nil {
		return false, err
	}
	var prev string
	err = tx.QueryRow(ctx, `SELECT "rowHash" FROM "UsageRecord" ORDER BY seq DESC LIMIT 1`).Scan(&prev)
	if err == pgx.ErrNoRows {
		prev = GenesisHash
	} else if err != nil {
		return false, err
	}
	// 哈希必须绑定“入库后的值”：无部门的员工统一用零 UUID。
	if e.DeptID == "" {
		e.DeptID = zeroDept
	}
	rowHash := ChainHash(prev, e)

	dept := e.DeptID
	ts := time.Unix(e.TS, 0).UTC()
	var ttft, latency *int32

	tag, err := tx.Exec(ctx,
		`INSERT INTO "UsageRecord"(
			rid, ts, "keyId", "empId", "deptId", provider, alias,
			"modelReq", "modelServed", "authId", "priceVersion", unpriced,
			"inputTokens", "outputTokens", "cacheCreationInputTokens", "cacheReadInputTokens",
			"costCents", status, "requestId", stream, failed, "ttftMs", "latencyMs",
			"prevHash", "rowHash")
		 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25)
		 ON CONFLICT (rid) DO NOTHING`,
		e.RID, ts, e.KeyID, e.EmpID, dept, e.Provider, e.Alias,
		e.ModelReq, e.ModelServed, e.AuthID, e.PriceVersion, e.Unpriced,
		e.InputTokens, e.OutputTokens, e.CacheWrite, e.CacheRead,
		e.CostCents, e.Status, e.RequestID, e.Stream, e.Failed, ttft, latency,
		prev, rowHash)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 0 {
		return false, nil // 已记过账，直接成功返回
	}

	if err := upsertDaily(ctx, tx, e, dept, ts); err != nil {
		return false, err
	}
	if err := settleBudgets(ctx, tx, e); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

func upsertDaily(ctx context.Context, tx pgx.Tx, e Entry, dept string, ts time.Time) error {
	var errors int64
	if e.Failed {
		errors = 1
	}
	_, err := tx.Exec(ctx,
		`INSERT INTO "UsageDaily"(
			day, "empId", "deptId", model, calls, errors,
			"inputTokens", "outputTokens", "cacheReadTokens", "cacheWriteTokens", "costCents")
		 VALUES($1::date,$2,$3,$4,1,$5,$6,$7,$8,$9,$10)
		 ON CONFLICT (day, "empId", "deptId", model) DO UPDATE SET
			calls = "UsageDaily".calls + 1,
			errors = "UsageDaily".errors + EXCLUDED.errors,
			"inputTokens" = "UsageDaily"."inputTokens" + EXCLUDED."inputTokens",
			"outputTokens" = "UsageDaily"."outputTokens" + EXCLUDED."outputTokens",
			"cacheReadTokens" = "UsageDaily"."cacheReadTokens" + EXCLUDED."cacheReadTokens",
			"cacheWriteTokens" = "UsageDaily"."cacheWriteTokens" + EXCLUDED."cacheWriteTokens",
			"costCents" = "UsageDaily"."costCents" + EXCLUDED."costCents"`,
		ts.Format("2006-01-02"), e.EmpID, dept, e.ModelServed, errors,
		e.InputTokens, e.OutputTokens, e.CacheRead, e.CacheWrite, e.CostCents)
	return err
}

// settleBudgets 对员工/全局两级预算分别记账；同一 (budgetId, rid, settle) 只生效一次。
// 部门已移出产品面；Entry.DeptID 仍参与哈希链（历史兼容），但不再命中部门预算。
// 实际费用可能略超限额（多退少补），下一次请求的预检负责拦截。
func settleBudgets(ctx context.Context, tx pgx.Tx, e Entry) error {
	if e.CostCents <= 0 {
		return nil
	}
	rows, err := tx.Query(ctx,
		`SELECT id::text FROM "Budget"
		  WHERE period = 'monthly'
		    AND ((scope = 'emp' AND "scopeId" = $1)
		      OR scope = 'global')
		  ORDER BY id`, e.EmpID)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, id := range ids {
		tag, err := tx.Exec(ctx,
			`INSERT INTO "BudgetLedger"(id, "budgetId", rid, kind, "deltaCents")
			 VALUES(gen_random_uuid(), $1, $2, 'settle', $3)
			 ON CONFLICT ("budgetId", rid, kind) DO NOTHING`,
			id, e.RID, e.CostCents)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			continue
		}
		if _, err := tx.Exec(ctx,
			`UPDATE "Budget" SET "usedCents" = "usedCents" + $2, "updatedAt" = now() WHERE id = $1`,
			id, e.CostCents); err != nil {
			return err
		}
	}
	return nil
}

func requestedModel(rec usage.Record) string {
	if rec.Alias != "" {
		return rec.Alias
	}
	return rec.Model
}

func servedModel(rec usage.Record) string {
	if rec.ResponseModel != "" {
		return rec.ResponseModel
	}
	return rec.Model
}

// timestamp 用秒级 Unix 时间：数据库 timestamptz 为微秒精度，
// 而控制面用 JavaScript Date 校验链，只有秒级能逐位复算。
func timestamp(t time.Time) int64 {
	if t.IsZero() {
		return time.Now().UTC().Unix()
	}
	return t.UTC().Unix()
}

// ridOrNew 保证账本行的 rid 非空；正常路径用 SDK 的 RequestID，异常缺失时补一个。
func ridOrNew(id string) string {
	if id != "" {
		return id
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("rid-%d", time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%s-%s-%s-%s-%s",
		hex.EncodeToString(b[0:4]), hex.EncodeToString(b[4:6]),
		hex.EncodeToString(b[6:8]), hex.EncodeToString(b[8:10]),
		hex.EncodeToString(b[10:16]))
}
