package redact

import (
	"strings"
	"testing"
)

// benchText 是一条典型请求正文规模的文本，内含三处可被打码的 PII 形态。
// 打码发生在转发前的每个请求上，因此它的成本是每请求的。
// 刻意不用真实凭据形态（sk-/kk_ 前缀）：基准夹具进仓库，`secret-scan` 会按凭据拦下，
// 而这几条 PII 同样能覆盖规则扫描的成本。
func benchText() string {
	return `上下文：` + strings.Repeat("这是一段中文提示词，包含若干技术术语与实体名称。", 30) +
		` zhang.wei@example.com 与手机号 13800138000、工号 EMP-10086 结尾`
}

// BenchmarkMaskText 量打码引擎对一段文本的全部规则扫描。
func BenchmarkMaskText(b *testing.B) {
	engine := NewEngine(DefaultRules())
	mapper := NewMapper()
	text := benchText()
	b.SetBytes(int64(len(text)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if got := engine.MaskText(text, mapper); got == text {
			b.Fatal("未发生打码，基准无效")
		}
	}
}

// BenchmarkSanitize 量不可逆出口（错误体/日志）的打码成本。
func BenchmarkSanitize(b *testing.B) {
	engine := NewEngine(DefaultRules())
	text := benchText()
	b.SetBytes(int64(len(text)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if got := engine.Sanitize(text); got == text {
			b.Fatal("未发生脱敏，基准无效")
		}
	}
}

// BenchmarkMapperPlaceholder 量占位符映射的单次成本：它决定"同一凭据在一次
// 会话内映射到同一占位符"这条语义的开销上限。
func BenchmarkMapperPlaceholder(b *testing.B) {
	mapper := NewMapper()
	values := []string{"sk-aaaa1111", "sk-bbbb2222", "13800138000"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		mapper.Placeholder(values[i%len(values)])
	}
}

// BenchmarkRestoreBytes 量流式占位符还原（批次 G 的 B4 修复路径）的每块成本。
func BenchmarkRestoreBytes(b *testing.B) {
	mapper := NewMapper()
	placeholder := mapper.Placeholder("sk-aaaa1111")
	chunk := []byte("data: " + strings.Repeat("一些流式增量文本。", 20) + placeholder + "\n\n")
	b.SetBytes(int64(len(chunk)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		mapper.RestoreBytes(chunk)
	}
}
