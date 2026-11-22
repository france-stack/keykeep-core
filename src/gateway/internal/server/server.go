// Package server 提供网关进程的 HTTP 外壳：健康检查 + 反向代理到内嵌 SDK。
package server

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"time"
)

// RequestState 记录一次转发的结果，供幂等中间件判定 completed/failed/outcome_unknown。
type RequestState struct {
	mu          sync.Mutex
	gotResponse bool
	statusCode  int
	proxyErr    error
}

func (s *RequestState) markResponse(status int) {
	s.mu.Lock()
	s.gotResponse = true
	s.statusCode = status
	s.mu.Unlock()
}

func (s *RequestState) markError(err error) {
	s.mu.Lock()
	s.proxyErr = err
	s.mu.Unlock()
}

// Result 返回 (是否收到响应, 状态码, 代理错误)。
func (s *RequestState) Result() (bool, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gotResponse, s.statusCode, s.proxyErr
}

type requestStateKey struct{}

// WithRequestState 把状态对象放进请求上下文；反代会克隆该上下文。
func WithRequestState(ctx context.Context, state *RequestState) context.Context {
	return context.WithValue(ctx, requestStateKey{}, state)
}

// ResponseTransformer 在响应落回客户端前处理 body（非流式）。
// 返回错误会让 ReverseProxy 走 ErrorHandler；实现方应尽量返回 nil。
type ResponseTransformer func(*http.Response) error

type responseTransformerKey struct{}

func WithResponseTransformer(ctx context.Context, fn ResponseTransformer) context.Context {
	return context.WithValue(ctx, responseTransformerKey{}, fn)
}

func responseTransformerFrom(ctx context.Context) ResponseTransformer {
	fn, _ := ctx.Value(responseTransformerKey{}).(ResponseTransformer)
	return fn
}

// RequestStateFrom 取出状态对象；没有则返回 nil。
func RequestStateFrom(ctx context.Context) *RequestState {
	state, _ := ctx.Value(requestStateKey{}).(*RequestState)
	return state
}

// NewProxyHandler 只负责把请求转发到内嵌 SDK。
// 上游 502/503 缺少 Retry-After 时补 1 秒，满足"过载要带回退提示"的契约（FR-25）。
func NewProxyHandler(sdk *url.URL) http.Handler {
	proxy := httputil.NewSingleHostReverseProxy(sdk)
	proxy.ModifyResponse = func(resp *http.Response) error {
		if state := RequestStateFrom(resp.Request.Context()); state != nil {
			state.markResponse(resp.StatusCode)
		}
		if fn := responseTransformerFrom(resp.Request.Context()); fn != nil {
			if err := fn(resp); err != nil {
				return err
			}
		}
		if (resp.StatusCode == http.StatusBadGateway || resp.StatusCode == http.StatusServiceUnavailable) &&
			resp.Header.Get("Retry-After") == "" {
			resp.Header.Set("Retry-After", "1")
		}
		return nil
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		if state := RequestStateFrom(r.Context()); state != nil {
			state.markError(err)
		}
		w.Header().Set("Retry-After", "1")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{
			"code": "upstream_error",
		}})
	}
	return proxy
}

// NewRootHandler 组合健康检查、本地自助接口与转发层。
func NewRootHandler(version string, local http.Handler, handles func(string) bool, forward http.Handler) http.Handler {
	return healthzHandler(version, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handles != nil && handles(r.URL.Path) {
			local.ServeHTTP(w, r)
			return
		}
		forward.ServeHTTP(w, r)
	}))
}

// NewHandler 返回网关的公开路由：/healthz 不走 SDK，其余全部转发。
func NewHandler(sdk *url.URL, version string) http.Handler {
	return healthzHandler(version, NewProxyHandler(sdk))
}

// NewRootHandlerWithMetrics 在健康检查之外再挂一个 /metrics。
//
// 两个端点都不经过虚拟 Key 校验，因此调用方必须自己保证它们只在内网/回环可读——
// 这也是它们不带任何身份信息的原因：/metrics 只有聚合计数，没有 Key、员工或路径。
// metrics 为 nil 时行为与 NewRootHandler 完全一致（未配置指标不影响转发）。
func NewRootHandlerWithMetrics(version string, local http.Handler, handles func(string) bool, forward http.Handler, metrics http.Handler) http.Handler {
	combined := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handles != nil && handles(r.URL.Path) {
			local.ServeHTTP(w, r)
			return
		}
		forward.ServeHTTP(w, r)
	})
	if metrics == nil {
		return healthzHandler(version, combined)
	}
	return muxHandler(version, map[string]http.Handler{"/metrics": metrics}, combined)
}

func healthzHandler(version string, next http.Handler) http.Handler {
	return muxHandler(version, nil, next)
}

// muxHandler 是 healthzHandler 的通用版：extra 里的精确路径优先于 next，
// 其余（含 /healthz）按原顺序处理。新增运维端点走这里，不再各自复制一套 mux。
func muxHandler(version string, extra map[string]http.Handler, next http.Handler) http.Handler {
	mux := http.NewServeMux()
	for path, handler := range extra {
		mux.Handle(path, handler)
	}
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status":  "ok",
			"version": version,
		})
	})

	mux.Handle("/", next)
	return mux
}

// NewHTTPServer 统一超时与头大小限制；SSE 由 ReverseProxy 逐块刷新。
func NewHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
}

// WaitPort 在启动阶段等待内嵌 SDK 监听就绪。
func WaitPort(addr string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}
