package ratelimit

import (
	"context"
	"log"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/keykeep/gateway/internal/dbpool"
)

// PGCounters 用 PostgreSQL 维护跨实例共享的 60 秒滑动窗口计数。
//
// 每次 Admit 是一个短事务：先按 Key 取事务级 advisory lock，再汇总窗口内的计数，
// 判定通过后在同一事务里累加本次估值。锁保证同 Key 的并发请求不会同时越过限额；
// 不同 Key 互不阻塞。Release 只回补 token 估值，不清除 RPM（与内存实现一致）。
type PGCounters struct {
	pool *pgxpool.Pool
	now  func() time.Time
	stop chan struct{}
}

// NewPGCounters 建连pg 并启动过期计数清理（每 60 秒删除 120 秒前的行）。
func NewPGCounters(ctx context.Context, databaseURL string) (*PGCounters, error) {
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
	p := &PGCounters{pool: pool, now: time.Now, stop: make(chan struct{})}
	go p.cleanupLoop()
	return p, nil
}

func (p *PGCounters) Close() {
	close(p.stop)
	p.pool.Close()
}

func (p *PGCounters) cleanupLoop() {
	ticker := time.NewTicker(WindowSeconds * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-p.stop:
			return
		case now := <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_, err := p.pool.Exec(ctx,
				`DELETE FROM "RateWindow" WHERE second < $1`, now.Unix()-2*WindowSeconds)
			cancel()
			if err != nil {
				log.Printf("ratelimit: cleanup rate window: %v", err)
			}
		}
	}
}

type windowState struct {
	total  int64
	oldest int64
}

// Admit 在事务内完成"汇总 → 判定 → 记账"，失败时不占用任何计数。
func (p *PGCounters) Admit(ctx context.Context, keyID string, cfg Config, estIn, estOut int64) (*Reservation, *Denial, error) {
	if cfg.IsZero() {
		return nil, nil, nil
	}
	now := p.now().Unix()

	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// 64 位哈希：hashtext 是 32 位，不同 Key 碰撞会互相串行化；
	// hashtextextended（PG14+）降到可忽略的碰撞概率。
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1::text, 0))`, keyID); err != nil {
		return nil, nil, err
	}

	states, err := windowSums(ctx, tx, keyID, now)
	if err != nil {
		return nil, nil, err
	}
	window := func(dim string) windowState {
		if s, ok := states[dim]; ok {
			return s
		}
		return windowState{oldest: now}
	}
	// 与内存实现统一口径（见 Denial 注释）：取相关维度中最早一条计数的
	// 过期时刻。此前这里取 max（最晚过期），与内存的 min 相反，同一请求
	// 在两种后端下拿到矛盾的 Retry-After。
	wait := func(dims ...string) int64 {
		oldest := int64(-1)
		for _, dim := range dims {
			if s := window(dim); s.total > 0 && (oldest < 0 || s.oldest < oldest) {
				oldest = s.oldest
			}
		}
		if oldest < 0 {
			return 1
		}
		if w := oldest + WindowSeconds - now; w >= 1 {
			return w
		}
		return 1
	}

	inSum, outSum, rpmSum := window("i").total, window("o").total, window("r").total
	if cfg.RPM > 0 && rpmSum+1 > cfg.RPM {
		return nil, &Denial{Dim: "rpm", RetryAfter: wait("r"), Remaining: max(cfg.RPM-rpmSum, 0)}, nil
	}
	if cfg.TPM > 0 && inSum+outSum+estIn+estOut > cfg.TPM {
		return nil, &Denial{
			Dim:        "tpm",
			RetryAfter: wait("i", "o"),
			Remaining:  max(cfg.TPM-inSum-outSum, 0),
		}, nil
	}
	if cfg.ITPM > 0 && inSum+estIn > cfg.ITPM {
		return nil, &Denial{Dim: "itpm", RetryAfter: wait("i"), Remaining: max(cfg.ITPM-inSum, 0)}, nil
	}
	if cfg.OTPM > 0 && outSum+estOut > cfg.OTPM {
		return nil, &Denial{Dim: "otpm", RetryAfter: wait("o"), Remaining: max(cfg.OTPM-outSum, 0)}, nil
	}

	for _, item := range []struct {
		dim    string
		amount int64
	}{{"r", 1}, {"i", estIn}, {"o", estOut}} {
		if item.amount <= 0 {
			continue
		}
		if err := addWindow(ctx, tx, keyID, now, item.dim, item.amount); err != nil {
			return nil, nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, nil, err
	}

	return newReservation(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for dim, amount := range map[string]int64{"i": estIn, "o": estOut} {
			if amount <= 0 {
				continue
			}
			if _, err := p.pool.Exec(ctx,
				`UPDATE "RateWindow" SET amount = GREATEST(amount - $4, 0)
				  WHERE "keyId" = $1::uuid AND second = $2 AND dim = $3`,
				keyID, now, dim, amount); err != nil {
				log.Printf("ratelimit: release estimate: %v", err)
			}
		}
	}), nil, nil
}

func (p *PGCounters) AddActual(ctx context.Context, keyID string, inputTokens, outputTokens int64) error {
	if keyID == "" || (inputTokens <= 0 && outputTokens <= 0) {
		return nil
	}
	now := p.now().Unix()
	for _, item := range []struct {
		dim    string
		amount int64
	}{{"i", inputTokens}, {"o", outputTokens}} {
		if item.amount <= 0 {
			continue
		}
		if err := addWindow(ctx, p.pool, keyID, now, item.dim, item.amount); err != nil {
			return err
		}
	}
	return nil
}

func (p *PGCounters) Snapshot(ctx context.Context, keyID string) (rpm, inTokens, outTokens int64, err error) {
	states, err := windowSums(ctx, p.pool, keyID, p.now().Unix())
	if err != nil {
		return 0, 0, 0, err
	}
	return states["r"].total, states["i"].total, states["o"].total, nil
}

func windowSums(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, keyID string, now int64) (map[string]windowState, error) {
	rows, err := q.Query(ctx,
		`SELECT dim,
		        COALESCE(SUM(amount), 0)::bigint,
		        COALESCE(MIN(second) FILTER (WHERE amount > 0), $2)::bigint
		   FROM "RateWindow"
		  WHERE "keyId" = $1::uuid AND second > $2 - $3
		  GROUP BY dim`, keyID, now, WindowSeconds)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	states := make(map[string]windowState, 3)
	for rows.Next() {
		var (
			dim    string
			state  windowState
			oldest int64
		)
		if err := rows.Scan(&dim, &state.total, &oldest); err != nil {
			return nil, err
		}
		state.oldest = oldest
		states[dim] = state
	}
	return states, rows.Err()
}

func addWindow(ctx context.Context, q interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}, keyID string, second int64, dim string, amount int64) error {
	_, err := q.Exec(ctx,
		`INSERT INTO "RateWindow"("keyId", second, dim, amount)
		 VALUES($1::uuid, $2, $3, $4)
		 ON CONFLICT ("keyId", second, dim) DO UPDATE
		 SET amount = "RateWindow".amount + EXCLUDED.amount`,
		keyID, second, dim, amount)
	return err
}
