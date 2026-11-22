package reqbody

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMiddlewareCachesAndRestoresBody(t *testing.T) {
	payload := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`
	var first, second []byte
	var firstTooLarge, secondTooLarge bool

	handler := Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		first, firstTooLarge = Read(r)
		second, secondTooLarge = Read(r)
		restored, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read restored body: %v", err)
		}
		if string(restored) != payload {
			t.Fatalf("body not restored: %q", restored)
		}
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(payload))
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if firstTooLarge || secondTooLarge {
		t.Fatal("unexpected tooLarge")
	}
	if string(first) != payload || string(second) != payload {
		t.Fatalf("cached body mismatch: %q / %q", first, second)
	}
}

func TestMiddlewareRejectsDeclaredOversize(t *testing.T) {
	// 超限请求在最外层直接 413（fail-close）：请求体上限是数据面统一边界，
	// 不随"该 Key 是否配了白名单"变化。
	reached := false
	handler := Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
	}))
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("{}"))
	req.ContentLength = MaxBytes + 1
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if reached {
		t.Fatal("oversized request must not reach downstream")
	}
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "request_too_large") {
		t.Fatalf("error body missing code: %q", rec.Body.String())
	}
}

func TestMiddlewareRejectsActualOversize(t *testing.T) {
	// ContentLength 未知（-1）但实际超限时同样 413。
	reached := false
	handler := Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
	}))
	req := httptest.NewRequest(http.MethodPost, "/v1/messages",
		strings.NewReader(strings.Repeat("x", MaxBytes+1)))
	req.ContentLength = -1
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if reached {
		t.Fatal("oversized body must not reach downstream")
	}
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
}

func TestReadFallsBackWithoutMiddleware(t *testing.T) {
	payload := `{"model":"m"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(payload))
	body, tooLarge := Read(req)
	if tooLarge || string(body) != payload {
		t.Fatalf("fallback read = %q tooLarge=%v", body, tooLarge)
	}
	restored, _ := io.ReadAll(req.Body)
	if string(restored) != payload {
		t.Fatalf("fallback did not restore body: %q", restored)
	}
}

func TestGetIsNotRead(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	if body, tooLarge := Read(req); body != nil || tooLarge {
		t.Fatalf("GET should not read body: %q %v", body, tooLarge)
	}
}
