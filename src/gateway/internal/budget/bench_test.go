package budget

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/keykeep/gateway/internal/reqbody"
)

func benchBody() string {
	return `{"model":"claude-sonnet-4.5","max_tokens":1024,"messages":[` +
		strings.Repeat(`{"role":"user","content":"`+strings.Repeat("你好这是一段中文上下文", 12)+`"},`, 8) +
		`{"role":"assistant","content":"ok"}]}`
}

// BenchmarkInspectBody 量预算预检在"本请求第一次有人问模型"时的成本：读体 + 一次解析。
// 装 reqbody.Middleware 且前置中间件已经问过模型时，这里会命中链级共享摘要，
// 数值随之下降——四种问法各自独立计时之和 > 一次共享解析，差值即批次 M 的收益，
// 同一次运行里的 BenchmarkBodyHintShared 给出更直接的对照。
func BenchmarkInspectBody(b *testing.B) {
	body := benchBody()
	handler := reqbody.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hint, ok := inspectBody(r)
		if !ok || hint.Model == "" || hint.Size == 0 {
			b.Fatalf("hint 解析异常: %+v ok=%v", hint, ok)
		}
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		handler.ServeHTTP(rec, req)
	}
}

// BenchmarkOutputTokens 量纯计算部分（无 IO 无解析），作为上面那条的对照。
func BenchmarkOutputTokens(b *testing.B) {
	hint := reqbody.Hint{Model: "claude-sonnet-4.5", MaxTokens: 0, MaxCompletion: 2048}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if outputTokens(hint) != 2048 {
			b.Fatal("口径变了")
		}
	}
}
