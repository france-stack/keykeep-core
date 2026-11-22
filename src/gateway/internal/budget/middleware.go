package budget

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/keykeep/gateway/internal/ledger"
	"github.com/keykeep/gateway/internal/reqbody"
	"github.com/keykeep/gateway/internal/virtualkey"
)

// maxInspectBytes 限制为估算读取的请求体大小；超过则只做余额检查，不预扣。
const maxInspectBytes = 8 << 20

// Middleware 在虚拟 Key 校验之后做两级预算预检。
// 预扣金额按请求体估算，响应结束后释放；实际费用由账本插件结算。
func Middleware(gate *Gate, next http.Handler) http.Handler {
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
		var estimate int64
		if hint, ok := inspectBody(r); ok && hint.Model != "" {
			prompt, completion := gate.Price(r.Context(), hint.Model)
			if prompt > 0 || completion > 0 {
				estimate = ledger.EstimateCents(hint.Size/3, outputTokens(hint), prompt, completion)
			} else if gate.UnpricedFloorPerM > 0 {
				// 未定价模型按保底价预扣：与账本结算口径一致，预算不留绕过面。
				estimate = ledger.FloorCost(hint.Size/3, outputTokens(hint), gate.UnpricedFloorPerM)
			}
		}

		res, err := gate.Reserve(r.Context(), info.EmpID, estimate)
		if err != nil {
			log.Printf("budget: reserve: %v", err)
			writeError(w, http.StatusServiceUnavailable, "budget_unavailable", 0, "")
			return
		}
		defer res.Release(r.Context())
		if !res.Allowed() {
			writeError(w, http.StatusPaymentRequired, "budget_exceeded", res.RemainingCents(), res.Scope())
			return
		}
		next.ServeHTTP(w, r)
	})
}

// inspectBody 返回共享的请求体摘要。解析走 reqbody（整条链只解析一次），
// 这里只做"值不值得为它定价"的前置判断。
func inspectBody(r *http.Request) (reqbody.Hint, bool) {
	if r.Body == nil || (r.Method != http.MethodPost && r.Method != http.MethodPut && r.Method != http.MethodPatch) {
		return reqbody.Hint{}, false
	}
	if r.ContentLength > maxInspectBytes {
		return reqbody.Hint{}, false
	}
	hint := reqbody.BodyHint(r)
	if hint.TooLarge || hint.Size == 0 {
		return reqbody.Hint{}, false
	}
	return hint, true
}

func outputTokens(hint reqbody.Hint) int64 {
	switch {
	case hint.MaxTokens > 0:
		return hint.MaxTokens
	case hint.MaxCompletion > 0:
		return hint.MaxCompletion
	default:
		return 1024
	}
}

func writeError(w http.ResponseWriter, status int, code string, remaining int64, scope string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	body := map[string]any{"code": code}
	if remaining > 0 || scope != "" {
		body["remainingCents"] = remaining
		body["scope"] = scope
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"error": body})
}
