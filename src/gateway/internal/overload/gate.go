// Package overload 在网关边缘做并发保护与有界排队（FR-22 全局级 / FR-25）。
//
// 语义：全局并发槽 + 有界等待队列；快速路径不等待，队列满或等待超时返回 503。
// 单 Key 并发由 Keyed 提供（非阻塞，超限即拒），对应 Limit 表的 dim='concurrency'。
package overload

import (
	"context"
	"errors"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

var (
	ErrQueueFull    = errors.New("overload: queue full")
	ErrQueueTimeout = errors.New("overload: queue timeout")
	ErrClosed       = errors.New("overload: closed")
)

// Config 是网关边缘过载保护参数；MaxConcurrency=0 表示关闭该级。
type Config struct {
	MaxConcurrency int
	QueueSize      int
	QueueTimeout   time.Duration
}

// FromEnv 读取环境变量；非法值回退默认并在调用方日志中体现。
func FromEnv() Config {
	return Config{
		MaxConcurrency: envInt("KEYKEEP_MAX_CONCURRENCY", 200),
		QueueSize:      envInt("KEYKEEP_QUEUE_SIZE", 100),
		QueueTimeout:   time.Duration(envInt("KEYKEEP_QUEUE_TIMEOUT_MS", 10000)) * time.Millisecond,
	}
}

func envInt(key string, fallback int) int {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return fallback
	}
	return value
}

// Stats 是过载保护的运行计数，供日志与排障使用。
type Stats struct {
	Inflight     int
	Queued       int
	Admitted     uint64
	QueueFull    uint64
	QueueTimeout uint64
	WaitTotalMs  int64
	WaitMaxMs    int64
}

// Gate 是全局并发槽 + 有界队列。
type Gate struct {
	slots    chan struct{}
	max      int
	queue    int
	timeout  time.Duration
	done     chan struct{}
	closeOne sync.Once

	mu       sync.Mutex
	waiting  int
	admitted atomic.Uint64
	full     atomic.Uint64
	timeouts atomic.Uint64
	waitMs   atomic.Int64
	waitMax  atomic.Int64
}

// New 构造 Gate；MaxConcurrency=0 时 Acquire 直接放行。
func New(cfg Config) *Gate {
	g := &Gate{
		max:     cfg.MaxConcurrency,
		queue:   cfg.QueueSize,
		timeout: cfg.QueueTimeout,
		done:    make(chan struct{}),
	}
	if g.max > 0 {
		g.slots = make(chan struct{}, g.max)
	}
	if g.timeout <= 0 {
		g.timeout = 10 * time.Second
	}
	return g
}

// Acquire 获取一个全局并发槽；返回的 release 可重复调用。
func (g *Gate) Acquire(ctx context.Context) (release func(), waited time.Duration, err error) {
	if g == nil || g.max == 0 {
		return func() {}, 0, nil
	}
	start := time.Now()
	select {
	case g.slots <- struct{}{}:
		g.admitted.Add(1)
		return g.releaser(), 0, nil
	default:
	}

	// 队列满立即拒绝，不进入等待。
	g.mu.Lock()
	if g.isClosed() {
		g.mu.Unlock()
		return nil, 0, ErrClosed
	}
	if g.waiting >= g.queue {
		g.mu.Unlock()
		g.full.Add(1)
		return nil, 0, ErrQueueFull
	}
	g.waiting++
	g.mu.Unlock()

	defer func() {
		g.mu.Lock()
		g.waiting--
		g.mu.Unlock()
	}()

	timer := time.NewTimer(g.timeout)
	defer timer.Stop()
	select {
	case g.slots <- struct{}{}:
		waited = time.Since(start)
		g.admitted.Add(1)
		g.waitMs.Add(waited.Milliseconds())
		for {
			prev := g.waitMax.Load()
			if waited.Milliseconds() <= prev || g.waitMax.CompareAndSwap(prev, waited.Milliseconds()) {
				break
			}
		}
		return g.releaser(), waited, nil
	case <-timer.C:
		g.timeouts.Add(1)
		return nil, 0, ErrQueueTimeout
	case <-ctx.Done():
		return nil, 0, ctx.Err()
	case <-g.done:
		return nil, 0, ErrClosed
	}
}

func (g *Gate) releaser() func() {
	var once sync.Once
	return func() {
		once.Do(func() { <-g.slots })
	}
}

// Close 唤醒所有等待者；已在执行的请求按各自 release 正常归还。
func (g *Gate) Close() {
	if g == nil {
		return
	}
	g.closeOne.Do(func() { close(g.done) })
}

func (g *Gate) isClosed() bool {
	select {
	case <-g.done:
		return true
	default:
		return false
	}
}

// Snapshot 返回当前占用与累计拒绝情况。
func (g *Gate) Snapshot() Stats {
	if g == nil {
		return Stats{}
	}
	g.mu.Lock()
	waiting := g.waiting
	g.mu.Unlock()
	inflight := 0
	if g.slots != nil {
		inflight = len(g.slots)
	}
	return Stats{
		Inflight:     inflight,
		Queued:       waiting,
		Admitted:     g.admitted.Load(),
		QueueFull:    g.full.Load(),
		QueueTimeout: g.timeouts.Load(),
		WaitTotalMs:  g.waitMs.Load(),
		WaitMaxMs:    g.waitMax.Load(),
	}
}

// Keyed 是按 Key 的并发上限；非阻塞——超限立即拒绝，不进入队列。
type Keyed struct {
	mu sync.Mutex
	m  map[string]*keySem
}

type keySem struct {
	slots chan struct{}
	inUse int
}

func NewKeyed() *Keyed {
	return &Keyed{m: make(map[string]*keySem)}
}

// Acquire 尝试占用 Key 的一个并发额度；limit<=0 直接放行。
// 返回的 release 可重复调用；Key 空闲后从表中移除，避免长跑进程内存增长。
func (k *Keyed) Acquire(keyID string, limit int64) (func(), bool) {
	if k == nil || limit <= 0 || keyID == "" {
		return func() {}, true
	}
	k.mu.Lock()
	sem := k.m[keyID]
	if sem == nil || cap(sem.slots) != int(limit) {
		if sem != nil && sem.inUse > 0 {
			// 限额正在变化：旧槽未释放前沿用旧容量，避免丢计数。
			limit = int64(cap(sem.slots))
		} else {
			sem = &keySem{slots: make(chan struct{}, int(limit))}
			k.m[keyID] = sem
		}
	}
	select {
	case sem.slots <- struct{}{}:
		sem.inUse++
		k.mu.Unlock()
		var once sync.Once
		return func() {
			once.Do(func() {
				<-sem.slots
				k.mu.Lock()
				sem.inUse--
				// 防御性守卫：摘除前确认 map 里挂的仍是本信号量。
				// 当前 inUse 递减与摘除同持 k.mu，替换只可能发生在 inUse==0（此时
				// 旧信号量已被摘除），误删不可触发；守卫保证后续改动破坏该不变式时也不出错。
				if sem.inUse == 0 {
					if cur := k.m[keyID]; cur == sem {
						delete(k.m, keyID)
					}
				}
				k.mu.Unlock()
			})
		}, true
	default:
		k.mu.Unlock()
		return nil, false
	}
}
