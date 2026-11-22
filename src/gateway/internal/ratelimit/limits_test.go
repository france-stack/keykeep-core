package ratelimit

import (
	"testing"
	"time"
)

func TestTPMRetryAfterUsesBothTokenBuckets(t *testing.T) {
	start := time.Unix(1_700_000_000, 0)
	l := newTestLimiter(start)
	cfg := Config{TPM: 1000}

	// 输入计数先写入；30 秒后只有输出计数。
	_ = l.AddActual(ctx, "k1", 100, 0)
	l.now = func() time.Time { return start.Add(30 * time.Second) }
	_ = l.AddActual(ctx, "k1", 0, 900)

	_, denial, _ := l.Admit(ctx, "k1", cfg, 200, 0)
	if denial == nil || denial.Dim != "tpm" {
		t.Fatalf("expected tpm denial: %+v", denial)
	}
	// 输出桶 30 秒后过期（最早腾出空间），输入桶还要 60 秒。
	if denial.RetryAfter != 30 {
		t.Fatalf("retryAfter = %d, want 30 (output bucket)", denial.RetryAfter)
	}
}

func TestTPMRetryAfterWhenOnlyOutputCounts(t *testing.T) {
	start := time.Unix(1_700_000_000, 0)
	l := newTestLimiter(start)
	cfg := Config{TPM: 100}
	_ = l.AddActual(ctx, "k1", 0, 90)

	_, denial, _ := l.Admit(ctx, "k1", cfg, 0, 50)
	if denial == nil || denial.Dim != "tpm" {
		t.Fatalf("expected tpm denial: %+v", denial)
	}
	if denial.RetryAfter != 60 {
		t.Fatalf("retryAfter = %d, want 60", denial.RetryAfter)
	}
}

func TestIdleKeysAreEvicted(t *testing.T) {
	start := time.Unix(1_700_000_000, 0)
	l := newTestLimiter(start)
	_, _, _ = l.Admit(ctx, "old", Config{RPM: 10}, 0, 0)
	// 分片后清扫只发生在被访问 Key 所在的分片：找一个与 old 同分片的 Key。
	peer := ""
	for i := 0; i < 1000; i++ {
		candidate := "peer-" + string(rune('a'+i%26)) + string(rune('a'+i/26))
		if l.shardFor(candidate) == l.shardFor("old") {
			peer = candidate
			break
		}
	}
	if peer == "" {
		t.Fatal("no same-shard peer found")
	}
	_, _, _ = l.Admit(ctx, peer, Config{RPM: 10}, 0, 0)

	// 推进两个窗口后再访问同分片的 Key，触发清扫。
	l.now = func() time.Time { return start.Add(3 * WindowSeconds * time.Second) }
	_, _, _ = l.Admit(ctx, peer, Config{RPM: 10}, 0, 0)

	sh := l.shardFor("old")
	sh.mu.Lock()
	_, hasOld := sh.counters["old"]
	_, seenOld := sh.lastSeen["old"]
	sh.mu.Unlock()
	if hasOld || seenOld {
		t.Fatal("idle key should be evicted")
	}
}
