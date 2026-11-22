package reqbody

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// withState 手工装配 Middleware 的缓存状态，便于断言"每请求解析一次"。
func withState(body []byte) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	ctx := context.WithValue(req.Context(), ctxKey{}, &state{body: body})
	return req.WithContext(ctx)
}

// TestBodyHintParsesOnce 钉住批次 M 的不变量：同一条请求上第二次问模型
// 不再解析正文。判据用缓存指针恒等——它由 parseHint 唯一赋值，
// 若缓存被移除，第二次调用必然换一个新指针，测试立刻翻红。
// 不用计时做判据：计时断言在 CI 上会随机翻红。
func TestBodyHintParsesOnce(t *testing.T) {
	payload := []byte(`{"model":"claude-sonnet-4.5","max_tokens":1024,"stream":true}`)
	req := withState(payload)

	first := BodyHint(req)
	st, ok := fromContext(req.Context())
	if !ok || st.hint == nil {
		t.Fatal("首次询问未建立摘要缓存")
	}
	cached := st.hint
	for i := 0; i < 3; i++ {
		got := BodyHint(req)
		if st.hint != cached {
			t.Fatalf("第 %d 次复问重新解析了正文", i+2)
		}
		if got.Model != first.Model || got.MaxTokens != first.MaxTokens || got.Stream != first.Stream {
			t.Fatalf("第 %d 次复问口径漂移: %+v vs %+v", i+2, got, first)
		}
	}
}

// TestBodyHintFieldParity 逐个钉住四个消费方的字段口径。
// 收敛为一次解析的前提是：每个消费方拿到的值与它自行解析时一致。
func TestBodyHintFieldParity(t *testing.T) {
	payload := `{"model":"gemini-2.5-flash","max_tokens":2048,"max_completion_tokens":99,"stream":true}`
	h := parseHint([]byte(payload), false)
	if h.Model != "gemini-2.5-flash" || h.MaxTokens != 2048 || h.MaxCompletion != 99 || !h.Stream {
		t.Fatalf("字段口径异常: %+v", h)
	}
	if h.Size != int64(len(payload)) {
		t.Fatalf("size=%d want %d", h.Size, len(payload))
	}
	// max_tokens 缺失时只退回 max_completion_tokens（与限流/预算原口径一致）。
	h = parseHint([]byte(`{"model":"x","max_completion_tokens":64}`), false)
	if h.MaxTokens != 0 || h.MaxCompletion != 64 {
		t.Fatalf("max_completion_tokens 口径异常: %+v", h)
	}
	// 正文长度按原始字节算，不是按解码后的字符数——估算 token 用的是字节。
	h = parseHint([]byte(`{"model":"中文"}`), false)
	if h.Size != int64(len(`{"model":"中文"}`)) {
		t.Fatalf("中文正文应按字节计长度: %d", h.Size)
	}
}

// TestBodyHintLenientTypes 类型不符取零值且不产生解析错误：
// 等价于原先各中间件 `_ = json.Unmarshal(...)` 后按零值继续的写法。
// 用 RawMessage 承载非 model 字段，正是为了让"别人的字段类型写错"
// 不要把另一路的判据也带上错误。
func TestBodyHintLenientTypes(t *testing.T) {
	h := parseHint([]byte(`{"model":"claude-sonnet-4.5","max_tokens":"1024","stream":"yes"}`), false)
	if h.Err != nil {
		t.Fatalf("非 model 字段类型不符不应产生解析错误: %v", h.Err)
	}
	if h.Model != "claude-sonnet-4.5" || h.MaxTokens != 0 || h.Stream {
		t.Fatalf("宽松转换口径异常: %+v", h)
	}
}

// TestBodyHintKeepsModelError 白名单的 fail-close 依赖 model 自身解码失败：
// model 类型不符或正文不是 JSON 对象时，Err 必须非空且 Model 为空。
func TestBodyHintKeepsModelError(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    string
		wantErr bool
		model   string // 只断言能确定下来的值：解码器会保留已读到的字段
	}{
		{"model 类型不符", `{"model":123}`, true, ""},
		{"顶层不是对象", `[1,2,3]`, true, ""},
		{"截断的 JSON", `{"model":"x"`, true, ""},
		{"model 缺失", `{"max_tokens":8}`, false, ""},
	} {
		h := parseHint([]byte(tc.body), false)
		if h.Model != tc.model {
			t.Fatalf("%s: model=%q want %q", tc.name, h.Model, tc.model)
		}
		if tc.wantErr && h.Err == nil {
			t.Fatalf("%s: 必须带解析错误（白名单据此 fail-close）", tc.name)
		}
		if !tc.wantErr && h.Err != nil {
			t.Fatalf("%s: 合法正文不应带解析错误: %v", tc.name, h.Err)
		}
	}
}

// TestBodyHintSkipsOversized 超限正文不解析：与"超限请求交给上游边界处理"一致，
// 也避免对半截正文做无意义解码。
func TestBodyHintSkipsOversized(t *testing.T) {
	h := parseHint(nil, true)
	if h.Err != nil || h.Model != "" || h.Size != 0 || !h.TooLarge {
		t.Fatalf("超限正文应短路: %+v", h)
	}
}

// TestBodyHintWithoutMiddleware 未装配 Middleware（单测直调中间件）时，
// BodyHint 回退到"读一次并解析"，且必须把正文放回，转发不受影响。
func TestBodyHintWithoutMiddleware(t *testing.T) {
	payload := `{"model":"claude-sonnet-4.5","max_tokens":8}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader([]byte(payload)))
	h := BodyHint(req)
	if h.Model != "claude-sonnet-4.5" || h.MaxTokens != 8 || h.TooLarge {
		t.Fatalf("回退路径口径异常: %+v", h)
	}
	restored, err := io.ReadAll(req.Body)
	if err != nil || string(restored) != payload {
		t.Fatalf("正文未放回: %q %v", restored, err)
	}
}
