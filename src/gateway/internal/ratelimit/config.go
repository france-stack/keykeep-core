// Package ratelimit 实现按虚拟 Key 的 RPM / TPM 双维度限流（FR-23 / FR-24）。
//
// 计数窗口为 60 秒滑动窗口，计数在进程内存中维护：单实例部署足够，
// 多实例需改为 Redis 或 PG 原子计数（见 docs/implementation/06-known-limits.md）。
package ratelimit

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/keykeep/gateway/internal/dbpool"
)

// WindowSeconds 是 RPM 与 TPM 的统计窗口，与验收用例「1 分钟内」一致。
const WindowSeconds = 60

// errNilPool 表示限额存储没有可用连接池（初始化失败，或测试注入的空实现）。
var errNilPool = errors.New("ratelimit: 限额存储连接池不可用")

// configTTL 是限额配置的缓存时长；控制面改限额后最迟 2 秒生效。
const configTTL = 2 * time.Second

// Config 是单把 Key 的限额；0 表示该维度不限制。
type Config struct {
	RPM         int64
	TPM         int64
	ITPM        int64
	OTPM        int64
	Concurrency int64 // 同时进行的请求数上限（FR-22 的第二级）
}

func (c Config) IsZero() bool {
	return c.RPM == 0 && c.TPM == 0 && c.ITPM == 0 && c.OTPM == 0 && c.Concurrency == 0
}

// RateIsZero 表示四个窗口维度都不限制（并发维度由 overload 中间件单独处理）。
func (c Config) RateIsZero() bool {
	return c.RPM == 0 && c.TPM == 0 && c.ITPM == 0 && c.OTPM == 0
}

type cached struct {
	cfg     Config
	expires time.Time
}

// Store 读取控制面 Limit 表中 scope='key' 的限额行，带短缓存。
type Store struct {
	pool  *pgxpool.Pool
	mu    sync.Mutex
	cache map[string]cached
	now   func() time.Time
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
	return &Store{pool: pool, cache: make(map[string]cached), now: time.Now}, nil
}

func (s *Store) Close() {
	if s.pool != nil {
		s.pool.Close()
	}
}

// NewStoreWithPool 供测试注入一个不可用的连接池，让「限额读不到」这条分支可测。
// 生产路径只用 NewStore。
func NewStoreWithPool(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool, cache: make(map[string]cached), now: time.Now}
}

// Get 返回 Key 的限额；数据库失败时沿用未过期的缓存，否则返回空限额并报错。
func (s *Store) Get(ctx context.Context, keyID string) (Config, error) {
	return s.load(ctx, "key:"+keyID, "key", keyID)
}

// GetGlobal 返回全局限额（scope='global', scopeId='global'）；与 Key 级分别缓存。
func (s *Store) GetGlobal(ctx context.Context) (Config, error) {
	return s.load(ctx, "global", "global", "global")
}

func (s *Store) load(ctx context.Context, cacheKey, scope, scopeID string) (Config, error) {
	now := s.now()
	s.mu.Lock()
	if hit, ok := s.cache[cacheKey]; ok && now.Before(hit.expires) {
		s.mu.Unlock()
		return hit.cfg, nil
	}
	s.mu.Unlock()

	if s.pool == nil {
		// 池缺失等价于后端不可用：有未过期缓存就沿用，否则报错由调用方决定放行还是拒绝。
		if hit, ok := s.cache[cacheKey]; ok {
			return hit.cfg, nil
		}
		return Config{}, errNilPool
	}
	rows, err := s.pool.Query(ctx,
		`SELECT dim, "limitValue" FROM "Limit"
		  WHERE scope = $1 AND "scopeId" = $2 AND "limitValue" > 0`, scope, scopeID)
	if err != nil {
		s.mu.Lock()
		hit, ok := s.cache[cacheKey]
		s.mu.Unlock()
		if ok {
			return hit.cfg, nil
		}
		return Config{}, err
	}
	defer rows.Close()

	var cfg Config
	for rows.Next() {
		var (
			dim   string
			value int64
		)
		if err := rows.Scan(&dim, &value); err != nil {
			return Config{}, err
		}
		switch dim {
		case "rpm":
			cfg.RPM = value
		case "tpm":
			cfg.TPM = value
		case "itpm":
			cfg.ITPM = value
		case "otpm":
			cfg.OTPM = value
		case "concurrency":
			cfg.Concurrency = value
		}
	}
	if err := rows.Err(); err != nil && err != pgx.ErrNoRows {
		return Config{}, err
	}

	s.mu.Lock()
	s.cache[cacheKey] = cached{cfg: cfg, expires: now.Add(configTTL)}
	s.mu.Unlock()
	return cfg, nil
}

// Invalidate 让指定 Key 的缓存立即失效；控制面改限额后可调用（当前依赖 TTL）。
func (s *Store) Invalidate(keyID string) {
	s.mu.Lock()
	delete(s.cache, "key:"+keyID)
	s.mu.Unlock()
}

// logStoreError 只描述事实：放不放行由 middleware 按 KEYKEEP_RATE_FAIL_MODE 决定。
// 以前这句话写死了"按不限流放行"，在 fail-closed 之后会误导排障。
func logStoreError(err error, path, keyID string) {
	log.Printf("ratelimit: load key limits failed (path=%s key=%s): %v", path, keyID, err)
}
