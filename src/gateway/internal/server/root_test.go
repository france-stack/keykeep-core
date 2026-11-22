package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestRootHandlerRoutesLocalPaths(t *testing.T) {
	sdk, _ := url.Parse("http://127.0.0.1:1")
	_ = sdk
	local := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(299) })
	forward := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(298) })
	root := NewRootHandler("test", local, func(path string) bool { return path == "/v1/limit" }, forward)

	for path, want := range map[string]int{"/v1/limit": 299, "/v1/messages": 298} {
		rec := httptest.NewRecorder()
		root.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != want {
			t.Fatalf("%s = %d, want %d", path, rec.Code, want)
		}
	}

	rec := httptest.NewRecorder()
	root.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz = %d", rec.Code)
	}
}
