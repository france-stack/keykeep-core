// Package reqbody 让转发链只读取一次请求体。
//
// 网关的幂等、白名单、限流、预算都要看请求体；此前各自 io.ReadAll + 放回，
// 单请求最多拷贝 4 次（每次上限 8 MiB）。本包在最外层读一次并缓存原始字节，
// 下游通过 Read 复用；原始字节保持给幂等哈希，转发体由脱敏层另行替换。
package reqbody

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
)

// MaxBytes 与既有各中间件的请求体上限保持一致。
const MaxBytes = 8 << 20

type state struct {
	body     []byte
	tooLarge bool
	hint     *Hint
}

type ctxKey struct{}

// Middleware 读取并放回请求体，把原始字节放入请求上下文。
// 只处理可能带 body 的方法；/healthz 与 GET 不读。
// 超过 MaxBytes 的请求在此直接 413：请求体上限是数据面的统一边界，
// 不随"该 Key 是否配了白名单"变化（否则无白名单 Key 可无界转发）。
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body == nil || !hasBody(r.Method) {
			next.ServeHTTP(w, r)
			return
		}
		st := &state{}
		if r.ContentLength > MaxBytes {
			// 声明超限：不读正文，直接拒绝。
			writeTooLarge(w)
			return
		}
		buf, err := io.ReadAll(io.LimitReader(r.Body, MaxBytes+1))
		if err != nil {
			// 读失败：无法确定长度，按超限拒绝（fail-close）。
			writeTooLarge(w)
			return
		}
		if len(buf) > MaxBytes {
			writeTooLarge(w)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(buf))
		r.ContentLength = int64(len(buf))
		st.body = buf
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, st)))
	})
}

// writeTooLarge 返回与白名单一致的 413 错误体。
func writeTooLarge(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "close")
	w.WriteHeader(http.StatusRequestEntityTooLarge)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "request_too_large"}})
}

// Read 返回原始请求体。若外层没有安装 Middleware（单测直接调用中间件），
// 回退到“读一次并放回”，行为与旧实现一致。
func Read(r *http.Request) ([]byte, bool) {
	if st, ok := fromContext(r.Context()); ok {
		return st.body, st.tooLarge
	}
	if r.Body == nil || !hasBody(r.Method) {
		return nil, false
	}
	buf, err := io.ReadAll(io.LimitReader(r.Body, MaxBytes+1))
	if err != nil {
		r.Body = io.NopCloser(bytes.NewReader(buf))
		return buf, true
	}
	if len(buf) > MaxBytes {
		r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(buf), r.Body))
		return nil, true
	}
	r.Body = io.NopCloser(bytes.NewReader(buf))
	r.ContentLength = int64(len(buf))
	return buf, false
}

// From 返回缓存的原始字节；没有缓存时 ok=false。
func From(r *http.Request) ([]byte, bool) {
	st, ok := fromContext(r.Context())
	if !ok {
		return nil, false
	}
	return st.body, true
}

// Hint 是转发链共享的请求体摘要：整条链只解析一次 JSON。
//
// 网关在转发前要问四个问题——这个模型能不能用（policy）、这次大概多少
// token（ratelimit）、预算够不够（budget）、这笔请求见过没有（idempotency），
// 证据都在同一份 body 里。encoding/json 不认"上次解析过"，各自 Unmarshal
// 等于把同一正文完整解析四遍（批次 M 实测：约 116 µs / 请求，其中三遍是重复劳动）。
// 本包已经把"读四遍"收敛成"读一遍"，这里把剩下的"解析四遍"一并收敛。
//
// 字段口径刻意与各中间件原先自行解析时保持等价：
//   - Model 与解析错误来自同一形状的解码，白名单的 fail-close 判据不变；
//   - 数值/布尔字段类型不符时取零值，等同于原先"忽略解析错误"的写法；
//   - 用 RawMessage 承载非 model 字段，避免"先出现的坏字段截断后出现的正常字段"
//     这种顺序依赖（原先按各自结构体解码时会截断，共享解析不该引入新的截断面）。
type Hint struct {
	Err           error
	Model         string
	MaxTokens     int64
	MaxCompletion int64
	Stream        bool
	Size          int64
	TooLarge      bool
}

// rawHint 承载四个中间件实际关心的字段；除 model 外一律 RawMessage，
// 由使用方按需宽松转换。
type rawHint struct {
	Model         string          `json:"model"`
	MaxTokens     json.RawMessage `json:"max_tokens"`
	MaxCompletion json.RawMessage `json:"max_completion_tokens"`
	Stream        json.RawMessage `json:"stream"`
}

// BodyHint 返回请求体摘要。装了 Middleware 时同一请求内多次调用只解析一次；
// 没装（单测直接调中间件）时退化为"读一次并解析"，与 Read 的口径一致。
//
// 摘要缓存在 per-request 的 state 上且不加锁：一条请求的中间件链是单 goroutine
// 顺序执行的，跨 goroutine 共享同一 *state 不属于本包的用法。
func BodyHint(r *http.Request) Hint {
	if st, ok := fromContext(r.Context()); ok {
		if st.hint == nil {
			st.hint = parseHint(st.body, st.tooLarge)
		}
		return *st.hint
	}
	body, tooLarge := Read(r)
	return *parseHint(body, tooLarge)
}

func parseHint(body []byte, tooLarge bool) *Hint {
	h := &Hint{Size: int64(len(body)), TooLarge: tooLarge}
	if tooLarge || len(body) == 0 {
		return h
	}
	var raw rawHint
	h.Err = json.Unmarshal(body, &raw)
	h.Model = raw.Model
	h.MaxTokens = intField(raw.MaxTokens)
	h.MaxCompletion = intField(raw.MaxCompletion)
	h.Stream = boolField(raw.Stream)
	return h
}

func intField(raw json.RawMessage) int64 {
	var n int64
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &n)
	}
	return n
}

func boolField(raw json.RawMessage) bool {
	var v bool
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &v)
	}
	return v
}

func fromContext(ctx context.Context) (*state, bool) {
	st, ok := ctx.Value(ctxKey{}).(*state)
	return st, ok
}

func hasBody(method string) bool {
	return method == http.MethodPost || method == http.MethodPut || method == http.MethodPatch
}
