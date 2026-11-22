package policy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/keykeep/gateway/internal/reqbody"
	"github.com/keykeep/gateway/internal/virtualkey"
)

// benchBody 是一条接近真实规模的聊天请求：模型名 + 一轮多轮上下文，约 2 KiB。
// 选这个量级是因为转发链上的 JSON 解析成本随正文线性增长，太小量不出差异。
func benchBody() string {
	return `{"model":"claude-sonnet-4.5","max_tokens":1024,"stream":true,"messages":[` +
		strings.Repeat(`{"role":"user","content":"`+strings.Repeat("你好这是一段中文上下文", 12)+`"},`, 8) +
		`{"role":"assistant","content":"ok"}]}`
}

// BenchmarkWhitelistRequest 量「读体 + 解析 + 白名单判定」整段成本。
// 它是热路径四次全量 JSON 解析中的第一次；批次 M 把四次收敛成一次，
// 这条基准就是收敛前后的可比锚点（链路上装了 reqbody.Middleware，
// 所以读体不重复计费，量到的主要是解析）。
func BenchmarkWhitelistRequest(b *testing.B) {
	info := keyInfo("claude-*", "gpt-*")
	sink := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	handler := reqbody.Middleware(
		virtualkey.Middleware(fakeStore{info: info}, "ik_", nil, Whitelist(nil, sink)),
	)
	body := benchBody()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer kk_"+strings.Repeat("a", 43))
		req.Header.Set("Content-Type", "application/json")
		handler.ServeHTTP(httptest.NewRecorder(), req)
	}
}

// BenchmarkMatchesAny 只量通配匹配本身（不分配正则的逐字符回溯）。
func BenchmarkMatchesAny(b *testing.B) {
	patterns := []string{"claude-sonnet-4.5", "gpt-*", "vendor/model?", "*/qwen3"}
	cases := []string{"claude-sonnet-4.5", "gpt-5.1", "vendor/model7", "alibaba/qwen3", "nope"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		MatchesAny(patterns, cases[i%len(cases)])
	}
}

// BenchmarkGeminiPathModel 量 Gemini 路径模型提取（白名单的第二个模型事实源）。
func BenchmarkGeminiPathModel(b *testing.B) {
	path := "/v1beta/models/gemini-2.0-flash:generateContent"
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		geminiPathModel(path)
	}
}
