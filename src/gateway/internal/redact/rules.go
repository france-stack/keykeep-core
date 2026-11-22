package redact

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/keykeep/gateway/internal/dbpool"
)

// Loader 从控制面读取规则；DB 不可用时调用方保留内置规则。
type Loader struct {
	pool *pgxpool.Pool
}

func NewLoader(ctx context.Context, databaseURL string) (*Loader, error) {
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
	return &Loader{pool: pool}, nil
}

func (l *Loader) Close() { l.pool.Close() }

// Load 读取启用规则。调用方应与内置规则合并。
func (l *Loader) Load(ctx context.Context) ([]Rule, error) {
	// 读全部规则：同名 enabled=false 用于覆盖/停用内置规则。
	rows, err := l.pool.Query(ctx,
		`SELECT name, pattern, kind, target, replacement, priority, enabled
		   FROM "RedactRule"
		  ORDER BY priority, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Rule
	for rows.Next() {
		var rule Rule
		if err := rows.Scan(&rule.Name, &rule.Pattern, &rule.Kind, &rule.Target,
			&rule.Replacement, &rule.Priority, &rule.Enabled); err != nil {
			return nil, err
		}
		out = append(out, rule)
	}
	return out, rows.Err()
}

// Merge 合并内置规则与 DB 规则；同名时 DB 覆盖内置。
func Merge(defaults, overrides []Rule) []Rule {
	byName := make(map[string]Rule, len(defaults)+len(overrides))
	for _, rule := range defaults {
		byName[rule.Name] = rule
	}
	for _, rule := range overrides {
		byName[rule.Name] = rule
	}
	out := make([]Rule, 0, len(byName))
	for _, rule := range byName {
		out = append(out, rule)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Priority < out[j].Priority })
	return out
}

// Fingerprint 用于轮询比对；只含规则字段，不含时间。
func Fingerprint(rules []Rule) string {
	parts := make([]string, 0, len(rules))
	for _, rule := range rules {
		parts = append(parts, strings.Join([]string{
			rule.Name, rule.Pattern, rule.Kind, rule.Target, rule.Replacement,
			fmt.Sprintf("%d", rule.Priority), fmt.Sprintf("%t", rule.Enabled),
		}, "\x00"))
	}
	sort.Strings(parts)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x01")))
	return hex.EncodeToString(sum[:])
}

// Holder 原子保存当前 Engine，供请求并发读取。
type Holder struct {
	value atomic.Pointer[Engine]
}

func NewHolder(engine *Engine) *Holder {
	h := &Holder{}
	h.value.Store(engine)
	return h
}

func (h *Holder) Get() *Engine { return h.value.Load() }

func (h *Holder) Set(engine *Engine) { h.value.Store(engine) }

// Watch 轮询 DB 规则；失败保留旧规则。首次加载由调用方完成，避免启动竞态。
func Watch(ctx context.Context, loader *Loader, holder *Holder, interval time.Duration) {
	if interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var last string
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			loadCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			rules, err := loader.Load(loadCtx)
			cancel()
			if err != nil {
				log.Printf("redact: reload rules: %v (keeping previous)", err)
				continue
			}
			merged := Merge(DefaultRules(), rules)
			fp := Fingerprint(merged)
			if fp == last {
				continue
			}
			holder.Set(NewEngine(merged))
			last = fp
			log.Printf("redact: reloaded %d rules", len(merged))
		}
	}
}
