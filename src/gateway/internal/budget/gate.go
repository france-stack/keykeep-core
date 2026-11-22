// Package budget 实现两级预算预检与预扣：员工 / 全局取最严。
//
// 预扣在请求进入网关时按预估 token 完成，请求结束后由中间件精确释放；
// 实际费用由 ledger 插件按用量结算。进程异常退出会遗留 reservedCents，
// 因此在 NewGate 启动时清零（单实例部署；多实例需改为带租约的预扣表）。
package budget

import (
	"context"
	"log"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/keykeep/gateway/internal/dbpool"
)

const periodMonthly = "monthly"

type Gate struct {
	pool *pgxpool.Pool
	// UnpricedFloorPerM 未定价模型的保底单价（分/百万 token）；0 表示不保底。
	// 与 ledger 的保底结算口径一致，由装配方统一注入。
	UnpricedFloorPerM int64
}

type reservedRow struct {
	id     string
	amount int64
}

// Reservation 是一次预扣的结果；无论允许与否都必须调用 Release。
type Reservation struct {
	gate      *Gate
	allowed   bool
	scope     string
	remaining int64
	rows      []reservedRow
}

func (r *Reservation) Allowed() bool         { return r.allowed }
func (r *Reservation) RemainingCents() int64 { return r.remaining }
func (r *Reservation) Scope() string         { return r.scope }

func NewGate(ctx context.Context, databaseURL string) (*Gate, error) {
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
	g := &Gate{pool: pool}
	g.resetStaleReservations(ctx)
	return g, nil
}

func (g *Gate) Close() { g.pool.Close() }

// resetStaleReservations 清理上次进程遗留的预扣，避免额度永久占用。
func (g *Gate) resetStaleReservations(ctx context.Context) {
	tag, err := g.pool.Exec(ctx,
		`UPDATE "Budget" SET "reservedCents" = 0 WHERE "reservedCents" <> 0`)
	if err != nil {
		log.Printf("budget: reset stale reservations: %v", err)
		return
	}
	if n := tag.RowsAffected(); n > 0 {
		log.Printf("budget: cleared %d stale reservation(s) from previous run", n)
	}
}

// Price 读取模型单价（分 / 1M token）；查不到返回 0 表示未定价。
func (g *Gate) Price(ctx context.Context, model string) (prompt, completion int64) {
	if model == "" {
		return 0, 0
	}
	err := g.pool.QueryRow(ctx,
		`SELECT c."pricingPromptCents", c."pricingCompletionCents"
		   FROM "ModelMap" m JOIN "ModelCatalog" c ON c."modelId" = m."modelId"
		  WHERE m.alias = $1
		  LIMIT 1`, model).Scan(&prompt, &completion)
	if err != nil {
		return 0, 0
	}
	return prompt, completion
}

// Reserve 在事务内对员工/全局两级预算行加行锁并预扣。
// estimate 为 0 时只做余额检查，不占用额度。
func (g *Gate) Reserve(ctx context.Context, empID string, estimate int64) (*Reservation, error) {
	tx, err := g.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx,
		`SELECT id::text, scope, "limitCents" - "usedCents" - "reservedCents"
		   FROM "Budget"
		  WHERE period = $1
		    AND ((scope = 'emp' AND "scopeId" = $2)
		      OR scope = 'global')
		  ORDER BY id
		  FOR UPDATE`, periodMonthly, empID)
	if err != nil {
		return nil, err
	}
	type level struct {
		id        string
		scope     string
		remaining int64
	}
	levels, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (level, error) {
		var l level
		err := row.Scan(&l.id, &l.scope, &l.remaining)
		return l, err
	})
	if err != nil {
		return nil, err
	}
	if len(levels) == 0 {
		return &Reservation{gate: g, allowed: true}, nil
	}

	tightest := levels[0]
	for _, l := range levels[1:] {
		if l.remaining < tightest.remaining {
			tightest = l
		}
	}
	// 余额已耗尽时必须拒绝：estimate 为 0（如小请求估算不出费用）也不能放行，
	// 否则“先放行再结算”会让已用完的预算继续被穿透。
	if tightest.remaining <= 0 || estimate > tightest.remaining {
		return &Reservation{gate: g, scope: tightest.scope, remaining: tightest.remaining}, nil
	}

	res := &Reservation{gate: g, allowed: true, scope: tightest.scope, remaining: tightest.remaining}
	if estimate > 0 {
		for _, l := range levels {
			if _, err := tx.Exec(ctx,
				`UPDATE "Budget" SET "reservedCents" = "reservedCents" + $2, "updatedAt" = now()
				  WHERE id = $1`, l.id, estimate); err != nil {
				return nil, err
			}
			res.rows = append(res.rows, reservedRow{id: l.id, amount: estimate})
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return res, nil
}

// Release 释放本次预扣；已失败/未预扣时为无操作，可重复调用。
func (r *Reservation) Release(ctx context.Context) {
	if r == nil || r.gate == nil || len(r.rows) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	for _, row := range r.rows {
		if _, err := r.gate.pool.Exec(ctx,
			`UPDATE "Budget" SET "reservedCents" = GREATEST("reservedCents" - $2, 0), "updatedAt" = now()
			  WHERE id = $1`, row.id, row.amount); err != nil {
			log.Printf("budget: release reservation %s: %v", row.id, err)
		}
	}
	r.rows = nil
}
