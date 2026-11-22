package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestHealthHandler(t *testing.T) {
	target, _ := url.Parse("http://127.0.0.1:1")
	handler := NewHandler(target, "0.1.0-test")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("health status = %d, want 200", rec.Code)
	}
	if body := rec.Body.String(); body != "{\"status\":\"ok\",\"version\":\"0.1.0-test\"}\n" {
		t.Fatalf("health body = %q", body)
	}
}

func TestProxyForwardsToSDK(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[]}`)
	}))
	defer upstream.Close()

	target, _ := url.Parse(upstream.URL)
	handler := NewHandler(target, "test")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("proxy status = %d", rec.Code)
	}
	if body := rec.Body.String(); body != `{"data":[]}` {
		t.Fatalf("proxy body = %q", body)
	}
}

func TestHealthRejectsPost(t *testing.T) {
	target, _ := url.Parse("http://127.0.0.1:1")
	handler := NewHandler(target, "test")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/healthz", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}
