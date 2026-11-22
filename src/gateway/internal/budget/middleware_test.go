package budget

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/keykeep/gateway/internal/reqbody"
)

func TestInspectBodyParsesAndRestores(t *testing.T) {
	payload := `{"model":"mock-claude","max_tokens":256,"messages":[]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(payload))
	req.ContentLength = int64(len(payload))

	hint, ok := inspectBody(req)
	if !ok || hint.Model != "mock-claude" || hint.MaxTokens != 256 {
		t.Fatalf("hint=%#v ok=%v", hint, ok)
	}
	if hint.Size != int64(len(payload)) {
		t.Fatalf("size=%d", hint.Size)
	}
	restored, err := io.ReadAll(req.Body)
	if err != nil || string(restored) != payload {
		t.Fatalf("body not restored: %q %v", restored, err)
	}
}

func TestInspectBodySkipsUnsupportedMethodsAndEmpty(t *testing.T) {
	get := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	if _, ok := inspectBody(get); ok {
		t.Fatal("GET must not be inspected")
	}
	post := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	post.Body = nil
	if _, ok := inspectBody(post); ok {
		t.Fatal("nil body must be skipped")
	}
}

func TestInspectBodyIgnoresOversizedContentLength(t *testing.T) {
	payload := `{"model":"x"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(payload))
	req.ContentLength = maxInspectBytes + 1
	if _, ok := inspectBody(req); ok {
		t.Fatal("oversized body must be skipped")
	}
	restored, _ := io.ReadAll(req.Body)
	if string(restored) != payload {
		t.Fatalf("body consumed: %q", restored)
	}
}

func TestOutputTokensDefault(t *testing.T) {
	if outputTokens(reqbody.Hint{}) != 1024 {
		t.Fatal("default output estimate")
	}
	if outputTokens(reqbody.Hint{MaxCompletion: 64}) != 64 {
		t.Fatal("max_completion_tokens preferred")
	}
	if outputTokens(reqbody.Hint{MaxTokens: 32, MaxCompletion: 64}) != 32 {
		t.Fatal("max_tokens preferred")
	}
}

// TestInspectBodyReusesSharedHint 钉住批次 M 的口径：预算预检连续四次询问
// 都拿到同一份链级摘要，字段值不随询问次数漂移（缓存指针恒等的判据在
// internal/reqbody 里钉，见 TestBodyHintParsedOnce）。
func TestInspectBodyReusesSharedHint(t *testing.T) {
	payload := `{"model":"claude-sonnet-4.5","max_tokens":512,"messages":[]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(payload))
	var hints []reqbody.Hint
	handler := reqbody.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for i := 0; i < 4; i++ { // 模拟幂等/白名单/限流/预算四问
			hint, ok := inspectBody(r)
			if !ok {
				t.Fatalf("第 %d 次询问未拿到摘要", i+1)
			}
			hints = append(hints, hint)
		}
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	for i, h := range hints {
		if h.Model != "claude-sonnet-4.5" || h.MaxTokens != 512 || h.Size != int64(len(payload)) {
			t.Fatalf("第 %d 次摘要口径异常: %+v", i+1, h)
		}
	}
	if hints[0].Err != nil || hints[3].Err != nil {
		t.Fatalf("合法正文不应带解析错误: %v %v", hints[0].Err, hints[3].Err)
	}
}

func TestWriteErrorShape(t *testing.T) {
	rec := httptest.NewRecorder()
	writeError(rec, http.StatusPaymentRequired, "budget_exceeded", 120, "dept")
	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("status=%d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"budget_exceeded", "remainingCents", "dept"} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q: %s", want, body)
		}
	}
}
