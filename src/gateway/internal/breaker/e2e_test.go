package breaker

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth" // secret-scan:ignore high-entropy-string 公开 Go 模块路径，非凭据
)

// FA-27 的端到端语义（不依赖 SDK 进程）：
// 两个上游凭据，主凭据持续 500、备凭据正常。状态机在 3 次连续失败后把主凭据
// 剔除路由；接下来的请求全部走备凭据（10/10 成功）；冷却到期后主凭据被放回做探测，
// 探测仍失败则再次剔除并翻倍冷却。切换判定在下一次路由前完成（本次实现里
// 剔除发生在结果回调，路由侧下一次请求即生效——时延远低于 3s 阈值）。
func TestFailoverSkipsFailingCredentialUntilProbe(t *testing.T) {
	clock := newFakeClock()
	m := New(Options{FailureThreshold: 3, BaseCooldown: 5 * time.Second,
		MaxCooldown: 10 * time.Second, Now: clock.Now})
	policy := NewPolicy(m)

	primary := CredentialID("key-primary", "https://primary.example")
	backup := CredentialID("key-backup", "https://backup.example")
	creds := []string{primary, backup}

	// 两个 mock 上游：主 500，备 200。
	primarySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer primarySrv.Close()
	backupSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backupSrv.Close()

	var backupHits, primaryHits int32
	// 模拟一次请求：按熔断状态选凭据、打上游、把结果喂给状态机。
	doRequest := func() bool {
		usable, _ := m.FilterOpen(creds)
		if len(usable) == 0 {
			return false
		}
		urls := map[string]string{primary: primarySrv.URL, backup: backupSrv.URL}
		chosen := usable[0] // 主优先；被剔除时自然落到备
		resp, err := http.Get(urls[chosen])
		success := err == nil && resp.StatusCode < 500
		if resp != nil {
			_ = resp.Body.Close()
		}
		switch chosen {
		case primary:
			atomic.AddInt32(&primaryHits, 1)
		case backup:
			atomic.AddInt32(&backupHits, 1)
		}
		policy.ApplyResultPolicy(nil, coreauth.Result{AuthID: chosen, Success: success,
			Error: func() *coreauth.Error {
				if success {
					return nil
				}
				return &coreauth.Error{HTTPStatus: http.StatusInternalServerError, Code: "upstream_error"}
			}()})
		return success
	}

	// 前 3 次：主凭据失败，状态机达到阈值打开。
	for i := 0; i < 3; i++ {
		doRequest()
	}
	if !m.Open(primary) {
		t.Fatal("primary must be open after 3 consecutive failures")
	}

	// 接下来的 10 次请求必须全部成功（走备凭据，不重试已判定故障的主凭据）。
	for i := 0; i < 10; i++ {
		if !doRequest() {
			t.Fatalf("request %d failed: failover did not route to backup", i)
		}
	}
	if got := atomic.LoadInt32(&backupHits); got != 10 {
		t.Fatalf("backup hits = %d, want 10", got)
	}

	// 冷却到期 → 半开：主凭据被放回做一次探测，仍失败 → 回到打开且冷却翻倍。
	clock.Advance(5 * time.Second)
	if m.Open(primary) {
		t.Fatal("cooldown elapsed: primary must leave open for half-open probe")
	}
	doRequest() // 探测：主凭据再次 500
	tr := m.Transitions()
	last := tr[len(tr)-1]
	if last.To != StateOpen {
		t.Fatalf("last transition = %s, want open", last.To)
	}
	if last.Cooldown != 10 {
		t.Fatalf("cooldown after failed probe = %ds, want 10s (doubled, capped)", last.Cooldown)
	}
	if !m.Open(primary) {
		t.Fatal("primary must be open again after failed probe")
	}

	// 探测成功路径：再来一轮，冷却到期后模拟主凭据恢复。
	clock.Advance(10 * time.Second)
	if m.Open(primary) {
		t.Fatal("second cooldown elapsed: must go half-open")
	}
	// 主凭据恢复：直接喂一个成功结果（模拟上游恢复）。
	policy.ApplyResultPolicy(nil, coreauth.Result{AuthID: primary, Success: true})
	if m.Open(primary) {
		t.Fatal("successful probe must close the breaker")
	}
	if m.Snapshot()[0].State != StateClosed && m.Snapshot()[1].State != StateClosed {
		t.Fatal("snapshot must report closed after recovery")
	}
}
