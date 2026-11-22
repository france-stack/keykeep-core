package relay

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// testKey 是运行期拼出来的假 Key：避免在源码里写出高熵字符串（secret-scan 会命中）。
var testKey = "kk_test_" + strings.Repeat("x", 36)

func newTestRelay(t *testing.T, upstream *httptest.Server) http.Handler {
	t.Helper()
	u, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	return NewHandler(Config{
		Upstream: u,
		APIKey:   testKey,
		Version:  "test",
		Listen:   "127.0.0.1:3426",
		ClientID: "test-box",
	})
}

// 核心契约：Agent 带的是占位 Key，到达公司网关时必须换成员工虚拟 Key，
// 且客户端自带的鉴权头一个都不能漏过去。
func TestForwardsWithVirtualKeyAndStripsClientCredentials(t *testing.T) {
	var got http.Header
	var gotQuery string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer upstream.Close()

	handler := newTestRelay(t, upstream)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages?key=leaked-in-query", strings.NewReader(`{}`))
	req.Host = "127.0.0.1:3426"
	req.Header.Set("Authorization", "Bearer keykeep-local")
	req.Header.Set("x-api-key", "keykeep-local")
	req.Header.Set("x-goog-api-key", "keykeep-local")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if want := "Bearer " + testKey; got.Get("Authorization") != want {
		t.Fatalf("Authorization = %q, want %q", got.Get("Authorization"), want)
	}
	for _, header := range []string{"x-api-key", "X-Api-Key", "x-goog-api-key"} {
		if value := got.Get(header); value != "" {
			t.Fatalf("%s leaked upstream: %q", header, value)
		}
	}
	if strings.Contains(gotQuery, "key=") {
		t.Fatalf("query string key leaked upstream: %q", gotQuery)
	}
	if got.Get("X-KeyKeep-Client") != "test-box" {
		t.Fatalf("client id missing: %q", got.Get("X-KeyKeep-Client"))
	}
	// 连接器必须为每个请求补 X-Request-Id：网关侧幂等/审计/账本依赖它串链。
	if got.Get("X-Request-Id") == "" {
		t.Fatal("X-Request-Id not injected upstream")
	}
}

// 连接器内嵌员工 Key，只服务本机：非回环 Host（DNS rebinding）与
// 非回环 Origin/Referer（跨站简单请求）都必须被拒绝。
func TestRejectsNonLoopbackHostAndOrigin(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("upstream must not be reached")
	}))
	defer upstream.Close()
	handler := newTestRelay(t, upstream)

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	req.Host = "evil.example.com:3426" // DNS rebinding 形态
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-loopback Host: status = %d, want 403", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	req.Host = "127.0.0.1:3426"
	req.Header.Set("Origin", "https://evil.example.com") // 跨站简单请求形态
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-site Origin: status = %d, want 403", rec.Code)
	}
}

func TestIsLoopbackHost(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:3426", true},
		{"localhost:3426", true},
		{"[::1]:3426", true},
		{":3426", false}, // Go 会绑所有网卡
		{"0.0.0.0:3426", false},
		{"192.168.1.5:3426", false},
		{"evil.example.com:3426", false},
	}
	for _, tc := range cases {
		if got := IsLoopbackHost(tc.addr); got != tc.want {
			t.Fatalf("IsLoopbackHost(%q) = %v, want %v", tc.addr, got, tc.want)
		}
	}
}

func TestHealthzReportsModeAndUpstream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			_, _ = io.WriteString(w, `{"status":"ok"}`)
			return
		}
		w.WriteHeader(http.StatusTeapot)
	}))
	defer upstream.Close()

	handler := newTestRelay(t, upstream)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Host = "127.0.0.1:3426"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, needle := range []string{`"mode":"client"`, `"upstreamReachable":true`, `"version":"test"`} {
		if !strings.Contains(body, needle) {
			t.Fatalf("health body %q missing %s", body, needle)
		}
	}
}

func TestStatusNeverReturnsPlaintextKey(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{}`)
	}))
	defer upstream.Close()

	handler := newTestRelay(t, upstream)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/keykeep/status", nil)
	req.Host = "127.0.0.1:3426"
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, testKey) {
		t.Fatalf("status leaked key material: %s", body)
	}
	if !strings.Contains(body, `"baseUrlAnthropic":"http://127.0.0.1:3426"`) {
		t.Fatalf("status missing anthropic base: %s", body)
	}
	if !strings.Contains(body, `"baseUrlOpenAI":"http://127.0.0.1:3426/v1"`) {
		t.Fatalf("status missing openai base: %s", body)
	}
}

func TestUpstreamDownReturnsExplanation(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	u, _ := url.Parse(upstream.URL)
	upstream.Close() // 立刻关掉，模拟断网

	handler := NewHandler(Config{Upstream: u, APIKey: "kk_x", Version: "test", Listen: "127.0.0.1:3426"})
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	req.Host = "127.0.0.1:3426"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "upstream_unreachable") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestDashboardHasNoSecretPlaceholders(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()
	handler := newTestRelay(t, upstream)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "127.0.0.1:3426"
	req.Header.Set("Accept", "text/html")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "KeyKeep 连接器") {
		t.Fatalf("dashboard missing title")
	}
}
