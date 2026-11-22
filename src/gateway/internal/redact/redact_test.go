package redact

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/keykeep/gateway/internal/reqbody"
)

func TestEngineMasksAndRestores(t *testing.T) {
	engine := NewEngine(DefaultRules())
	mapper := NewMapper()
	fakeSecret := "sk-" + strings.Repeat("a", 24)
	text := "contact 13800138000 or alice@example.com; key " + fakeSecret
	masked := engine.MaskText(text, mapper)

	if strings.Contains(masked, "13800138000") || strings.Contains(masked, "alice@example.com") {
		t.Fatalf("masked text still contains PII: %s", masked)
	}
	if !ContainsPlaceholder(masked) {
		t.Fatalf("expected placeholders: %s", masked)
	}
	if restored := mapper.Restore(masked); restored != text {
		t.Fatalf("restore mismatch:\n got %q\nwant %q", restored, text)
	}
}

func TestEngineReusesPlaceholderForSameValue(t *testing.T) {
	engine := NewEngine(DefaultRules())
	mapper := NewMapper()
	masked := engine.MaskText("13800138000 and 13800138000", mapper)
	if mapper.Count() != 1 {
		t.Fatalf("mapping count = %d, want 1 (%s)", mapper.Count(), masked)
	}
	if strings.Count(masked, "⟦PII-0001⟧") != 2 {
		t.Fatalf("same value should reuse placeholder: %s", masked)
	}
}

func TestMaskJSONPreservesShape(t *testing.T) {
	engine := NewEngine(DefaultRules())
	mapper := NewMapper()
	body := []byte(`{"model":"m","stream":true,"messages":[{"role":"user","content":"mail a@b.com"}]}`)
	out, err := engine.MaskJSON(context.Background(), body, mapper, nil)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out, []byte("a@b.com")) {
		t.Fatalf("email not masked: %s", out)
	}
	if !bytes.Contains(out, []byte(`"stream":true`)) || !bytes.Contains(out, []byte(`"model":"m"`)) {
		t.Fatalf("JSON shape changed: %s", out)
	}
}

func TestSanitizeIsIrreversible(t *testing.T) {
	engine := NewEngine(DefaultRules())
	fakeSecret := "sk-" + strings.Repeat("a", 24)
	out := engine.Sanitize("key " + fakeSecret)
	if strings.Contains(out, fakeSecret) || !strings.Contains(out, "[REDACTED]") {
		t.Fatalf("sanitize failed: %s", out)
	}
}

func TestNormalizeModeDefault(t *testing.T) {
	for _, raw := range []string{"", "unknown"} {
		if got := NormalizeMode(raw); got != ModeRulesOnly {
			t.Fatalf("NormalizeMode(%q) = %s, want rules-only", raw, got)
		}
	}
	if NormalizeMode("block") != ModeBlock || NormalizeMode("off") != ModePassThrough {
		t.Fatal("mode normalization failed")
	}
}

func TestMiddlewareMasksBodyAndRestoresResponse(t *testing.T) {
	engine := NewEngine(DefaultRules())
	holder := NewHolder(engine)
	var forwarded string
	var captured *Mapper
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		forwarded = string(body)
		captured = MapperFrom(r.Context())
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
	})
	handler := Middleware(Config{Mode: ModeRulesOnly, Holder: holder}, next)

	body := `{"messages":[{"role":"user","content":"call me 13800138000"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if strings.Contains(forwarded, "13800138000") {
		t.Fatalf("upstream saw raw phone: %s", forwarded)
	}
	if rec.Header().Get("X-Masking") != "rules-only" {
		t.Fatalf("X-Masking = %q", rec.Header().Get("X-Masking"))
	}
	if captured == nil {
		t.Fatal("mapper missing from request context")
	}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"echo":"` + forwarded + `"}`)),
	}
	if err := transformResponse(resp, engine, captured); err != nil {
		t.Fatal(err)
	}
	out, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(out), "13800138000") {
		t.Fatalf("response placeholders not restored: %s", out)
	}
}

func TestMiddlewareBlockWithoutDetector(t *testing.T) {
	handler := Middleware(Config{Mode: ModeBlock, Holder: NewHolder(NewEngine(DefaultRules()))},
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Fatal("should not forward") }))
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "masking_unavailable") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestMiddlewarePassThrough(t *testing.T) {
	var forwarded string
	handler := Middleware(Config{Mode: ModePassThrough},
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			forwarded = string(body)
		}))
	body := `{"content":"13800138000"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if forwarded != body {
		t.Fatalf("pass-through changed body: %s", forwarded)
	}
	if rec.Header().Get("X-Masking") != "off" {
		t.Fatalf("X-Masking = %q", rec.Header().Get("X-Masking"))
	}
}

func TestTransformResponseOversizedPassesThroughIntact(t *testing.T) {
	// 超过 8MiB 的响应放弃改写但必须原样透传：
	// 旧实现把已读前缀当完整 body 挂回，客户端拿到被静默截断的 200。
	engine := NewEngine(DefaultRules())
	payload := `{"data":"` + strings.Repeat("x", reqbody.MaxBytes) + `","tail":"end"}`
	resp := &http.Response{
		StatusCode:    http.StatusOK,
		Header:        http.Header{"Content-Type": []string{"application/json"}},
		ContentLength: int64(len(payload)),
		Body:          io.NopCloser(strings.NewReader(payload)),
	}
	if err := transformResponse(resp, engine, NewMapper()); err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != len(payload) {
		t.Fatalf("oversized response truncated: got %d bytes, want %d", len(out), len(payload))
	}
	if string(out) != payload {
		t.Fatal("oversized response content changed")
	}
	if resp.ContentLength != int64(len(payload)) {
		t.Fatalf("ContentLength = %d, want %d", resp.ContentLength, len(payload))
	}
}

func TestTransformResponseSanitizesErrors(t *testing.T) {
	engine := NewEngine(DefaultRules())
	fakeSecret := "sk-" + strings.Repeat("a", 24)
	resp := &http.Response{
		StatusCode: http.StatusBadGateway,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":"` + fakeSecret + `"}`)),
	}
	if err := transformResponse(resp, engine, NewMapper()); err != nil {
		t.Fatal(err)
	}
	out, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(out), fakeSecret) {
		t.Fatalf("error body leaked secret: %s", out)
	}
}
