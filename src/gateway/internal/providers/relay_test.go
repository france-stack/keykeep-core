package providers

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// 计数器串行化 reload 调用；Relay 的 reload 只会在 Run 的 select 循环里触发，
// 本测试不开 Run（直接调 reloadOnce / EnsureKey），因此加锁防竞争即可。
type countingReload struct {
	mu    sync.Mutex
	calls int
	keys  []string
	err   error
}

func (c *countingReload) reload(ctx context.Context) ([]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if c.err != nil {
		return nil, c.err
	}
	return c.keys, nil
}

func (c *countingReload) setKeys(keys []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.keys = keys
}

// 新键不在访问列表时，EnsureKey 必须触发重载并等到键就绪——账本归属依赖它。
func TestRelayEnsureKeyReloadsAndWaitForNewKey(t *testing.T) {
	fake := &countingReload{keys: []string{"ik_old"}}
	relay := NewRelay(fake.reload, time.Hour) // 不靠轮询，只靠 kick
	relay.SeedAccessKeys([]string{"ik_old"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go relay.Run(ctx)

	if relay.HasKey("ik_new") {
		t.Fatalf("new key should not be in the access snapshot yet")
	}
	fake.setKeys([]string{"ik_old", "ik_new"})
	done := make(chan bool, 1)
	go func() { done <- relay.EnsureKey(ctx, "ik_new", 3*time.Second) }()
	if !<-done {
		t.Fatalf("EnsureKey should succeed once the reload lands the key")
	}
	if !relay.HasKey("ik_new") {
		t.Fatalf("snapshot should contain the key after EnsureKey")
	}
	if fake.calls == 0 {
		t.Fatalf("EnsureKey should have triggered at least one reload")
	}
}

// 等待超时必须返回 false 而不是永远阻塞——可用性优先于归属。
func TestRelayEnsureKeyTimesOutWhenKeyNeverArrives(t *testing.T) {
	fake := &countingReload{keys: []string{"ik_old"}}
	relay := NewRelay(fake.reload, time.Hour)
	relay.SeedAccessKeys([]string{"ik_old"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go relay.Run(ctx)

	if relay.EnsureKey(ctx, "ik_missing", 150*time.Millisecond) {
		t.Fatalf("EnsureKey should time out for a key that never lands")
	}
}

// 已在列表里的键必须走快路径，不触发重载。
func TestRelayEnsureKeyFastPath(t *testing.T) {
	fake := &countingReload{keys: []string{"ik_old"}}
	relay := NewRelay(fake.reload, time.Hour)
	relay.SeedAccessKeys([]string{"ik_old"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go relay.Run(ctx)

	before := fake.calls
	if !relay.EnsureKey(ctx, "ik_old", time.Second) {
		t.Fatalf("existing key should hit the fast path")
	}
	if fake.calls != before {
		t.Fatalf("fast path should not trigger reload: before=%d after=%d", before, fake.calls)
	}
}

// reload 失败时保留旧快照，EnsureKey 超时返回 false，绝不 panic。
func TestRelayEnsureKeySurvivesReloadFailure(t *testing.T) {
	fake := &countingReload{err: errors.New("db down")}
	relay := NewRelay(fake.reload, time.Hour)
	relay.SeedAccessKeys([]string{"ik_old"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go relay.Run(ctx)

	if relay.EnsureKey(ctx, "ik_any", 150*time.Millisecond) {
		t.Fatalf("EnsureKey should fail when reload keeps failing")
	}
	if relay.HasKey("ik_any") {
		t.Fatalf("failed reload must not poison the snapshot")
	}
}
