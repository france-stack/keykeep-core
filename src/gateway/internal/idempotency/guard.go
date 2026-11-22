package idempotency

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// instanceLockKey 是「只有一个网关进程」的咨询锁编号。
//
// 取值约定：账本哈希链用 7411001（internal/ledger），限流用 hashtextextended(keyId, 0)，
// 这里取 7411002，三者不重叠。
const instanceLockKey int64 = 7411002

// guardHeartbeat 是持有锁的连接探活周期。
const guardHeartbeat = 15 * time.Second

// ErrAlreadyRunning 表示同一个库上已有另一个网关实例持锁。
var ErrAlreadyRunning = errors.New("另一个 KeyKeep 网关实例正在运行（同一个数据库）")

// Guard 用 PostgreSQL 会话级咨询锁保证单实例。
//
// 为什么必须是会话级锁 + 独立连接：pool 会在语句之间轮换底层连接，
// 会话级锁随连接一起消失，锁可能在无感知的情况下被释放。因此这里用一条
// 独占的 pgx.Conn 承载锁，直到 Close。
//
// 它解决的是「重复双击启动」这个真实场景：sweepStartup 在监听端口之前执行
// （cmd/gateway/main.go 先建 Store 再 ListenAndServe），若没有闸门，后来者
// 会先把先进者在途的声明扫成 outcome_unknown，然后自己 bind 失败退出——
// 先进者的请求被无辜作废。
type Guard struct {
	dbURL string
	stop  chan struct{}
	done  chan struct{}
	once  sync.Once

	mu   sync.Mutex // 保护 conn 与 held；heartbeat 会换连接
	conn *pgx.Conn
	held bool
}

// AcquireGuard 尝试占住单实例名额。拿不到返回 ErrAlreadyRunning。
func AcquireGuard(ctx context.Context, databaseURL string) (*Guard, error) {
	conn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		return nil, err
	}
	ok, err := tryLock(ctx, conn)
	if err != nil {
		_ = conn.Close(context.Background())
		return nil, err
	}
	if !ok {
		_ = conn.Close(context.Background())
		return nil, ErrAlreadyRunning
	}
	g := &Guard{dbURL: databaseURL, stop: make(chan struct{}), done: make(chan struct{}), conn: conn, held: true}
	go g.heartbeat()
	return g, nil
}

// tryLock 抢一次会话级锁（不阻塞）。
func tryLock(ctx context.Context, conn *pgx.Conn) (bool, error) {
	var ok bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, instanceLockKey).Scan(&ok); err != nil {
		return false, err
	}
	return ok, nil
}

// heartbeat 发现连接断了就地重连并重新抢锁。
//
// 抢不回来只记日志，不杀进程：数据库抖动时把网关停掉，比让它在「单实例
// 保证暂时失效」的窗口里继续服务更糟——后者最坏是两个实例各扫一次声明的
// 理论风险（此时通常也没有真实并发写），前者是立刻全员不可用。
// 这条取舍在 docs/implementation/06-known-limits.md 有登记。
func (g *Guard) heartbeat() {
	defer close(g.done)
	ticker := time.NewTicker(guardHeartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-g.stop:
			return
		case <-ticker.C:
			g.check()
		}
	}
}

func (g *Guard) check() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	g.mu.Lock()
	conn := g.conn
	g.mu.Unlock()
	if conn == nil {
		return // 已 Close
	}
	if err := conn.Ping(ctx); err == nil {
		return
	}

	_ = conn.Close(context.Background())
	fresh, err := pgx.Connect(ctx, g.dbURL)
	if err != nil {
		// 连接断了就是没持锁：Held() 必须如实报 false。保留旧 conn 引用
		// （已关闭），下一轮心跳 Ping 失败会再次走到这里重试重连。
		log.Printf("idempotency: guard reconnect failed: %v", err)
		g.mu.Lock()
		g.held = false
		g.mu.Unlock()
		return
	}
	ok, err := tryLock(ctx, fresh)
	if err != nil || !ok {
		_ = fresh.Close(context.Background())
		log.Printf("idempotency: guard lost single-instance lock (err=%v held=%t); continuing without it", err, ok)
		g.mu.Lock()
		g.conn = nil
		g.held = false
		g.mu.Unlock()
		return
	}
	g.mu.Lock()
	g.conn = fresh
	g.held = true
	g.mu.Unlock()
	log.Printf("idempotency: guard lock reacquired after reconnect")
}

// Held 报告当前是否确实持锁；供状态展示与排障使用。
func (g *Guard) Held() bool {
	if g == nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.held
}

// Close 释放锁并关闭连接。可重复调用。
func (g *Guard) Close() {
	if g == nil {
		return
	}
	g.once.Do(func() {
		close(g.stop)
		<-g.done

		g.mu.Lock()
		conn := g.conn
		g.conn = nil
		g.held = false
		g.mu.Unlock()
		if conn == nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = conn.Exec(ctx, `SELECT pg_advisory_unlock($1)`, instanceLockKey)
		_ = conn.Close(ctx)
	})
}
