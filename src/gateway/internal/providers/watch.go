package providers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// Watch 轮询控制面数据；供应商、模型映射或 Key 列表变化时重写 SDK 配置文件，
// 由内嵌 SDK 的 fsnotify watcher 触发热重载。
//
// 这是 FR-10 / FA-10「停用凭据后不参与路由」的落地方式：控制面只改库，
// 网关按指纹发现变化并重写配置，无需重启进程。interval<=0 或 configPath 为空时不启动。
func Watch(ctx context.Context, databaseURL, kekRaw string, base *config.Config, basePath, keyPrefix, configPath string, overrides Overrides, interval time.Duration, skip func(breakerID string) bool) {
	if interval <= 0 || configPath == "" {
		return
	}
	relay := NewRelay(func(ctx context.Context) ([]string, error) {
		cfg, raw, err := Load(ctx, databaseURL, kekRaw, base, basePath, keyPrefix, overrides, skip)
		if err != nil {
			return nil, err
		}
		if err := WriteConfig(configPath, raw); err != nil {
			return nil, err
		}
		return cfg.APIKeys, nil
	}, interval)
	relay.Run(ctx)
}

// Relay 是 Watch 的可编程形态：轮询之外支持主动触发重载（kick），并把当前
// SDK 访问键列表暴露给虚拟键中间件。
//
// 为什么需要它：键签发 → 下一个轮询 tick 重载之间存在最长 interval 的窗口，
// 窗口内 SDK 的 access 列表还没有这个键，请求虽然能被外层中间件校验，
// 但 SDK 回调带不出内部键，账本只能静默丢账（2026-11-08 E2E 复跑实测丢 5 笔）。
// 中间件对「访问列表里还没有的键」调用 EnsureKey 触发立即重载并等待就绪，
// 把丢账窗口收敛为一次新键首次调用的短暂等待。
type Relay struct {
	reload   func(ctx context.Context) ([]string, error)
	interval time.Duration
	kick     chan struct{}

	mu     sync.RWMutex
	access map[string]struct{}
}

// NewRelay 以 reload 函数构建 Relay；reload 负责生成并落盘 SDK 配置，返回
// 本次配置里的访问键列表。interval<=0 时 Run 退化为只响应 kick。
func NewRelay(reload func(ctx context.Context) ([]string, error), interval time.Duration) *Relay {
	return &Relay{
		reload:   reload,
		interval: interval,
		kick:     make(chan struct{}, 1),
		access:   map[string]struct{}{},
	}
}

// SeedAccessKeys 用启动时已构建的访问键列表播种快照，避免首个请求触发
// 一次多余的重载（启动 Load 的结果调用方手上就有）。
func (r *Relay) SeedAccessKeys(ids []string) {
	access := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		access[id] = struct{}{}
	}
	r.mu.Lock()
	r.access = access
	r.mu.Unlock()
}

// Run 阻塞运行轮询循环；ctx 取消即返回。interval<=0 时只响应 kick。
func (r *Relay) Run(ctx context.Context) {
	var resetTick func()
	var tickerC <-chan time.Time
	if r.interval > 0 {
		ticker := time.NewTicker(r.interval)
		defer ticker.Stop()
		tickerC = ticker.C
		resetTick = func() { ticker.Reset(r.interval) }
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-tickerC:
		case <-r.kick:
			if resetTick != nil {
				// kick 后重置轮询节拍，避免 kick 与 tick 背靠背触发两次重载。
				resetTick()
			}
		}
		r.reloadOnce(ctx)
	}
}

func (r *Relay) reloadOnce(ctx context.Context) {
	ids, err := r.reload(ctx)
	if err != nil {
		// 读取或解密失败时保留旧配置，宁可少一次热更新也不把转发打挂。
		log.Printf("provider watch: reload failed, keeping previous config: %v", err)
		return
	}
	access := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		access[id] = struct{}{}
	}
	r.mu.Lock()
	r.access = access
	r.mu.Unlock()
}

// HasKey 报告当前 SDK 访问列表是否包含该键 ID。
func (r *Relay) HasKey(id string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.access[id]
	return ok
}

// EnsureKey 确保 SDK 访问列表包含键 ID：不在列表则触发立即重载并等待就绪。
// 返回 true 表示键已进入访问列表（含 SDK 150ms 配置防抖的宽限）；false 表示
// 等待超时——调用方应放行请求并接受该笔调用可能无法归属账本，绝不阻塞转发。
func (r *Relay) EnsureKey(ctx context.Context, id string, maxWait time.Duration) bool {
	if r.HasKey(id) {
		return true
	}
	select {
	case r.kick <- struct{}{}:
	default:
	}
	deadline := time.Now().Add(maxWait)
	for {
		if r.HasKey(id) {
			// 快照更新 ≠ SDK 已应用：文件 watcher 有 150ms 防抖，宽限后再放行。
			time.Sleep(250 * time.Millisecond)
			return true
		}
		if ctx.Err() != nil {
			return false
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// WriteConfig 就地重写被 SDK watcher 监听的配置文件。
// 写的是同一个 inode，fsnotify 的 Write 事件才能持续生效；内容很小，
// SDK 侧 150ms 防抖后读取时写入已完成。
func WriteConfig(path string, raw []byte) error {
	return os.WriteFile(path, raw, 0o600)
}

// Fingerprint 对「参与路由的数据」求指纹：启停状态、凭据密文、模型映射、可用虚拟 Key。
// 不包含 updatedAt 等噪声字段，避免无意义重载。
func Fingerprint(ctx context.Context, pool *pgxpool.Pool) (string, error) {
	h := sha256.New()

	rows, err := pool.Query(ctx,
		`SELECT c.id::text, c.name, c."providerType", c."baseUrl", c."authHeaderName",
		        c."authScheme", c."extraHeaders"::text, encode(c.ct, 'hex'), encode(c.nonce, 'hex'),
		        c.disabled::text,
		        COALESCE(m.alias, ''), COALESCE(m."modelId", ''), COALESCE(m."credentialId"::text, '')
		   FROM "ProviderCredential" c
		   LEFT JOIN "ModelMap" m ON m."credentialId" = c.id
		  ORDER BY c.id, m.alias, m."modelId"`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id, name, ptype, baseURL, header, scheme string
			headers, ct, nonce, disabled             string
			alias, modelID, credentialID             string
		)
		if err := rows.Scan(&id, &name, &ptype, &baseURL, &header, &scheme,
			&headers, &ct, &nonce, &disabled, &alias, &modelID, &credentialID); err != nil {
			return "", err
		}
		_, _ = fmt.Fprintf(h, "p|%s|%s|%s|%s|%s|%s|%s|%s|%s|%s|%s|%s|%s\n",
			id, name, ptype, baseURL, header, scheme, headers, ct, nonce, disabled, alias, modelID, credentialID)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}

	keyRows, err := pool.Query(ctx,
		`SELECT k.id::text, (k."revokedAt" IS NULL)::text, u.disabled::text
		   FROM "ApiKey" k JOIN "User" u ON u.id = k."empId"
		  ORDER BY k.id`)
	if err != nil {
		return "", err
	}
	defer keyRows.Close()
	for keyRows.Next() {
		var id, active, userDisabled string
		if err := keyRows.Scan(&id, &active, &userDisabled); err != nil {
			return "", err
		}
		_, _ = fmt.Fprintf(h, "k|%s|%s|%s\n", id, active, userDisabled)
	}
	if err := keyRows.Err(); err != nil {
		return "", err
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}
