// Package httpmw 收纳跨中间件共享的最小件：审计旁路契约与响应记录器。
//
// 这里只允许放"多个中间件逐字复制过"的东西，不放业务逻辑，也不依赖
// 任何其他 internal 包——它必须是依赖图的最叶子。
package httpmw

import (
	"context"
	"net/http"
)

// Auditor 是所有中间件共用的审计旁路契约；nil 表示不写审计。
//
// 收敛成一处之前，audit.Writer 的这一个方法在 6 个包里各抄了一份接口声明
// （policy / ratelimit / overload / redact / virtualkey / idempotency）。签名
// 一旦调整，只有直接接入 audit.Writer 的那个包会编译失败，其余 5 份静默漂移，
// 表现为"某些审计事件悄悄不写了"。
type Auditor interface {
	Record(ctx context.Context, action, actorID string, detail map[string]any, requestID string)
}

// Recorder 记录首个状态码与写出字节数。
//
// Flush 与 Unwrap 是硬要求：网关用 ReverseProxy 转发 SSE，包装层一旦丢掉
// Flusher，事件流会被攒成一大块，客户端看到的就是"整段回答一次性出现"。
type Recorder struct {
	http.ResponseWriter
	Code  int
	Bytes int64
}

func NewRecorder(w http.ResponseWriter) *Recorder {
	return &Recorder{ResponseWriter: w}
}

func (r *Recorder) WriteHeader(code int) {
	if r.Code == 0 {
		r.Code = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *Recorder) Write(data []byte) (int, error) {
	if r.Code == 0 {
		r.Code = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(data)
	r.Bytes += int64(n)
	return n, err
}

// Status 返回首个状态码；完全没写响应时按 net/http 的隐式约定返回 200。
func (r *Recorder) Status() int {
	if r.Code == 0 {
		return http.StatusOK
	}
	return r.Code
}

func (r *Recorder) Flush() {
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (r *Recorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }
