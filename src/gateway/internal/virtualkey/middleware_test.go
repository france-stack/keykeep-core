package virtualkey

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeStore struct {
	info *KeyInfo
	err  error
}

func (f fakeStore) Lookup(_ context.Context, _ []byte) (*KeyInfo, error) {
	return f.info, f.err
}

var validKey = "kk_" + strings.Repeat("a", 43)

func TestMiddlewareAcceptsValidKey(t *testing.T) {
	dept := "dept-1"
	store := fakeStore{info: &KeyInfo{ID: "key-1", EmpID: "emp-1", DeptID: &dept}}
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if got := r.Header.Get("Authorization"); got != "Bearer ik_key-1" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("X-KeyKeep-Emp-Id"); got != "emp-1" {
			t.Errorf("emp header = %q", got)
		}
		if got := r.Header.Get("X-KeyKeep-Dept-Id"); got != "dept-1" {
			t.Errorf("dept header = %q", got)
		}
		if info, ok := FromContext(r.Context()); !ok || info.ID != "key-1" {
			t.Errorf("context info = %#v, %v", info, ok)
		}
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+validKey)
	rec := httptest.NewRecorder()
	Middleware(store, "ik_", nil, next).ServeHTTP(rec, req)

	if !called || rec.Code != http.StatusOK {
		t.Fatalf("called=%v status=%d", called, rec.Code)
	}
}

// 客户端凭据头不允许穿透到 SDK / 上游：留在请求里等于把员工凭据泄露给供应商。
func TestMiddlewareStripsCredentialHeaders(t *testing.T) {
	store := fakeStore{info: &KeyInfo{ID: "key-1", EmpID: "emp-1"}}
	var saw map[string]string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		saw = map[string]string{
			"x-goog-api-key":      r.Header.Get("X-Goog-Api-Key"),
			"proxy-authorization": r.Header.Get("Proxy-Authorization"),
			"cookie":              r.Header.Get("Cookie"),
			"x-api-key":           r.Header.Get("X-Api-Key"),
		}
		w.WriteHeader(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+validKey)
	req.Header.Set("X-Goog-Api-Key", "kk_leaky")
	req.Header.Set("Proxy-Authorization", "Basic c2VjcmV0")
	req.Header.Set("Cookie", "session=secret")
	req.Header.Set("x-api-key", validKey)
	rec := httptest.NewRecorder()
	Middleware(store, "ik_", nil, next).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	for name, got := range saw {
		if got != "" {
			t.Errorf("%s leaked to downstream: %q", name, got)
		}
	}
}

// Anthropic 官方 SDK / Claude Code 默认发 x-api-key；员工照官方文档配置时必须能过。
func TestMiddlewareAcceptsApiKeyHeader(t *testing.T) {
	store := fakeStore{info: &KeyInfo{ID: "key-1", EmpID: "emp-1"}}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer ik_key-1" {
			t.Errorf("Authorization = %q", got)
		}
		// 员工虚拟 Key 不能被转给上游。
		if got := r.Header.Get("x-api-key"); got != "" {
			t.Errorf("x-api-key must be stripped, got %q", got)
		}
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("{}"))
	req.Header.Set("x-api-key", validKey)
	rec := httptest.NewRecorder()
	Middleware(store, "ik_", nil, next).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
}

// 部分客户端把 "Bearer <key>" 原样塞进 ANTHROPIC_AUTH_TOKEN（即 x-api-key）。
func TestMiddlewareAcceptsApiKeyHeaderWithBearerPrefix(t *testing.T) {
	store := fakeStore{info: &KeyInfo{ID: "key-1", EmpID: "emp-1"}}
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("{}"))
	req.Header.Set("x-api-key", "Bearer "+validKey)
	rec := httptest.NewRecorder()
	Middleware(store, "ik_", nil, next).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
}

func TestMiddlewareRejectsMissingOrMalformedKey(t *testing.T) {
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("next must not be called")
	})
	for _, auth := range []string{"", "Bearer", "Bearer short", "Basic x"} {
		req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		rec := httptest.NewRecorder()
		Middleware(fakeStore{}, "ik_", nil, next).ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("auth=%q status=%d", auth, rec.Code)
		}
	}
}

func TestMiddlewareRejectsUnknownAndRevokedKey(t *testing.T) {
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("next must not be called")
	})
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+validKey)
	rec := httptest.NewRecorder()
	Middleware(fakeStore{info: nil}, "ik_", nil, next).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d", rec.Code)
	}
}

func TestMiddlewareStoreErrorIs503(t *testing.T) {
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("next must not be called")
	})
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+validKey)
	rec := httptest.NewRecorder()
	Middleware(fakeStore{err: errors.New("db down")}, "ik_", nil, next).ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d", rec.Code)
	}
}

func TestMiddlewareSkipsHealth(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	rec := httptest.NewRecorder()
	Middleware(fakeStore{}, "ik_", nil, next).ServeHTTP(
		rec,
		httptest.NewRequest(http.MethodGet, "/healthz", nil),
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
}

type recordingAuditor struct {
	actions []string
	details []map[string]any
}

func (r *recordingAuditor) Record(_ context.Context, action, _ string, detail map[string]any, _ string) {
	r.actions = append(r.actions, action)
	r.details = append(r.details, detail)
}

func TestMiddlewareAuditsAuthFailure(t *testing.T) {
	auditor := &recordingAuditor{}
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("next must not be called")
	})
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+validKey)
	rec := httptest.NewRecorder()
	Middleware(fakeStore{}, "ik_", auditor, next).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d", rec.Code)
	}
	if len(auditor.actions) != 1 || auditor.actions[0] != "key.auth_failed" {
		t.Fatalf("actions=%v", auditor.actions)
	}
	if auditor.details[0]["reason"] != "unknown_or_revoked" {
		t.Fatalf("detail=%v", auditor.details[0])
	}
}

func TestIsKeyFormat(t *testing.T) {
	if !IsKeyFormat(validKey) {
		t.Fatal("valid key rejected")
	}
	for _, bad := range []string{"", "kk_short", "sk_" + strings.Repeat("a", 43), "kk_" + strings.Repeat("!", 43)} {
		if IsKeyFormat(bad) {
			t.Fatalf("bad key accepted: %q", bad)
		}
	}
}

// 缺陷回归（P1）：EnsureAccess 必须把 keyPrefix+ID 交给 ensure——与转发头
// "Bearer "+keyPrefix+info.ID 同口径。传裸 UUID 时 providers.Relay.HasKey 永远
// false，EnsureKey 的快路径打不中，每请求白等 KEYKEEP_ENSURE_KEY_WAIT（默认 3s）。
func TestEnsureAccessPassesPrefixedKeyID(t *testing.T) {
	store := fakeStore{info: &KeyInfo{ID: "key-1", EmpID: "emp-1"}}
	newRequest := func() *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("{}"))
		req.Header.Set("Authorization", "Bearer "+validKey)
		return req
	}

	t.Run("prefixed key id reaches ensure", func(t *testing.T) {
		var ensureIDs []string
		nextCalled := false
		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			nextCalled = true
			// 端到端同口径：ensure 收到的键必须与转发头里的键逐字相同。
			if len(ensureIDs) != 1 {
				t.Errorf("ensure calls = %d", len(ensureIDs))
			} else if got, want := r.Header.Get("Authorization"), "Bearer "+ensureIDs[0]; got != want {
				t.Errorf("Authorization = %q, want %q", got, want)
			}
			w.WriteHeader(http.StatusOK)
		})
		ensure := func(_ context.Context, keyID string) bool {
			ensureIDs = append(ensureIDs, keyID)
			return true
		}

		rec := httptest.NewRecorder()
		Middleware(store, "ik_", nil, EnsureAccess("ik_", ensure, next)).ServeHTTP(rec, newRequest())

		if len(ensureIDs) != 1 || ensureIDs[0] != "ik_key-1" {
			t.Fatalf("ensure keyIDs = %v, want [ik_key-1] (bare %q is the P1 bug)", ensureIDs, "key-1")
		}
		if !nextCalled || rec.Code != http.StatusOK {
			t.Fatalf("next called=%v status=%d, want true/200", nextCalled, rec.Code)
		}
	})

	t.Run("nil ensure passes through", func(t *testing.T) {
		nextCalled := false
		next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			nextCalled = true
			w.WriteHeader(http.StatusOK)
		})
		rec := httptest.NewRecorder()
		Middleware(store, "ik_", nil, EnsureAccess("ik_", nil, next)).ServeHTTP(rec, newRequest())

		if !nextCalled || rec.Code != http.StatusOK {
			t.Fatalf("next called=%v status=%d, want true/200", nextCalled, rec.Code)
		}
	})

	t.Run("ensure false does not block forwarding", func(t *testing.T) {
		ensureCalled := false
		nextCalled := false
		next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			nextCalled = true
			w.WriteHeader(http.StatusOK)
		})
		ensure := func(_ context.Context, _ string) bool {
			ensureCalled = true
			return false
		}
		rec := httptest.NewRecorder()
		Middleware(store, "ik_", nil, EnsureAccess("ik_", ensure, next)).ServeHTTP(rec, newRequest())

		if !ensureCalled {
			t.Fatal("ensure not called")
		}
		// 可用性优先：访问列表未就绪只记日志，不能把请求打回。
		if !nextCalled || rec.Code != http.StatusOK {
			t.Fatalf("next called=%v status=%d, want true/200", nextCalled, rec.Code)
		}
	})
}
