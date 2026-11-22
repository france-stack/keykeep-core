package ratelimit

import (
	"context"
	"sync"
	"time"
)

type bucket struct {
	stamp int64
	value int64
}

// counters 是单把 Key 的 60 个秒级桶；桶下标 = Unix 秒 % 60，
// 下标上的 stamp 与当前秒不符即视为过期，读取时重置。
type counters struct {
	rpm  [WindowSeconds]bucket
	itpm [WindowSeconds]bucket
	otpm [WindowSeconds]bucket
}

// Limiter 是单进程内存计数实现（Counter 接口）。
// 按 keyID 分片加锁：不同 Key 的热路径互不阻塞（此前全局单锁让所有 Key
// 的 Admit/release/AddActual 串行化）。
type Limiter struct {
	now    func() time.Time
	shards [limiterShards]limiterShard
}

const limiterShards = 32

type limiterShard struct {
	mu        sync.Mutex
	counters  map[string]*counters
	lastSeen  map[string]int64
	lastSweep int64
}

func NewLimiter() *Limiter {
	l := &Limiter{now: time.Now}
	for i := range l.shards {
		l.shards[i].counters = make(map[string]*counters)
		l.shards[i].lastSeen = make(map[string]int64)
	}
	return l
}

// shardFor 用内联 FNV-1a 选分片，避免热路径上的哈希对象分配。
func (l *Limiter) shardFor(keyID string) *limiterShard {
	var h uint64 = 14695981039346656037
	for i := 0; i < len(keyID); i++ {
		h ^= uint64(keyID[i])
		h *= 1099511628211
	}
	return &l.shards[h%limiterShards]
}

func (l *Limiter) Close() {}

// Denial 描述一次被拒绝的维度与建议重试秒数。
//
// RetryAfter 的统一口径（内存与 PG 实现一致）：相关维度中最早一条计数
// 过期所需的秒数，下限 1。它是保守下界——届时窗口腾出空间，但不保证
// 足够本次估值（并发流量不可预知），客户端应把 429 视为「稍后重试」而非精确预约。
type Denial struct {
	Dim        string
	RetryAfter int64
	Remaining  int64
}

type entry struct {
	dim    byte // 'r' rpm, 'i' itpm, 'o' otpm
	index  int
	stamp  int64
	amount int64
}

// Admit 检查并占用 estIn / estOut 的额度。
// 任一维度超限即返回 Denial，且不占用任何计数；放行时返回 Reservation。
func (l *Limiter) Admit(_ context.Context, keyID string, cfg Config, estIn, estOut int64) (*Reservation, *Denial, error) {
	sh := l.shardFor(keyID)
	sh.mu.Lock()
	defer sh.mu.Unlock()

	now := l.now().Unix()
	sh.sweepLocked(now)
	c := sh.counters[keyID]
	if c == nil {
		c = &counters{}
		sh.counters[keyID] = c
	}
	sh.lastSeen[keyID] = now

	inSum := sum(&c.itpm, now)
	outSum := sum(&c.otpm, now)
	rpmSum := sum(&c.rpm, now)

	if cfg.RPM > 0 && rpmSum+1 > cfg.RPM {
		return nil, &Denial{Dim: "rpm", RetryAfter: retryAfter(&c.rpm, now), Remaining: max(cfg.RPM-rpmSum, 0)}, nil
	}
	if cfg.TPM > 0 && inSum+outSum+estIn+estOut > cfg.TPM {
		// 两个 token 桶都计入 TPM：取最早腾出空间的那一刻（有量的桶里最小的等待）。
		return nil, &Denial{
			Dim:        "tpm",
			RetryAfter: minPositiveWait(retryAfter(&c.itpm, now), inSum > 0, retryAfter(&c.otpm, now), outSum > 0),
			Remaining:  max(cfg.TPM-inSum-outSum, 0),
		}, nil
	}
	if cfg.ITPM > 0 && inSum+estIn > cfg.ITPM {
		return nil, &Denial{Dim: "itpm", RetryAfter: retryAfter(&c.itpm, now), Remaining: max(cfg.ITPM-inSum, 0)}, nil
	}
	if cfg.OTPM > 0 && outSum+estOut > cfg.OTPM {
		return nil, &Denial{Dim: "otpm", RetryAfter: retryAfter(&c.otpm, now), Remaining: max(cfg.OTPM-outSum, 0)}, nil
	}

	adds := make([]entry, 0, 3)
	adds = append(adds, l.add(c, 'r', now, 1))
	if estIn > 0 {
		adds = append(adds, l.add(c, 'i', now, estIn))
	}
	if estOut > 0 {
		adds = append(adds, l.add(c, 'o', now, estOut))
	}
	return newReservation(func() { l.release(sh, keyID, adds) }), nil, nil
}

