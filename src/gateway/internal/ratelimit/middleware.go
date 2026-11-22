package ratelimit

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strconv"
	"sync/atomic"

	"github.com/keykeep/gateway/internal/httpmw"
	"github.com/keykeep/gateway/internal/reqbody"
	"github.com/keykeep/gateway/internal/virtualkey"
)

// debug 打开后在每次检查时打印窗口计数，便于排障；生产默认关闭。
var debug = os.Getenv("KEYKEEP_DEBUG_RATELIMIT") != ""

const maxInspectBytes = 8 << 20

// Auditor 是审计旁路；nil 表示不写审计。契约定义收敛到 internal/httpmw（C17）。
type Auditor = httpmw.Auditor

// Middleware 在虚拟 Key 校验之后、预算预检之前执行。
// GlobalCounterID 是全局限流计数使用的固定 Key 标识（RateWindow.keyId 为 UUID）。
const GlobalCounterID = "00000000-0000-0000-0000-000000000000"

// Middleware 在虚拟 Key 校验之后、预算预检之前执行。
//
// 限额配置读不到时怎么处理，由 KEYKEEP_RATE_FAIL_MODE 决定：
//
//	closed（默认，2026-11-08 起）—— 503 rate_limit_unavailable，请求不放行。
//	open                       —— 按不限流放行，只记日志（旧行为）。
//
// 默认改成 closed 的原因：限流是计费链的上游闸门。放行意味着 PG 抖动期间
// 计费与预算一起旁路，损失是钱；拒绝意味着这批请求拿不到服务，损失是几分钟
// 的可用性。前者不可逆、后者可重试，所以默认选后者。确实需要"限流坏了也要
// 继续转发"的部署可以显式设 open，并把它写进自己的降级说明。
func Middleware(counter Counter, store *Store, auditor Auditor, next http.Handler) http.Handler {
	failClosed := os.Getenv("KEYKEEP_RATE_FAIL_MODE") != "open"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		info, ok := virtualkey.FromContext(r.Context())
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		keyCfg, err := store.Get(r.Context(), info.ID)
		if err != nil {
			logStoreError(err, r.URL.Path, info.ID)
			if failClosed {
				writeUnavailable(w, r, auditor, info, "limits_unavailable", "key")
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		globalCfg, err := store.GetGlobal(r.Context())
		if err != nil {
			log.Printf("ratelimit: load global limits: %v (global tier skipped)", err)
			if failClosed {
				writeUnavailable(w, r, auditor, info, "global_limits_unavailable", "global")
				return
			}
			globalCfg = Config{}
		}
		if keyCfg.RateIsZero() && globalCfg.RateIsZero() {
			next.ServeHTTP(w, r)
			return
		}

		estIn, estOut := estimate(r)
		var releases []func()
		releaseAll := func() {
			for _, release := range releases {
				release()
			}
		}

		// 两级：Key 级与全局级都要通过；全局拒绝时先归还 Key 级估值。
		for _, tier := range []struct {
			scope string
			id    string
			cfg   Config
		}{
			{"key", info.ID, keyCfg},
			{"global", GlobalCounterID, globalCfg},
		} {
			if tier.cfg.RateIsZero() {
				continue
			}
			// Snapshot 只服务 debug 日志：PG 后端下它每级一查，是无条件白付的
			// 两次往返，只在调试开启时才付这个成本。
			var rpmNow, inNow, outNow int64
			if debug {
				rpmNow, inNow, outNow, _ = counter.Snapshot(r.Context(), tier.id)
			}
			res, denial, err := counter.Admit(r.Context(), tier.id, tier.cfg, estIn, estOut)
			if err != nil {
				// 计数后端不可用时同样是「无法判定是否超限」：fail-closed 下拒绝，
				// fail-open 下归还已占的估值后放行。
				log.Printf("ratelimit: counter unavailable (%s): %v", tier.scope, err)
				releaseAll()
				if failClosed {
					writeUnavailable(w, r, auditor, info, "counter_unavailable", tier.scope)
					return
				}
				next.ServeHTTP(w, r)
				return
			}
			if debug {
				log.Printf("ratelimit: scope=%s key=%s cfg=%+v before=%d/%d/%d est=%d/%d denied=%v",
					tier.scope, tier.id, tier.cfg, rpmNow, inNow, outNow, estIn, estOut, denial != nil)
			}
			if denial != nil {
				observeRateLimit(tier.scope, "deny")
				releaseAll()
				log.Printf("ratelimit: deny scope=%s key=%s dim=%s retry=%d remaining=%d",
					tier.scope, info.ID, denial.Dim, denial.RetryAfter, denial.Remaining)
				if auditor != nil {
					auditor.Record(r.Context(), "key.rate_limited", info.EmpID, map[string]any{
						"keyId":      info.ID,
						"scope":      tier.scope,
						"dim":        denial.Dim,
						"retryAfter": denial.RetryAfter,
						"remaining":  denial.Remaining,
						"path":       r.URL.Path,
					}, r.Header.Get("X-Request-Id"))
				}
				w.Header().Set("Retry-After", strconv.FormatInt(denial.RetryAfter, 10))
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.Header().Set("Cache-Control", "no-store")
				w.WriteHeader(http.StatusTooManyRequests)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{
					"code":       "rate_limited",
					"scope":      tier.scope,
					"dim":        denial.Dim,
					"retryAfter": denial.RetryAfter,
					"remaining":  denial.Remaining,
				}})
				return
			}
			observeRateLimit(tier.scope, "allow")
			releases = append(releases, res.Release)
		}
		// 估值在请求结束时归还；实际用量由账本回调 AddActual 补记。
		defer releaseAll()
		next.ServeHTTP(w, r)
	})
}

