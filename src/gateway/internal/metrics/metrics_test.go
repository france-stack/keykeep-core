package metrics

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/keykeep/gateway/internal/overload"
)

// 这条测试盯住两件事：注册表在两种部署形态下都能导出（无库时至少有无依赖指标），
// 以及重复 BindDatabase 不会 panic——启动路径上一旦 panic，网关就完全起不来。
func TestRegistryExportsAfterBind(t *testing.T) {
	gate := overload.New(overload.Config{MaxConcurrency: 2, QueueSize: 4})
	registry := New()
	registry.BindDatabase(Options{
		Gate:                gate,
		Ready:               func() bool { return true },
		RateLimitFailClosed: func() uint64 { return 3 },
	})
	registry.BindDatabase(Options{Gate: gate}) // 第二次必须是空操作

	registry.ObserveRequest("POST", 200, "", 0.25)
	registry.ObserveRateLimit("key", "deny")
	registry.ObserveAuditWrite(false)

	body := scrape(t, registry)

	for _, want := range []string{
		`keykeep_gateway_requests_total{method="POST",reason="",status="200"} 1`,
		`keykeep_gateway_request_duration_seconds_count{status="200"} 1`,
		`keykeep_gateway_ratelimit_decisions_total{outcome="deny",tier="key"} 1`,
		`keykeep_gateway_audit_writes_total{outcome="error"} 1`,
		`keykeep_gateway_ratelimit_fail_closed_total 3`,
		`keykeep_gateway_ready{component="database"} 1`,
		`keykeep_gateway_inflight 0`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("指标输出缺少 %q\n---\n%s", want, body)
		}
	}
}

// 未连库的部署形态（本地联调 / 只跑 SDK）也必须能导出无依赖指标。
func TestRegistryWorksWithoutDatabase(t *testing.T) {
	registry := New()
	registry.ObserveRequest("GET", 404, "client_error", 0.01)

	body := scrape(t, registry)
	if !strings.Contains(body, `keykeep_gateway_requests_total{method="GET",reason="client_error",status="404"} 1`) {
		t.Fatalf("无库形态缺少请求计数\n---\n%s", body)
	}
	if strings.Contains(body, "keykeep_gateway_inflight") {
		t.Fatal("未绑定数据库时不应出现依赖运行时对象的指标（会被误读成 0）")
	}
}

func scrape(t *testing.T, registry *Registry) string {
	t.Helper()
	server := httptest.NewServer(registry.Handler())
	defer server.Close()
	resp, err := http.Get(server.URL)
	if err != nil {
		t.Fatalf("抓取 /metrics 失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读取响应失败: %v", err)
	}
	return string(raw)
}
