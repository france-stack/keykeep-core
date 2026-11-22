// Package virtualkey 校验 KeyKeep 虚拟 Key，并把请求换成内嵌 SDK 的内部 Key。
//
// 虚拟 Key 的明文只在签发时返回一次；这里只做 SHA-256 查询，撤销后立即拒绝。
package virtualkey

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"github.com/keykeep/gateway/internal/httpmw"
)

type KeyInfo struct {
	ID            string
	EmpID         string
	DeptID        *string
	Prefix4       string
	FP6           string
	AllowedModels []string
}

type Store interface {
	Lookup(ctx context.Context, keyHash []byte) (*KeyInfo, error)
}

// Auditor 是审计旁路；nil 表示不写审计。契约定义收敛到 internal/httpmw（C17）。
type Auditor = httpmw.Auditor

type contextKey string

const keyInfoContextKey contextKey = "keykeep.virtualkey"

// FromContext 返回已校验的 Key 信息；未经过中间件时为 false。
func FromContext(ctx context.Context) (*KeyInfo, bool) {
	info, ok := ctx.Value(keyInfoContextKey).(*KeyInfo)
	return info, ok
}

// WithKeyInfo 把已校验的 Key 信息放入上下文；供测试与内部组合使用。
func WithKeyInfo(ctx context.Context, info *KeyInfo) context.Context {
	return context.WithValue(ctx, keyInfoContextKey, info)
}

// IsKeyFormat 判断是否是 KeyKeep 虚拟 Key：kk_ + 43 位 base64url。
func IsKeyFormat(value string) bool {
	if len(value) != 46 || !strings.HasPrefix(value, "kk_") {
		return false
	}
	for _, ch := range value[3:] {
		switch {
		case ch >= 'a' && ch <= 'z':
		case ch >= 'A' && ch <= 'Z':
		case ch >= '0' && ch <= '9':
		case ch == '-' || ch == '_':
		default:
			return false
		}
	}
	return true
}

// recordFailure 记录 Key 校验失败；只写原因与路径，不记录 Key 明文。
func recordFailure(r *http.Request, auditor Auditor, reason string) {
	if auditor == nil {
		return
	}
	auditor.Record(r.Context(), "key.auth_failed", "", map[string]any{
		"reason": reason,
		"path":   r.URL.Path,
	}, r.Header.Get("X-Request-Id"))
}

func writeError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code}})
}

// extractKey 取客户端提交的虚拟 Key。
//
// 契约（dev-spec §1.1）以 Authorization: Bearer 为主；x-api-key 是兼容回退，
// 因为 Anthropic 官方 SDK / Claude Code 默认发 x-api-key，员工照官方文档填
// ANTHROPIC_API_KEY 时不会带 Bearer。x-api-key 允许带 "Bearer " 前缀
// （部分客户端把 ANTHROPIC_AUTH_TOKEN 原样塞进去）。两处都出现时以 Bearer 为准。
func extractKey(r *http.Request) (string, string) {
	if auth := r.Header.Get("Authorization"); auth != "" {
		const prefix = "Bearer "
		if !strings.HasPrefix(auth, prefix) {
			return "", "missing_bearer"
		}
		plaintext := strings.TrimSpace(strings.TrimPrefix(auth, prefix))
		if plaintext == "" {
			return "", "missing_bearer"
		}
		return plaintext, ""
	}
	apiKey := strings.TrimSpace(r.Header.Get("x-api-key"))
	apiKey = strings.TrimSpace(strings.TrimPrefix(apiKey, "Bearer "))
	if apiKey == "" {
		return "", "missing_bearer"
	}
	return apiKey, ""
}

// Middleware 校验虚拟 Key 后，把 Authorization 替换成内部 Key 再交给 SDK。
// /healthz 不校验，保证编排健康检查不依赖数据库。
// 注意：SDK 访问列表的就绪等待放在内链 EnsureAccess（幂等之后）——放在这里
// 会先于 claim 创建阻塞，客户端 1s 超时语义（T-21）就废了。
func Middleware(store Store, keyPrefix string, auditor Auditor, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}

		plaintext, reason := extractKey(r)
		if reason != "" {
			recordFailure(r, auditor, reason)
			writeError(w, http.StatusUnauthorized, "invalid_api_key")
			return
		}
		if !IsKeyFormat(plaintext) {
			recordFailure(r, auditor, "malformed")
			writeError(w, http.StatusUnauthorized, "invalid_api_key")
			return
		}

		sum := sha256.Sum256([]byte(plaintext))
		info, err := store.Lookup(r.Context(), sum[:])
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "key_store_unavailable")
			return
		}
		if info == nil {
			recordFailure(r, auditor, "unknown_or_revoked")
			writeError(w, http.StatusUnauthorized, "invalid_api_key")
			return
		}

		// 先删后写，避免客户端伪造身份头。
		// x-api-key 是客户端提交的虚拟 Key：必须删除，否则会被 SDK 原样转给上游，
		// 等于把员工 Key 泄露给供应商。x-goog-api-key / Proxy-Authorization / Cookie
		// 同理：都是客户端凭据头，数据面不需要，留在请求里只会外泄。
		r.Header.Del("x-api-key")
		r.Header.Del("X-Api-Key")
		r.Header.Del("X-Goog-Api-Key")
		r.Header.Del("Proxy-Authorization")
		r.Header.Del("Cookie")
		r.Header.Del("X-KeyKeep-Key-Id")
		r.Header.Del("X-KeyKeep-Emp-Id")
		r.Header.Del("X-KeyKeep-Dept-Id")
		r.Header.Set("X-KeyKeep-Key-Id", info.ID)
		r.Header.Set("X-KeyKeep-Emp-Id", info.EmpID)
		if info.DeptID != nil {
			r.Header.Set("X-KeyKeep-Dept-Id", *info.DeptID)
		}
		r.Header.Set("Authorization", "Bearer "+keyPrefix+info.ID)

		ctx := context.WithValue(r.Context(), keyInfoContextKey, info)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// EnsureAccess 在转发前确保当前键已进入 SDK 访问列表（必要时触发一次重载并
// 等待就绪）。**必须放在幂等中间件之后**：T-21 的客户端 1s 超时语义要求
// claim 先于任何等待创建（客户端断连 → claim 留在 outcome_unknown 可查）；
// 放在最外层会让等待先于 claim，T-21 全线崩（2026-11-08 实测）。
// 等待超时仍转发：可用性优先，该笔调用可能无法归属账本（记日志）。
// ensure 为 nil 时（无数据库形态）直接放行。
//
// 访问列表的键与转发头同口径 = keyPrefix+UUID，两侧必须一致，否则每请求白等
// maxWait（EnsureKey 的 HasKey 快路径永远打不中，只能等轮询超时）。
func EnsureAccess(keyPrefix string, ensure func(ctx context.Context, keyID string) bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ensure != nil {
			if info, ok := FromContext(r.Context()); ok && !ensure(r.Context(), keyPrefix+info.ID) {
				log.Printf("virtualkey: key %s not in sdk access list after wait; this call may not be attributed", keyPrefix+info.ID)
			}
		}
		next.ServeHTTP(w, r)
	})
}
