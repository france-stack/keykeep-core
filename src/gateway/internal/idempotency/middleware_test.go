package idempotency

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/keykeep/gateway/internal/virtualkey"
)

type fakeStore struct {
	inserted bool
	existing *Claim
	final    struct {
		status string
		code   int
		err    string
	}
}

func (f *fakeStore) Acquire(_ context.Context, c Claim) (*Claim, *Claim, error) {
	if f.inserted {
		c.ID = "claim-1"
		return &c, nil, nil
	}
	return nil, f.existing, nil
}

func (f *fakeStore) Finalize(_ context.Context, _ string, status string, code int, errCode string) error {
	f.final.status, f.final.code, f.final.err = status, code, errCode
	return nil
}

type fakeAuditor struct{ actions []string }

func (a *fakeAuditor) Record(_ context.Context, action, _ string, _ map[string]any, _ string) {
	a.actions = append(a.actions, action)
}

func request(t *testing.T, body, requestID string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	req.Header.Set("X-Request-Id", requestID)
	return req.WithContext(virtualkey.WithKeyInfo(req.Context(),
		&virtualkey.KeyInfo{ID: "key-1", EmpID: "emp-1"}))
}

func TestDuplicateCompletedIsRejected(t *testing.T) {
	store := &fakeStore{inserted: false, existing: &Claim{
		RequestID: "rid-1", PayloadHash: hashFor(t, "POST", "/v1/messages", `{"model":"m"}`),
		Status: StatusCompleted,
	}}
	auditor := &fakeAuditor{}
	called := false
	handler := Middleware(store, auditor, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, request(t, `{"model":"m"}`, "rid-1"))

	if called {
		t.Fatal("duplicate must not reach upstream")
	}
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "request_already_completed") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(auditor.actions) != 1 || auditor.actions[0] != "request.duplicate" {
		t.Fatalf("audit=%v", auditor.actions)
	}
}

func TestPayloadConflict(t *testing.T) {
	store := &fakeStore{inserted: false, existing: &Claim{
		RequestID: "rid-2", PayloadHash: "different", Status: StatusCompleted,
	}}
	handler := Middleware(store, nil, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("must not reach upstream")
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, request(t, `{"model":"m"}`, "rid-2"))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "request_id_conflict") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestSuccessFinalizesCompleted(t *testing.T) {
	store := &fakeStore{inserted: true}
	handler := Middleware(store, nil, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, request(t, `{"model":"m"}`, "rid-3"))
	if store.final.status != StatusCompleted || store.final.code != http.StatusOK {
		t.Fatalf("final=%+v", store.final)
	}
}

func TestGatewayRejectionDeletesClaim(t *testing.T) {
	store := &fakeStore{inserted: true}
	handler := Middleware(store, nil, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, request(t, `{"model":"m"}`, "rid-4"))
	if store.final.status != "" {
		t.Fatalf("claim should be deleted for retryable rejection: %+v", store.final)
	}
}

func TestForbiddenFinalizesFailed(t *testing.T) {
	store := &fakeStore{inserted: true}
	handler := Middleware(store, nil, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, request(t, `{"model":"m"}`, "rid-5"))
	if store.final.status != StatusFailed || store.final.code != http.StatusForbidden {
		t.Fatalf("final=%+v", store.final)
	}
}

func TestPanicMarksOutcomeUnknownAndRepanics(t *testing.T) {
	store := &fakeStore{inserted: true}
	handler := Middleware(store, nil, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	}))
	func() {
		defer func() {
			if recovered := recover(); recovered == nil {
				t.Fatal("panic should propagate")
			}
		}()
		handler.ServeHTTP(httptest.NewRecorder(), request(t, `{"model":"m"}`, "rid-6"))
	}()
	if store.final.status != StatusOutcomeUnknown || store.final.err != "stream_aborted" {
		t.Fatalf("final=%+v", store.final)
	}
}

func TestMissingRequestIDPassesThrough(t *testing.T) {
	store := &fakeStore{inserted: true}
	called := false
	handler := Middleware(store, nil, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("{}"))
	req = req.WithContext(virtualkey.WithKeyInfo(req.Context(), &virtualkey.KeyInfo{ID: "key-1"}))
	handler.ServeHTTP(httptest.NewRecorder(), req)
	if !called || store.final.status != "" {
		t.Fatalf("called=%v final=%+v", called, store.final)
	}
}

func hashFor(t *testing.T, method, path, body string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(method + "\n" + path + "\n" + body))
	return hex.EncodeToString(sum[:])
}
