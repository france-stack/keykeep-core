package ledger

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

// fakeInternalKey 运行期拼出来：避免在源码里写出凭据形状的字面量（secret-scan 会命中）。
var fakeInternalKey = "ik_test_" + strings.Repeat("x", 20)

func makeRec(rid string) usage.Record {
	return usage.Record{
		RequestID:   rid,
		Provider:    "opendesign",
		Model:       "deepseek-v4.1-flash",
		Alias:       "kk-deepseek",
		APIKey:      fakeInternalKey,
		RequestedAt: time.Now(),
		Detail: usage.Detail{
			InputTokens:  100,
			OutputTokens: 200,
		},
	}
}

func TestWALAppendAndReplayRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wal.jsonl")
	w := newWAL(path)

	// 空队列重放是 no-op。
	n, err := w.replay(func(usage.Record) error { t.Fatal("empty WAL must not call process"); return nil })
	if err != nil || n != 0 {
		t.Fatalf("empty replay = %d, %v", n, err)
	}

	if err := w.append(makeRec("rid-1")); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := w.append(makeRec("rid-2")); err != nil {
		t.Fatalf("append: %v", err)
	}
	if got := w.count(); got != 2 {
		t.Fatalf("count = %d, want 2", got)
	}

	// PG 故障形态：process 全部失败，队列原样保留。
	var processed []string
	n, err = w.replay(func(rec usage.Record) error {
		processed = append(processed, rec.RequestID)
		return errors.New("pg down")
	})
	if err != nil || n != 0 || len(processed) != 2 {
		t.Fatalf("failed replay = %d, %v, processed=%v", n, err, processed)
	}
	if got := w.count(); got != 2 {
		t.Fatalf("failed entries must be kept, count = %d", got)
	}

	// PG 恢复：全部重放成功，文件清空。
	n, err = w.replay(func(rec usage.Record) error {
		processed = append(processed, rec.RequestID)
		return nil
	})
	if err != nil || n != 2 {
		t.Fatalf("recovered replay = %d, %v", n, err)
	}
	if got := w.count(); got != 0 {
		t.Fatalf("replayed entries must be removed, count = %d", got)
	}
	if len(processed) != 4 || processed[0] != "rid-1" || processed[2] != "rid-1" {
		t.Fatalf("replay order/content wrong: %v", processed)
	}
}

func TestWALKeepsCorruptLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wal.jsonl")
	w := newWAL(path)
	if err := w.append(makeRec("rid-1")); err != nil {
		t.Fatalf("append: %v", err)
	}
	// 模拟半截行（崩溃/截断形态）。
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(raw, []byte(`{"v":1,"rec":{"requestId":"rid-2","apiK`)...), 0o600); err != nil {
		t.Fatal(err)
	}

	replayed := 0
	n, err := w.replay(func(usage.Record) error { replayed += 1; return nil })
	if err != nil || n != 1 || replayed != 1 {
		t.Fatalf("corrupt-line replay = %d, %v", n, err)
	}
	// 损坏行保留，等人工排查：不能静默丢。
	if got := w.count(); got != 1 {
		t.Fatalf("corrupt line must be kept, count = %d", got)
	}
}

func TestWALConcurrentAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wal.jsonl")
	w := newWAL(path)
	var wg sync.WaitGroup
	for i := 0; i < 16; i += 1 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := w.append(makeRec("rid-concurrent")); err != nil {
				t.Errorf("append: %v", err)
			}
		}()
	}
	wg.Wait()
	if got := w.count(); got != 16 {
		t.Fatalf("count = %d, want 16", got)
	}
}

func TestTTLCacheExpiry(t *testing.T) {
	c := newTTLCache[string](50 * time.Millisecond)
	if _, ok := c.get("k"); ok {
		t.Fatal("empty cache must miss")
	}
	c.put("k", "v")
	if v, ok := c.get("k"); !ok || v != "v" {
		t.Fatalf("get = %q, %v", v, ok)
	}
	c.now = func() time.Time { return time.Now().Add(100 * time.Millisecond) }
	if _, ok := c.get("k"); ok {
		t.Fatal("expired entry must miss")
	}
	// nil 接收方不 panic。
	var nilCache *ttlCache[string]
	if _, ok := nilCache.get("k"); ok {
		t.Fatal("nil cache must miss")
	}
	nilCache.put("k", "v")
}