func (l *Limiter) release(sh *limiterShard, keyID string, adds []entry) {
	if len(adds) == 0 {
		return
	}
	sh.mu.Lock()
	defer sh.mu.Unlock()
	c := sh.counters[keyID]
	if c == nil {
		return
	}
	for _, e := range adds {
		// rpm 记录的是"窗口内发生过请求"，请求结束后不归还；
		// 只有 token 估值需要归还，实际用量随后由 AddActual 补记。
		if e.dim == 'r' {
			continue
		}
		arr := pick(c, e.dim)
		b := &arr[e.index]
		if b.stamp == e.stamp {
			b.value -= e.amount
			if b.value < 0 {
				b.value = 0
			}
		}
	}
}

// AddActual 在用量回调到达后用实际 token 替换估值（估值已由 Release 归还）。
func (l *Limiter) AddActual(_ context.Context, keyID string, inputTokens, outputTokens int64) error {
	if keyID == "" || (inputTokens <= 0 && outputTokens <= 0) {
		return nil
	}
	sh := l.shardFor(keyID)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	now := l.now().Unix()
	c := sh.counters[keyID]
	if c == nil {
		c = &counters{}
		sh.counters[keyID] = c
	}
	sh.lastSeen[keyID] = now
	if inputTokens > 0 {
		l.add(c, 'i', now, inputTokens)
	}
	if outputTokens > 0 {
		l.add(c, 'o', now, outputTokens)
	}
	return nil
}

// Snapshot 返回当前窗口内的已用计数，供排障与验收取证（S-4）。
func (l *Limiter) Snapshot(_ context.Context, keyID string) (rpm, inTokens, outTokens int64, err error) {
	sh := l.shardFor(keyID)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	c := sh.counters[keyID]
	if c == nil {
		return 0, 0, 0, nil
	}
	now := l.now().Unix()
	return sum(&c.rpm, now), sum(&c.itpm, now), sum(&c.otpm, now), nil
}

// sweepLocked 每分钟清理超过两个窗口没有活动的 Key，避免长跑进程内存单调增长。
func (sh *limiterShard) sweepLocked(now int64) {
	if sh.lastSweep != 0 && now-sh.lastSweep < WindowSeconds {
		return
	}
	sh.lastSweep = now
	for keyID, seen := range sh.lastSeen {
		if now-seen > 2*WindowSeconds {
			delete(sh.counters, keyID)
			delete(sh.lastSeen, keyID)
		}
	}
}

func (l *Limiter) add(c *counters, dim byte, now, amount int64) entry {
	arr := pick(c, dim)
	index := int(now % WindowSeconds)
	b := &arr[index]
	if b.stamp != now {
		b.stamp = now
		b.value = 0
	}
	b.value += amount
	return entry{dim: dim, index: index, stamp: now, amount: amount}
}

func pick(c *counters, dim byte) *[WindowSeconds]bucket {
	switch dim {
	case 'r':
		return &c.rpm
	case 'i':
		return &c.itpm
	default:
		return &c.otpm
	}
}

func sum(arr *[WindowSeconds]bucket, now int64) int64 {
	var total int64
	for i := range arr {
		if now-arr[i].stamp < WindowSeconds {
			total += arr[i].value
		}
	}
	return total
}

// retryAfter 返回窗口内最早一条计数过期所需的秒数。
func retryAfter(arr *[WindowSeconds]bucket, now int64) int64 {
	oldest := now
	for i := range arr {
		b := arr[i]
		if b.value > 0 && b.stamp < oldest {
			oldest = b.stamp
		}
	}
	wait := oldest + WindowSeconds - now
	if wait < 1 {
		wait = 1
	}
	return wait
}

// minPositiveWait 在"有计数"的桶中取较小等待；都为空时返回 1。
func minPositiveWait(waitIn int64, hasIn bool, waitOut int64, hasOut bool) int64 {
	switch {
	case hasIn && hasOut:
		if waitIn < waitOut {
			return waitIn
		}
		return waitOut
	case hasIn:
		return waitIn
	case hasOut:
		return waitOut
	default:
		return 1
	}
}

func max(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
