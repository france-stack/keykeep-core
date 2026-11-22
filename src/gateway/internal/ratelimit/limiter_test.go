package ratelimit

import (
	"context"
	"testing"
	"time"
)

var ctx = context.Background()

func newTestLimiter(now time.Time) *Limiter {
	l := NewLimiter()
	l.now = func() time.Time { return now }
	return l
}

func TestRPMDeniesAfterLimit(t *testing.T) {
	start := time.Unix(1_700_000_000, 0)
	l := newTestLimiter(start)
	cfg := Config{RPM: 10}

	for i := 0; i < 10; i++ {
		res, denial, _ := l.Admit(ctx, "k1", cfg, 0, 0)
		if denial != nil {
			t.Fatalf("request %d denied: %+v", i+1, denial)
		}
		res.Release()
	}

	res, denial, _ := l.Admit(ctx, "k1", cfg, 0, 0)
	if denial == nil || denial.Dim != "rpm" {
		t.Fatalf("11th request not denied by rpm: res=%v denial=%+v", res, denial)
	}
	if denial.RetryAfter < 1 || denial.RetryAfter > WindowSeconds {
		t.Fatalf("retryAfter = %d, want 1..%d", denial.RetryAfter, WindowSeconds)
	}

	l.now = func() time.Time { return start.Add(61 * time.Second) }
	if _, denial, _ := l.Admit(ctx, "k1", cfg, 0, 0); denial != nil {
		t.Fatalf("after window: %+v", denial)
	}
}

func TestRPMIsPerKey(t *testing.T) {
	l := newTestLimiter(time.Unix(1_700_000_000, 0))
	cfg := Config{RPM: 1}
	if _, denial, _ := l.Admit(ctx, "k1", cfg, 0, 0); denial != nil {
		t.Fatalf("k1 first: %+v", denial)
	}
	if _, denial, _ := l.Admit(ctx, "k2", cfg, 0, 0); denial != nil {
		t.Fatalf("k2 first: %+v", denial)
	}
	if _, denial, _ := l.Admit(ctx, "k1", cfg, 0, 0); denial == nil {
		t.Fatal("k1 second should be denied")
	}
}

func TestTPMDeniesWhenTokensExceeded(t *testing.T) {
	l := newTestLimiter(time.Unix(1_700_000_000, 0))
	cfg := Config{TPM: 1000}
	if _, denial, _ := l.Admit(ctx, "k1", cfg, 600, 300); denial != nil {
		t.Fatalf("first: %+v", denial)
	}
	_, denial, _ := l.Admit(ctx, "k1", cfg, 200, 0)
	if denial == nil || denial.Dim != "tpm" {
		t.Fatalf("second should exceed tpm: %+v", denial)
	}
}

func TestITPMAndOTPMTriggerIndependently(t *testing.T) {
	l := newTestLimiter(time.Unix(1_700_000_000, 0))
	cfg := Config{ITPM: 100, OTPM: 100}

	if _, denial, _ := l.Admit(ctx, "k1", cfg, 90, 10); denial != nil {
		t.Fatalf("first: %+v", denial)
	}
	if _, denial, _ := l.Admit(ctx, "k1", cfg, 20, 0); denial == nil || denial.Dim != "itpm" {
		t.Fatalf("input side should trigger itpm: %+v", denial)
	}
	if _, denial, _ := l.Admit(ctx, "k1", cfg, 0, 95); denial == nil || denial.Dim != "otpm" {
		t.Fatalf("output side should trigger otpm: %+v", denial)
	}
}

func TestReleaseReturnsEstimate(t *testing.T) {
	l := newTestLimiter(time.Unix(1_700_000_000, 0))
	cfg := Config{ITPM: 500}
	res, denial, _ := l.Admit(ctx, "k1", cfg, 500, 0)
	if denial != nil {
		t.Fatalf("first: %+v", denial)
	}
	if _, denial, _ := l.Admit(ctx, "k1", cfg, 500, 0); denial == nil {
		t.Fatal("should be denied while estimate is held")
	}
	res.Release()
	if _, denial, _ := l.Admit(ctx, "k1", cfg, 500, 0); denial != nil {
		t.Fatalf("after release: %+v", denial)
	}
}

func TestAddActualCountsSettledUsage(t *testing.T) {
	l := newTestLimiter(time.Unix(1_700_000_000, 0))
	cfg := Config{TPM: 1000}
	l.AddActual(ctx, "k1", 800, 300)
	if _, denial, _ := l.Admit(ctx, "k1", cfg, 1, 0); denial == nil || denial.Dim != "tpm" {
		t.Fatalf("actual usage should count toward tpm: %+v", denial)
	}
}

func TestSnapshot(t *testing.T) {
	l := newTestLimiter(time.Unix(1_700_000_000, 0))
	l.AddActual(ctx, "k1", 10, 5)
	rpm, in, out, _ := l.Snapshot(ctx, "k1")
	if rpm != 0 || in != 10 || out != 5 {
		t.Fatalf("snapshot = %d/%d/%d", rpm, in, out)
	}
}
