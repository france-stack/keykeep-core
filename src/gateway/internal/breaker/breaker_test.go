package breaker

import (
	"context"
	"net/http"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth" // secret-scan:ignore high-entropy-string 公开 Go 模块路径，非凭据
)

// fakeClock 是可手动推进的时钟。
type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time          { return c.now }
func (c *fakeClock) Advance(d time.Duration) { c.now = c.now.Add(d) }
func newFakeClock() *fakeClock               { return &fakeClock{now: time.Unix(1_700_000_000, 0)} }

func newMachine(clock *fakeClock) *Machine {
	return New(Options{FailureThreshold: 3, BaseCooldown: 5 * time.Second,
		MaxCooldown: 40 * time.Second, Now: clock.Now})
}

// FA-26：三态迁移闭合 → 打开 → 半开 → 闭合，全链路可取证。
func TestThreeStateTransitionsAreRecorded(t *testing.T) {
	clock := newFakeClock()
	m := newMachine(clock)
	key := "claude:apikey:abc"

	if !m.Allow(key) {
		t.Fatal("fresh key must be allowed")
	}
	m.Observe(key, false)
	m.Observe(key, false)
	if m.Open(key) {
		t.Fatal("below threshold must stay closed")
	}
	m.Observe(key, false)
	if !m.Open(key) {
		t.Fatal("threshold reached must open")
	}
	if m.Allow(key) {
		t.Fatal("open key must be blocked")
	}

	clock.Advance(5 * time.Second)
	if !m.Allow(key) {
		t.Fatal("cooldown elapsed must move to half-open and allow")
	}
	m.Observe(key, true)
	if m.Open(key) {
		t.Fatal("successful probe must close")
	}

	tr := m.Transitions()
	want := []struct{ from, to State }{
		{StateClosed, StateOpen},
		{StateOpen, StateHalfOpen},
		{StateHalfOpen, StateClosed},
	}
	if len(tr) != len(want) {
		t.Fatalf("got %d transitions, want %d: %+v", len(tr), len(want), tr)
	}
	for i, w := range want {
		if tr[i].From != w.from || tr[i].To != w.to {
			t.Fatalf("transition %d = %s→%s, want %s→%s", i, tr[i].From, tr[i].To, w.from, w.to)
		}
	}
	if tr[0].Cooldown != 5 {
		t.Fatalf("first cooldown = %ds, want 5s", tr[0].Cooldown)
	}
}

// FA-26：半开探测失败时冷却指数增长（5→10→20→40 封顶）。
func TestHalfOpenFailureDoublesCooldown(t *testing.T) {
	clock := newFakeClock()
	m := newMachine(clock)
	key := "k"

	wantCooldowns := []int{5, 10, 20, 40, 40}
	for round, want := range wantCooldowns {
		// 失败到打开。
		for i := 0; i < 3; i++ {
			m.Observe(key, false)
		}
		if !m.Open(key) {
			t.Fatalf("round %d: expected open", round)
		}
		tr := m.Transitions()
		lastOpen := tr[len(tr)-1]
		if lastOpen.Cooldown != want {
			t.Fatalf("round %d: cooldown = %d, want %d", round, lastOpen.Cooldown, want)
		}
		// 冷却到期 → 半开 → 探测失败 → 再打开（翻倍）。
		clock.Advance(time.Duration(want) * time.Second)
		_ = m.Allow(key)
		m.Observe(key, false)
	}
}

// 成功即清空失败计数：闭合态下偶发失败不应累积到跨界。
func TestSuccessResetsFailureCount(t *testing.T) {
	clock := newFakeClock()
	m := newMachine(clock)
	key := "k"
	m.Observe(key, false)
	m.Observe(key, false)
	m.Observe(key, true)
	m.Observe(key, false)
	m.Observe(key, false)
	if m.Open(key) {
		t.Fatal("must not open: failures were not consecutive")
	}
}

// FilterOpen 保持原顺序并返回被剔除项。
func TestFilterOpenKeepsOrder(t *testing.T) {
	clock := newFakeClock()
	m := newMachine(clock)
	open := "claude:apikey:open"
	for i := 0; i < 3; i++ {
		m.Observe(open, false)
	}
	ids := []string{"a", open, "b"}
	usable, blocked := m.FilterOpen(ids)
	if len(usable) != 2 || usable[0] != "a" || usable[1] != "b" {
		t.Fatalf("usable = %v, want [a b]", usable)
	}
	if len(blocked) != 1 || blocked[0] != open {
		t.Fatalf("blocked = %v", blocked)
	}
}

// 结果策略只把凭据侧故障计入健康度。
func TestPolicyCountsOnlyCredentialFailures(t *testing.T) {
	clock := newFakeClock()
	m := newMachine(clock)
	policy := NewPolicy(m)
	if policy == nil {
		t.Fatal("policy must not be nil")
	}

	clientErr := coreauth.Result{AuthID: "k", Success: false,
		Error: &coreauth.Error{HTTPStatus: http.StatusBadRequest, Code: "invalid_request"}}
	for i := 0; i < 5; i++ {
		policy.ApplyResultPolicy(context.Background(), clientErr)
	}
	if m.Open("k") {
		t.Fatal("4xx client errors must not open the breaker")
	}

	upstreamErr := coreauth.Result{AuthID: "k", Success: false,
		Error: &coreauth.Error{HTTPStatus: http.StatusInternalServerError, Code: "upstream_error"}}
	for i := 0; i < 3; i++ {
		policy.ApplyResultPolicy(context.Background(), upstreamErr)
	}
	if !m.Open("k") {
		t.Fatal("upstream 500s must open the breaker")
	}
}

// CredentialID 与 SDK 的 StableIDGenerator 同算法（字段 + 分隔符一致）。
func TestCredentialIDIsStableAndPrefixed(t *testing.T) {
	a := CredentialID("sk-1", "https://api.example.com")
	b := CredentialID("sk-1", "https://api.example.com")
	if a != b {
		t.Fatal("CredentialID must be deterministic")
	}
	if len(a) != len("claude:apikey:")+12 || a[:14] != "claude:apikey:" {
		t.Fatalf("CredentialID shape = %q", a)
	}
	if a == CredentialID("sk-2", "https://api.example.com") {
		t.Fatal("different keys must produce different IDs")
	}
}

// nil 状态机全部放行（无数据库形态 / 熔断关闭）。
func TestNilMachineIsSafe(t *testing.T) {
	var m *Machine
	if !m.Allow("k") || m.Open("k") {
		t.Fatal("nil machine must allow everything")
	}
	m.Observe("k", false)
	if len(m.Snapshot()) != 0 || len(m.Transitions()) != 0 {
		t.Fatal("nil machine must have empty views")
	}
	m.Reset()
}

// 迁移日志有上限，不会无限增长。
func TestTransitionLogIsBounded(t *testing.T) {
	clock := newFakeClock()
	m := New(Options{FailureThreshold: 1, BaseCooldown: time.Second, Now: clock.Now})
	m.maxTransitions = 10
	for i := 0; i < 30; i++ {
		m.Observe("k", false)
		clock.Advance(time.Second)
		m.Allow("k")
		m.Observe("k", true)
	}
	if n := len(m.Transitions()); n > 10 {
		t.Fatalf("transitions kept = %d, want <= 10", n)
	}
}
