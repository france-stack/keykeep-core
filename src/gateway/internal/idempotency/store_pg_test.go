package idempotency

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// 这些用例需要一个可用且已跑过迁移的 PostgreSQL。
//
// 设置 KEYKEEP_TEST_DATABASE_URL 才执行，否则跳过——CI 没有数据库服务，
// 用环境变量把「没测」和「测过了」区分开，而不是让它悄悄通过。
func testDB(t *testing.T) string {
	t.Helper()
	url := os.Getenv("KEYKEEP_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("未设置 KEYKEEP_TEST_DATABASE_URL，跳过数据库用例")
	}
	return url
}

func dbPool(t *testing.T, url string) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatalf("连库失败: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func insertClaim(t *testing.T, pool *pgxpool.Pool, keyID, requestID, status string) {
	t.Helper()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO "RequestClaim"(
			id, "keyId", "empId", "requestId", "payloadHash", path, model, stream,
			status, "statusCode", "errorCode", "updatedAt", "expiresAt")
		 VALUES(gen_random_uuid(), $1::uuid, $2::uuid, $3, $4, '/v1/messages', '', false,
		        $5, NULL, '', now(), now() + interval '1 hour')
		 ON CONFLICT ("keyId", "requestId") DO UPDATE SET status = EXCLUDED.status`,
		keyID, keyID, requestID, "hash-"+requestID, status)
	if err != nil {
		t.Fatalf("写入声明失败: %v", err)
	}
}

func claimStatus(t *testing.T, pool *pgxpool.Pool, keyID, requestID string) string {
	t.Helper()
	var status string
	err := pool.QueryRow(context.Background(),
		`SELECT status FROM "RequestClaim" WHERE "keyId" = $1::uuid AND "requestId" = $2`,
		keyID, requestID).Scan(&status)
	if err != nil {
		t.Fatalf("读声明失败: %v", err)
	}
	return status
}

// 两个进程抢同一个库时，第二个必须拿到 ErrAlreadyRunning。
func TestGuardRejectsSecondInstance(t *testing.T) {
	url := testDB(t)
	ctx := context.Background()

	first, err := AcquireGuard(ctx, url)
	if err != nil {
		t.Fatalf("第一个实例应当持有锁: %v", err)
	}
	defer first.Close()

	if _, err := AcquireGuard(ctx, url); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("第二个实例应当被拒绝，实际 err=%v", err)
	}
}

// 释放之后名额要能重新拿到，否则进程重启就再也起不来了。
func TestGuardReleasesLock(t *testing.T) {
	url := testDB(t)
	ctx := context.Background()

	first, err := AcquireGuard(ctx, url)
	if err != nil {
		t.Fatalf("第一个实例应当持有锁: %v", err)
	}
	first.Close()

	second, err := AcquireGuard(ctx, url)
	if err != nil {
		t.Fatalf("释放后应当可以重新获取: %v", err)
	}
	second.Close()
}

// 新进程启动时，上一个进程遗留的 in_progress 必须立刻收敛。
func TestSweepConvergesInterruptedClaims(t *testing.T) {
	url := testDB(t)
	ctx := context.Background()
	pool := dbPool(t, url)

	keyID := "00000000-0000-0000-0000-0000000000aa"
	interrupted := "req-interrupted"
	completed := "req-completed"
	failed := "req-failed"
	insertClaim(t, pool, keyID, interrupted, StatusInProgress)
	insertClaim(t, pool, keyID, completed, StatusCompleted)
	insertClaim(t, pool, keyID, failed, StatusFailed)

	store, err := NewStore(ctx, url)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	if got := claimStatus(t, pool, keyID, interrupted); got != StatusOutcomeUnknown {
		t.Fatalf("在途声明应变为 %s，实际 %s", StatusOutcomeUnknown, got)
	}
	if got := claimStatus(t, pool, keyID, completed); got != StatusCompleted {
		t.Fatalf("已完成声明不应被改写，实际 %s", got)
	}
	if got := claimStatus(t, pool, keyID, failed); got != StatusFailed {
		t.Fatalf("已失败声明不应被改写，实际 %s", got)
	}

	var errCode string
	if err := pool.QueryRow(ctx,
		`SELECT "errorCode" FROM "RequestClaim" WHERE "keyId" = $1::uuid AND "requestId" = $2`,
		keyID, interrupted).Scan(&errCode); err != nil {
		t.Fatalf("读 errorCode: %v", err)
	}
	if errCode != "process_restart" {
		t.Fatalf("errorCode 应为 process_restart，实际 %q", errCode)
	}
}

// 已经被占用时，NewStore 必须在碰任何声明之前失败。
func TestNewStoreDoesNotSweepWithoutGuard(t *testing.T) {
	url := testDB(t)
	ctx := context.Background()
	pool := dbPool(t, url)

	holder, err := AcquireGuard(ctx, url)
	if err != nil {
		t.Fatalf("占位实例应当持有锁: %v", err)
	}
	defer holder.Close()

	keyID := "00000000-0000-0000-0000-0000000000bb"
	requestID := "req-should-stay"
	insertClaim(t, pool, keyID, requestID, StatusInProgress)

	if _, err := NewStore(ctx, url); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("NewStore 应返回 ErrAlreadyRunning，实际 %v", err)
	}
	if got := claimStatus(t, pool, keyID, requestID); got != StatusInProgress {
		t.Fatalf("被拒绝的实例不应改动声明，实际状态 %s", got)
	}
}

// 心跳周期内连接被外部断开后，锁应当能重新拿回来。
func TestGuardReacquiresAfterReconnect(t *testing.T) {
	url := testDB(t)
	ctx := context.Background()

	guard, err := AcquireGuard(ctx, url)
	if err != nil {
		t.Fatalf("获取锁: %v", err)
	}
	defer guard.Close()

	// 模拟连接断开：直接关掉持锁连接，再触发一次检查。
	guard.mu.Lock()
	conn := guard.conn
	guard.mu.Unlock()
	_ = conn.Close(context.Background())

	guard.check()

	if !guard.Held() {
		t.Fatalf("重连后应当重新持有锁")
	}

	// 重新持锁之后，别的实例仍然拿不到。
	if _, err := AcquireGuard(ctx, url); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("重连后第二个实例仍应被拒绝，实际 err=%v", err)
	}
}
