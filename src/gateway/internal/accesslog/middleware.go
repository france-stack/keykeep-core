// Package accesslog 输出网关的访问日志：方法、路径、状态码、耗时与身份。
//
// 只记录元数据，不记录请求/响应正文、提示词或凭据；/healthz 与 /metrics 不记录。
package accesslog

import (
	"log"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/keykeep/gateway/internal/httpmw"
	"github.com/keykeep/gateway/internal/virtualkey"
)

// Middleware 放在虚拟 Key 之后、根路由之前，能拿到已校验的身份。
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" || r.URL.Path == "/metrics" {
			next.ServeHTTP(w, r)
			return
		}
		started := time.Now()
		recorder := httpmw.NewRecorder(w)
		next.ServeHTTP(recorder, r)
		elapsed := time.Since(started)

		keyID, empID := "", ""
		if info, ok := virtualkey.FromContext(r.Context()); ok {
			keyID, empID = info.ID, info.EmpID
		}
		log.Printf("access method=%s path=%s status=%d bytes=%d duration_ms=%d key=%s emp=%s request=%s",
			r.Method, r.URL.Path, recorder.Status(), recorder.Bytes, elapsed.Milliseconds(),
			keyID, empID, r.Header.Get("X-Request-Id"))
		observeRequest(r.Method, recorder.Status(), elapsed)
	})
}

// metricsSink 是可选的指标出口；nil 时不统计。
// 用最小接口而不是具体类型，保持依赖单向（metrics 不反过来 import accesslog）。
var metricsSink atomic.Value

// SetMetrics 注册指标出口。
func SetMetrics(sink interface {
	ObserveRequest(method string, status int, reason string, seconds float64)
}) {
	if sink == nil {
		return
	}
	metricsSink.Store(sink)
}

func observeRequest(method string, status int, elapsed time.Duration) {
	if sink, ok := metricsSink.Load().(interface {
		ObserveRequest(method string, status int, reason string, seconds float64)
	}); ok && sink != nil {
		sink.ObserveRequest(method, status, reasonOf(status), elapsed.Seconds())
	}
}

// reasonOf 把状态码收敛成有限几个标签值。指标标签必须是有限枚举，
// 而业务错误码集合会持续增长；需要细粒度原因时看访问日志。
func reasonOf(status int) string {
	switch {
	case status < 400:
		return ""
	case status == http.StatusTooManyRequests:
		return "rate_limited"
	case status == http.StatusServiceUnavailable:
		return "unavailable"
	case status == http.StatusUnauthorized:
		return "unauthorized"
	case status == http.StatusForbidden:
		return "forbidden"
	case status == http.StatusConflict:
		return "conflict"
	case status == http.StatusRequestEntityTooLarge:
		return "too_large"
	case status == http.StatusPaymentRequired:
		return "budget"
	case status >= 500:
		return "upstream_or_internal"
	default:
		return "client_error"
	}
}
