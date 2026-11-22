package overload

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/keykeep/gateway/internal/httpmw"
	"github.com/keykeep/gateway/internal/ratelimit"
	"github.com/keykeep/gateway/internal/virtualkey"
)

// Auditor 是审计旁路；nil 表示不写审计。契约定义收敛到 internal/httpmw（C17）。
type Auditor = httpmw.Auditor

// LimitStore 读取单 Key 的限额（其中 Concurrency 是第二级并发上限）。
type LimitStore interface {
	Get(ctx context.Context, keyID string) (ratelimit.Config, error)
}

// Middleware 在限流之后、预算之前执行：先挡住过载请求，避免无谓的预算预扣。
func Middleware(gate *Gate, keyed *Keyed, store LimitStore, auditor Auditor, next http.Handler) http.Handler {
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

		keyLimit := int64(0)
		if store != nil {
			if cfg, err := store.Get(r.Context(), info.ID); err != nil {
				log.Printf("overload: load key concurrency limit: %v", err)
			} else {
				keyLimit = cfg.Concurrency
			}
		}

		releaseKey, ok := keyed.Acquire(info.ID, keyLimit)
		if !ok {
			reject(w, r, auditor, info, "key_concurrency", keyLimit)
			return
		}
		defer releaseKey()

		release, _, err := gate.Acquire(r.Context())
		if err != nil {
			reject(w, r, auditor, info, reasonOf(err), int64(gate.max))
			return
		}
		defer release()

		next.ServeHTTP(w, r)
	})
}

func reasonOf(err error) string {
	switch {
	case errors.Is(err, ErrQueueFull):
		return "queue_full"
	case errors.Is(err, ErrQueueTimeout):
		return "queue_timeout"
	case errors.Is(err, ErrClosed):
		return "shutting_down"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "client_gone"
	default:
		return "unavailable"
	}
}

func reject(w http.ResponseWriter, r *http.Request, auditor Auditor, info *virtualkey.KeyInfo, reason string, limit int64) {
	log.Printf("overload: reject reason=%s key=%s limit=%d path=%s", reason, info.ID, limit, r.URL.Path)
	if auditor != nil {
		auditor.Record(r.Context(), "key.overloaded", info.EmpID, map[string]any{
			"keyId":  info.ID,
			"reason": reason,
			"limit":  limit,
			"path":   r.URL.Path,
		}, r.Header.Get("X-Request-Id"))
	}
	w.Header().Set("Retry-After", "1")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusServiceUnavailable)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{
		"code":       "overloaded",
		"reason":     reason,
		"retryAfter": 1,
	}})
}