// writeUnavailable 是 fail-closed 的出口：503 + Retry-After，并留下审计。
//
// 用 503 而不是 429：429 表示"你超限了"，会诱导客户端退避重试；503 表示
// "本服务暂时无法判定"，配合 Retry-After 让调用方按容量问题处理。
func writeUnavailable(w http.ResponseWriter, r *http.Request, auditor Auditor, info *virtualkey.KeyInfo, reason, tier string) {
	failClosedCount.Add(1)
	observeRateLimit(tier, "unavailable")
	if auditor != nil && info != nil {
		auditor.Record(r.Context(), "key.rate_limit_unavailable", info.EmpID, map[string]any{
			"keyId":  info.ID,
			"reason": reason,
			"path":   r.URL.Path,
		}, r.Header.Get("X-Request-Id"))
	}
	w.Header().Set("Retry-After", "1")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusServiceUnavailable)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{
		"code":   "rate_limit_unavailable",
		"reason": reason,
	}})
}

// estimate 用请求体字节数估算输入 token，输出按 max_tokens（缺省 1024）估算。
// 与预算中间件的估算口径一致：1 token ≈ 3 字节。
func estimate(r *http.Request) (int64, int64) {
	const defaultOutput = 1024
	if r.Body == nil || (r.Method != http.MethodPost && r.Method != http.MethodPut && r.Method != http.MethodPatch) {
		return 0, 0
	}
	if r.ContentLength > maxInspectBytes {
		return r.ContentLength / 3, int64(defaultOutput)
	}
	hint := reqbody.BodyHint(r)
	if hint.TooLarge {
		return 0, 0
	}
	estIn := hint.Size / 3
	estOut := int64(defaultOutput)
	if hint.MaxTokens > 0 {
		estOut = hint.MaxTokens
	}
	if hint.MaxCompletion > 0 {
		estOut = hint.MaxCompletion
	}
	return estIn, estOut
}

// ------------------------------------------------------------------ 指标

// 进程级 fail-closed 计数。放在包级而不是每请求传参：中间件签名已经很长，
// 而同一个进程只有一份限流中间件，包级计数就是它的计数。
var failClosedCount atomic.Uint64

// FailClosedCount 返回自启动以来按 fail-closed 拒绝的请求数（供 /metrics 读取）。
func FailClosedCount() uint64 { return failClosedCount.Load() }

// metricsSink 是可选的指标出口；nil 时不统计。
var metricsSink atomic.Value

// SetMetrics 注册指标出口。参数用最小接口而不是具体类型，避免 ratelimit
// 依赖 metrics 包（依赖方向必须保持单向：metrics 只读计数）。
func SetMetrics(sink interface {
	ObserveRateLimit(tier, outcome string)
}) {
	if sink == nil {
		return
	}
	metricsSink.Store(sink)
}

func observeRateLimit(tier, outcome string) {
	if sink, ok := metricsSink.Load().(interface{ ObserveRateLimit(tier, outcome string) }); ok && sink != nil {
		sink.ObserveRateLimit(tier, outcome)
	}
}
