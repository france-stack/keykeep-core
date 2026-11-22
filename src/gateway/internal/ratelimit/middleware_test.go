package ratelimit

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/keykeep/gateway/internal/virtualkey"
)

// 这两条测试盯住 2026-11-08 改的默认行为：限额读不到时不再沉默放行。
//
// 改动前 writeUnavailable 不存在，两条路径都直接 next.ServeHTTP——PG 抖动期间
// 计费与预算一起被旁路，且只有一行日志，没有任何调用方知道自己绕过了限流。

var errUnavailable = errors.New("pg unavailable")

// brokenCounter 让 Admit 直接失败，模拟 PG 抖动。
type brokenCounter struct{}

func (brokenCounter) Admit(context.Context, string, Config, int64, int64) (*Reservation, *Denial, error) {
	return nil, nil, errUnavailable
}
func (brokenCounter) AddActual(context.Context, string, int64, int64) error { return nil }
func (brokenCounter) Snapshot(context.Context, string) (int64, int64, int64, error) {
	return 0, 0, 0, errUnavailable
}
func (brokenCounter) Close() {}

// newBrokenStore 造一个必然读库失败的 Store：没有可用连接池，也没有缓存，
// 于是 Get/GetGlobal 都返回错误，正好走「后端不可用」这条分支。
func newBrokenStore(t *testing.T) *Store {
	t.Helper()
	return NewStoreWithPool(nil)
}

func requestWithKey(t *testing.T) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	return req.WithContext(virtualkey.WithKeyInfo(req.Context(), &virtualkey.KeyInfo{
		ID:    "11111111-1111-1111-1111-111111111111",
		EmpID: "22222222-2222-2222-2222-222222222222",
	}))
}

func TestMiddlewareFailsClosedByDefault(t *testing.T) {
	t.Setenv("KEYKEEP_RATE_FAIL_MODE", "")
	store := newBrokenStore(t)
	called := false
	handler := Middleware(NewLimiter(), store, nil, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, requestWithKey(t))

	if called {
		t.Fatal("限额读不到时请求被放行（fail-open）——默认必须是 fail-closed")
	}
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("状态码 = %d，期望 503", recorder.Code)
	}
	if got := recorder.Header().Get("Retry-After"); got == "" {
		t.Fatal("503 必须带 Retry-After，否则调用方无法区分永久失败与暂时不可用")
	}
}

func TestMiddlewareFailOpenWhenExplicitlyConfigured(t *testing.T) {
	t.Setenv("KEYKEEP_RATE_FAIL_MODE", "open")
	store := newBrokenStore(t)
	called := false
	handler := Middleware(NewLimiter(), store, nil, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, requestWithKey(t))

	if !called {
		t.Fatal("显式 fail-open 时请求应当放行")
	}
}

// 计数器失败同样要按同一开关处理：Admit 报错时 fail-closed 不能放行。
func TestCounterFailureRespectsFailMode(t *testing.T) {
	t.Setenv("KEYKEEP_RATE_FAIL_MODE", "")

	// 限额配置能读到（内存里塞一个未过期的缓存），但计数器失败。
	store := NewStoreWithPool(nil)
	store.cache["key:11111111-1111-1111-1111-111111111111"] = cached{cfg: Config{RPM: 10}, expires: timeHourFromNow()}
	store.cache["global"] = cached{cfg: Config{}, expires: timeHourFromNow()}
	called := false
	handler := Middleware(brokenCounter{}, store, nil, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, requestWithKey(t))
	if called {
		t.Fatal("计数器不可用时请求被放行（fail-open）——默认必须是 fail-closed")
	}
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("状态码 = %d，期望 503", recorder.Code)
	}
}

// timeHourFromNow 让缓存条目处于有效期内。
func timeHourFromNow() time.Time { return time.Now().Add(time.Hour) }
