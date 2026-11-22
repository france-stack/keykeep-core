package accesslog

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/keykeep/gateway/internal/virtualkey"
)

func TestLogsStatusAndBytes(t *testing.T) {
	handler := Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("hello"))
	}))
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req = req.WithContext(virtualkey.WithKeyInfo(req.Context(),
		&virtualkey.KeyInfo{ID: "key-1", EmpID: "emp-1"}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusTeapot || rec.Body.String() != "hello" {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestHealthzBypassesLoggingButStillServes(t *testing.T) {
	called := false
	handler := Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if !called || rec.Code != http.StatusOK {
		t.Fatalf("called=%v status=%d", called, rec.Code)
	}
}
