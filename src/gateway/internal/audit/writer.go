// Package audit 把数据面的拒绝事件写入控制面的 AuditEvent 表。
//
// 写入是旁路：任何错误只记日志，绝不改变转发结果，也不阻塞上游响应。
//
// 写入同时接进防篡改链（与控制面 lib/audit.ts 同一套规则）：链只认「库里读回来
// 的那一行」，所以一条事件要走 insert → 回读 → 算摘要 → 回填四步，并在同一事务里
// 持审计链锁。以前这里只做一条裸 INSERT，行落库后 rowHash 恒为 NULL——控制面
// 全量走链时，网关事件会变成链中间的成段空洞，数据面的一半审计在取证场景里失效。
package audit

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/keykeep/gateway/internal/dbpool"
)

// Writer 是网关侧审计写入口；actorID 为虚拟 Key 的 id（与 UsageRecord.keyId 同源）。
type Writer struct {
	pool     *pgxpool.Pool
	chainKey string
}

// NewWriter 建连并读取链密钥。KEYKEEP_AUDIT_CHAIN_KEY 与控制面共用同一个值：
// 两边配置不一致会让所有网关行在走链时报 hash_mismatch，所以启动时就把
// 不一致这件事说清楚，而不是等审计页某天变红。
func NewWriter(ctx context.Context, databaseURL string) (*Writer, error) {
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
	chainKey := os.Getenv("KEYKEEP_AUDIT_CHAIN_KEY")
	if chainKey == "" {
		log.Printf("audit: KEYKEEP_AUDIT_CHAIN_KEY 未设置，审计事件按裸 sha256 入链（无法抵御删尾重算）")
	}
	return &Writer{pool: pool, chainKey: chainKey}, nil
}

func (w *Writer) Close() { w.pool.Close() }

// Record 写入一条审计事件。detail 不得包含凭据、Key 明文或载荷正文。
func (w *Writer) Record(ctx context.Context, action, actorID string, detail map[string]any, requestID string) {
	if w == nil {
		return
	}
	// 审计是旁路，但旁路不等于可以半途而废：一次写不进去就重试一次。
	// 首次尝试用请求上下文之外的超时（请求已被客户端取消时仍要留痕）。
	for attempt := 0; attempt < 2; attempt++ {
		writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		err := w.writeChain(writeCtx, action, actorID, detail, requestID)
		cancel()
		if err == nil {
			observeAuditWrite(true)
			return
		}
		// 唯一键/序列类错误重试没有意义，直接记日志退出。
		if !isRetryable(err) {
			observeAuditWrite(false)
			log.Printf("audit: write %s: %v", action, err)
			return
		}
	}
	observeAuditWrite(false)
}

// writeChain 在一个事务里完成：取链锁 → 读链头 → 插入 → 回读 → 算摘要 → 回填。
func (w *Writer) writeChain(ctx context.Context, action, actorID string, detail map[string]any, requestID string) error {
	payload, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	var actor *string
	if actorID != "" {
		actor = &actorID
	}
	var rid *string
	if requestID != "" {
		rid = &requestID
	}

	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// 与账本 7411001、幂等守卫 7411002 同族；同一把锁也保护「读链头 → 写链尾」
	// 这一段，否则两个并发写会各自读到同一个链头，分叉出两条 prevHash 相同的链。
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, ChainLockKey); err != nil {
		return err
	}

	var prevHash string
	err = tx.QueryRow(ctx,
		`SELECT COALESCE((SELECT "rowHash" FROM "AuditEvent"
		                  WHERE "rowHash" IS NOT NULL
		                  ORDER BY seq DESC LIMIT 1), $1)`, GenesisHash).Scan(&prevHash)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if prevHash == "" {
		prevHash = GenesisHash
	}

	var id string
	if err := tx.QueryRow(ctx,
		`INSERT INTO "AuditEvent"(id, at, "actorId", action, detail, "requestId")
		 VALUES(gen_random_uuid(), now(), $1, $2, $3::jsonb, $4)
		 RETURNING id::text`,
		actor, action, string(payload), rid).Scan(&id); err != nil {
		return err
	}

	// 摘要只认库里的值：JSONB 会归一化键序与数字，拿写入前的 Go map 算，
	// 控制面校验时永远复算不出来。
	var (
		seq       int64
		at        time.Time
		storedAct *string
		storedDet []byte
		storedRID *string
	)
	if err := tx.QueryRow(ctx,
		`SELECT seq, at, "actorId", action, detail, "requestId"
		   FROM "AuditEvent" WHERE id = $1::uuid`, id).
		Scan(&seq, &at, &storedAct, &action, &storedDet, &storedRID); err != nil {
		return err
	}

	detailMap := map[string]any{}
	if len(storedDet) > 0 {
		if err := json.Unmarshal(storedDet, &detailMap); err != nil {
			return err
		}
	}
	row := ChainRow{
		Seq:     seq,
		At:      TruncateToMillis(at),
		Action:  action,
		Detail:  detailMap,
		ActorID: deref(storedAct),
	}
	row.RequestID = deref(storedRID)

	algo := AlgoFor(w.chainKey)
	rowHash, err := ChainHash(prevHash, row, algo, w.chainKey)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE "AuditEvent" SET "prevHash" = $1, "rowHash" = $2, "hashAlgo" = $3
		  WHERE id = $4::uuid`, prevHash, rowHash, algo, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// isRetryable 区分「数据库暂时不可用」与「这条数据本身写不进去」。
// 只有前者值得重试；后者重试两次也只是把同一条错误日志抄两遍。
func isRetryable(err error) bool {
	var pgErr interface{ SQLState() string }
	if errors.As(err, &pgErr) {
		state := pgErr.SQLState()
		// 08xxx 连接异常、40P01 死锁、40001 序列化失败、53xxx 资源不足。
		return len(state) >= 2 && (state[:2] == "08" || state == "40P01" || state == "40001" || state[:2] == "53")
	}
	return false
}

// ------------------------------------------------------------------ 指标

// metricsSink 是可选的审计写入计数出口；nil 时不统计。
// 用最小接口而不是具体类型：依赖方向必须单向（metrics 不反过来 import audit）。
var metricsSink atomic.Value

// SetMetrics 注册指标出口。
func (w *Writer) SetMetrics(sink interface{ ObserveAuditWrite(ok bool) }) {
	if sink == nil {
		return
	}
	metricsSink.Store(sink)
}

func observeAuditWrite(ok bool) {
	if sink, loaded := metricsSink.Load().(interface{ ObserveAuditWrite(ok bool) }); loaded && sink != nil {
		sink.ObserveAuditWrite(ok)
	}
}
