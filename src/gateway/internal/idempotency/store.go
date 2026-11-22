// Package idempotency 在网关边缘实现请求声明：同一虚拟 Key + X-Request-Id 只执行一次。
//
// 声明表 RequestClaim 由网关独占读写，只保存元数据与终态，不存请求/响应正文。
// 客户端不传 X-Request-Id 时不做幂等（保持兼容）。
package idempotency

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/keykeep/gateway/internal/dbpool"
)

const (
	StatusInProgress     = "in_progress"
	StatusCompleted      = "completed"
	StatusFailed         = "failed"
	StatusOutcomeUnknown = "outcome_unknown"

	// claimTTL 是终态声明保留时长：期间重复请求仍会被拒绝。
	claimTTL = 24 * time.Hour
)

type Claim struct {
	ID          string
	KeyID       string
	EmpID       string
	RequestID   string
	PayloadHash string
	Path        string
	Model       string
	Stream      bool
	Status      string
	StatusCode  *int
	ErrorCode   string
}

// Store 是 RequestClaim 表的网关侧访问层。
type Store struct {
	pool  *pgxpool.Pool
	guard *Guard
	stop  chan struct{}
}

func NewStore(ctx context.Context, databaseURL string) (*Store, error) {
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
	// 先抢单实例名额，再动声明表。顺序不能反：干净启动时把残留声明收敛为
	// outcome_unknown 只在一个进程持锁时才是安全的（多实例会把别的实例在途的
	// 请求一起作废）。拿不到锁就直接退出，一行声明都不碰。
	guard, err := AcquireGuard(ctx, databaseURL)
	if err != nil {
		pool.Close()
		return nil, err
	}
	s := &Store{pool: pool, guard: guard, stop: make(chan struct{})}
	s.sweepStartup(ctx)
	go s.cleanupLoop()
	return s, nil
}

func (s *Store) Close() {
	close(s.stop)
	s.pool.Close()
	s.guard.Close()
}

// sweepStartup 把上一个进程遗留的 in_progress 声明标为 outcome_unknown。
//
// 不设时间阈值：进程一死，它在途的请求就不可能再完成，立刻收敛才能让员工的
// 同 id 重试拿到确定的终态（outcome_unknown），而不是卡在 request_in_progress
// 等一个永远不会到来的结果。
//
// 这条语句的正确性完全依赖「同一时刻只有一个网关进程」——由 Guard 的会话级
// 咨询锁保证。多实例部署前必须换成带心跳的租约表（按实例 id + 过期时间），
// 否则第二个实例启动会把第一个实例在途的请求全部作废。
func (s *Store) sweepStartup(ctx context.Context) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE "RequestClaim" SET status = $1, "errorCode" = 'process_restart', "updatedAt" = now()
		  WHERE status = $2`,
		StatusOutcomeUnknown, StatusInProgress)
	if err != nil {
		log.Printf("idempotency: startup sweep: %v", err)
		return
	}
	if n := tag.RowsAffected(); n > 0 {
		log.Printf("idempotency: marked %d interrupted claim(s) as outcome_unknown", n)
	}
}

func (s *Store) cleanupLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if _, err := s.pool.Exec(ctx, `DELETE FROM "RequestClaim" WHERE "expiresAt" < now()`); err != nil {
				log.Printf("idempotency: cleanup: %v", err)
			}
			cancel()
		}
	}
}

// Acquire 原子声明一次请求：claimed 非空表示新声明，existing 非空表示冲突。
//
// INSERT 冲突后的 SELECT 可能落空：并发 Finalize(status="") 在两条语句之间
// 删掉了声明。落空不是错误——声明已被释放，重新 INSERT 即可；不做这一步
// 会把正常的重试窗口变成偶发 503。
func (s *Store) Acquire(ctx context.Context, c Claim) (claimed *Claim, existing *Claim, err error) {
	statusCode := c.StatusCode
	for attempt := 0; ; attempt++ {
		var id string
		err = s.pool.QueryRow(ctx,
			`INSERT INTO "RequestClaim"(
				id, "keyId", "empId", "requestId", "payloadHash", path, model, stream,
				status, "statusCode", "errorCode", "updatedAt", "expiresAt")
			 VALUES(gen_random_uuid(), $1::uuid, $2::uuid, $3, $4, $5, $6, $7, $8, $9, $10, now(), now() + $11::interval)
			 ON CONFLICT ("keyId", "requestId") DO NOTHING
			 RETURNING id::text`,
			c.KeyID, c.EmpID, c.RequestID, c.PayloadHash, c.Path, c.Model, c.Stream,
			StatusInProgress, statusCode, "", claimTTL.String()).Scan(&id)
		if err == nil {
			c.ID = id
			c.Status = StatusInProgress
			return &c, nil, nil
		}
		if err != pgx.ErrNoRows {
			return nil, nil, err
		}

		existing = &Claim{}
		var sc *int
		err = s.pool.QueryRow(ctx,
			`SELECT id::text, "keyId"::text, "empId"::text, "requestId", "payloadHash",
			        path, model, stream, status, "statusCode", "errorCode"
			   FROM "RequestClaim" WHERE "keyId" = $1::uuid AND "requestId" = $2`,
			c.KeyID, c.RequestID).Scan(&existing.ID, &existing.KeyID, &existing.EmpID,
			&existing.RequestID, &existing.PayloadHash, &existing.Path, &existing.Model,
			&existing.Stream, &existing.Status, &sc, &existing.ErrorCode)
		if err == nil {
			existing.StatusCode = sc
			return nil, existing, nil
		}
		if err != pgx.ErrNoRows {
			return nil, nil, err
		}
		// 声明在 INSERT 与 SELECT 之间被删除；回到循环重新声明。
		// 上限防御极端抖动下的无限往返（每次往返都是一次真实的释放-重占）。
		if attempt >= 4 {
			return nil, nil, errors.New("idempotency: claim churn exceeded retry budget")
		}
	}
}

// Finalize 写终态；status 为空表示删除声明（网关自身可重试的拒绝）。
func (s *Store) Finalize(ctx context.Context, id, status string, statusCode int, errorCode string) error {
	if status == "" {
		_, err := s.pool.Exec(ctx, `DELETE FROM "RequestClaim" WHERE id = $1::uuid`, id)
		return err
	}
	_, err := s.pool.Exec(ctx,
		`UPDATE "RequestClaim"
		    SET status = $2, "statusCode" = $3, "errorCode" = $4,
		        "updatedAt" = now(), "expiresAt" = now() + $5::interval
		  WHERE id = $1::uuid`,
		id, status, statusCode, errorCode, claimTTL.String())
	return err
}
