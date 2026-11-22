package reqbody

import (
	"encoding/json"
	"strings"
	"testing"
)

// benchPayload 与各中间件基准同源：约 3.7 KB 的真实聊天正文，
// 带 model / max_tokens / stream 三个热路径字段。
func benchPayload() []byte {
	return []byte(`{"model":"claude-sonnet-4.5","max_tokens":1024,"stream":false,"messages":[` +
		strings.Repeat(`{"role":"user","content":"`+strings.Repeat("你好这是一段中文上下文", 12)+`"},`, 8) +
		`{"role":"assistant","content":"ok"}]}`)
}

// BenchmarkUnmarshalPerConsumer 是批次 M 改造前的形态：四个消费方各自把同一份
// 正文完整解析一遍。这里用与原先各中间件相同形状的解码结构体，量的是
// "问完四个问题"的解码成本，不是某一次解析的成本。
func BenchmarkUnmarshalPerConsumer(b *testing.B) {
	body := benchPayload()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		var policy struct {
			Model string `json:"model"`
		}
		var limit struct {
			Model         string `json:"model"`
			MaxTokens     int64  `json:"max_tokens"`
			MaxCompletion int64  `json:"max_completion_tokens"`
			Stream        bool   `json:"stream"`
		}
		var budget struct {
			Model         string `json:"model"`
			MaxTokens     int64  `json:"max_tokens"`
			MaxCompletion int64  `json:"max_completion_tokens"`
		}
		var claim struct {
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
		}
		for _, dst := range []any{&policy, &limit, &budget, &claim} {
			if err := json.Unmarshal(body, dst); err != nil {
				b.Fatalf("正文非法，基准无效: %v", err)
			}
		}
		if policy.Model == "" || limit.MaxTokens == 0 {
			b.Fatal("未解析出字段，基准无效")
		}
	}
}

// BenchmarkBodyHintShared 是改造后的形态：同一请求上四个消费方各问一次，
// 只有第一次真正解析，后三次命中链级缓存。
// 两条基准同一次运行里的比值就是热路径解析次数的收敛倍数。
func BenchmarkBodyHintShared(b *testing.B) {
	req := withState(benchPayload())
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		for q := 0; q < 4; q++ {
			hint := BodyHint(req)
			if hint.Model == "" || hint.MaxTokens == 0 || hint.Err != nil {
				b.Fatalf("摘要口径异常: %+v", hint)
			}
		}
	}
}

// BenchmarkParseHintOnce 单独量一次共享解析本身的成本（无缓存命中），
// 用于确认"收敛"没有将单次解析变慢——联合结构体比单字段结构体略贵是正常的。
func BenchmarkParseHintOnce(b *testing.B) {
	body := benchPayload()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		h := parseHint(body, false)
		if h.Model == "" || h.MaxTokens == 0 {
			b.Fatal("未解析出字段，基准无效")
		}
	}
}
