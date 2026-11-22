// Package dbpool 是全部组件创建 PG 连接池的唯一入口。
//
// 此前每个组件各自 pgxpool.New，默认 MaxConns = max(4, NumCPU)：
// 一台 16 核机器上 8 个组件就是 128+ 潜在连接，把 PG 的 max_connections
// 和内存都当成免费资源。这里统一封顶并给出生存期参数；
// 总连接上限 = 组件数 × cap，16 核机器上从 128+ 收敛到 32。
package dbpool

import (
	"context"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// defaultMaxConnsCap 是单池连接数上限；网关 8 个组件 × 4 = 32，
// 留足余量给 PG 默认 max_connections=100。
const defaultMaxConnsCap = 4

// MaxConnsCap 返回单池上限；KEYKEEP_PG_POOL_MAX_CONNS 可覆盖（1..64）。
func MaxConnsCap() int32 {
	raw := os.Getenv("KEYKEEP_PG_POOL_MAX_CONNS")
	if raw == "" {
		return defaultMaxConnsCap
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < 1 {
		return defaultMaxConnsCap
	}
	if v > 64 {
		return 64
	}
	return int32(v)
}

// Open 建立带显式资源上限的连接池。解析失败或 ping 不通返回错误。
func Open(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, err
	}
	if cfg.MaxConns > MaxConnsCap() {
		cfg.MaxConns = MaxConnsCap()
	}
	// 明确的生存期参数：连接复用是热路径性能的一部分。
	if cfg.MaxConnLifetime <= 0 {
		cfg.MaxConnLifetime = 30 * time.Minute
	}
	if cfg.MaxConnIdleTime <= 0 {
		cfg.MaxConnIdleTime = 5 * time.Minute
	}
	if cfg.HealthCheckPeriod <= 0 {
		cfg.HealthCheckPeriod = time.Minute
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return pool, nil
}
