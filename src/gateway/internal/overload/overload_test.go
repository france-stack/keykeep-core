package overload

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/keykeep/gateway/internal/ratelimit"
	"github.com/keykeep/gateway/internal/virtualkey"
)

func TestAcquireAndRelease(t *testing.T) {
	g := New(Config{MaxConcurrency: 1, QueueSize: 0, QueueTimeout: 20 * time.Millisecond})
	release, waited, err := g.Acquire(context.Background())
	if err != nil || waited != 0 {
		t.Fatalf("first acquire: %v waited=%s", err, waited)
	}

	if _, _, err := g.Acquire(context.Background()); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("second acquire = %v, want queue full", err)
	}

	release()
	release() // 幂等
	release2, _, err := g.Acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	release2()
}

func TestQueueTimeout(t *testing.T) {
	g := New(Config{MaxConcurrency: 1, QueueSize: 2, QueueTimeout: 30 * time.Millisecond})
	release, _, err := g.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	start := time.Now()
	if _, _, err := g.Acquire(context.Background()); !errors.Is(err, ErrQueueTimeout) {
		t.Fatalf("err = %v, want timeout", err)
	}
	if elapsed := time.Since(start); elapsed < 20*time.Millisecond {
		t.Fatalf("returned too early: %s", elapsed)
	}
}

func TestWaiterGetsSlotWhenReleased(t *testing.T) {
	g := New(Config{MaxConcurrency: 1, QueueSize: 1, QueueTimeout: 2 * time.Second})
	release, _, err := g.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		_, _, err := g.Acquire(context.Background())
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	release()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("waiter: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("waiter did not receive slot")
	}
	if stats := g.Snapshot(); stats.Admitted < 2 {
		t.Fatalf("admitted = %d", stats.Admitted)
	}
}

func TestCloseWakesWaiters(t *testing.T) {
	g := New(Config{MaxConcurrency: 1, QueueSize: 2, QueueTimeout: 5 * time.Second})
	release, _, err := g.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	done := make(chan error, 1)
	go func() {
		_, _, err := g.Acquire(context.Background())
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	g.Close()

	select {
	case err := <-done:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("waiter err = %v, want closed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("waiter not woken")
	}
}

func TestKeyedConcurrencyAndIsolation(t *testing.T) {
	k := NewKeyed()
	release, ok := k.Acquire("a", 1)
	if !ok {
		t.Fatal("first key slot denied")
	}
	if _, ok := k.Acquire("a", 1); ok {
		t.Fatal("second key slot should be denied")
	}
	other, ok := k.Acquire("b", 1)
	if !ok {
		t.Fatal("other key should not be affected")
	}
	other()
	release()
	if r, ok := k.Acquire("a", 1); !ok {
		t.Fatal("released key slot should be reusable")
	} else {
		r()
	}
	if r, ok := k.Acquire("unlimited", 0); !ok {
		t.Fatal("zero limit should be unlimited")
	} else {
		r()
	}
}

func TestKeyedReplacesChangedLimitWhenIdle(t *testing.T) {
	k := NewKeyed()
	release, ok := k.Acquire("a", 1)
	if !ok {
		t.Fatal("initial slot denied")
	}
	release()
	if _, ok := k.Acquire("a", 2); !ok {
		t.Fatal("limit change should take effect when idle")
	}
	if _, ok := k.Acquire("a", 2); !ok {
		t.Fatal("second slot under new limit should pass")
	}
	if _, ok := k.Acquire("a", 2); ok {
		t.Fatal("third slot should be denied under limit 2")
	}
}

type fakeLimits struct{ cfg ratelimit.Config }

func (f fakeLimits) Get(context.Context, string) (ratelimit.Config, error) { return f.cfg, nil }

type recordingAuditor struct {
	actions []string
}

func (r *recordingAuditor) Record(_ context.Context, action, _ string, _ map[string]any, _ string) {
	r.actions = append(r.actions, action)
}

func TestMiddlewareRejectsWhenQueueFull(t *testing.T) {
	g := New(Config{MaxConcurrency: 1, QueueSize: 0, QueueTimeout: time.Second})
	held, _, err := g.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer held()

	auditor := &recordingAuditor{}
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("next must not be called")
	})
	handler := Middleware(g, NewKeyed(), fakeLimits{}, auditor, next)

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("{}"))
	req = req.WithContext(virtualkey.WithKeyInfo(req.Context(), &virtualkey.KeyInfo{ID: "key-1", EmpID: "emp-1"}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("missing Retry-After")
	}
	body := rec.Body.String()
	for _, want := range []string{"overloaded", "queue_full"} {
		if !strings.Contains(body, want) {
			t.Fatalf("body %q missing %q", body, want)
		}
	}
	if len(auditor.actions) != 1 || auditor.actions[0] != "key.overloaded" {
		t.Fatalf("audit actions = %v", auditor.actions)
	}
}

func TestMiddlewareRejectsPerKeyConcurrency(t *testing.T) {
	g := New(Config{MaxConcurrency: 0})
	keyed := NewKeyed()
	handler := Middleware(g, keyed, fakeLimits{cfg: ratelimit.Config{Concurrency: 1}}, nil,
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))

	// 先占住该 Key 的唯一并发额度。
	release, ok := keyed.Acquire("key-1", 1)
	if !ok {
		t.Fatal("setup acquire failed")
	}
	defer release()

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("{}"))
	req = req.WithContext(virtualkey.WithKeyInfo(req.Context(), &virtualkey.KeyInfo{ID: "key-1"}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "key_concurrency") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}
