package desktop

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// F9 就绪假阳性：端口能连上时 PostgreSQL 仍可能在启动中（57P03），也可能
// 端口后面根本不是能查询的库（认证失败、库损坏）。前者应当重试，后者必须
// 立即失败——否则调用方会把「库不可用」当成就绪，起完服务后满屏 500。

func TestWaitDatabaseReadyRetriesStartingUp(t *testing.T) {
	calls := 0
	probe := func(string) error {
		calls++
		if calls < 3 {
			return errors.New("FATAL: the database system is starting up (SQLSTATE 57P03)")
		}
		return nil
	}
	if err := waitDatabaseReady(probe, "postgres://x", 2*time.Second, time.Millisecond); err != nil {
		t.Fatalf("启动中应重试并最终成功，实际: %v", err)
	}
	if calls != 3 {
		t.Fatalf("应探测 3 次，实际 %d", calls)
	}
}

func TestWaitDatabaseReadyFailsFastOnNonRetryable(t *testing.T) {
	calls := 0
	probe := func(string) error {
		calls++
		return errors.New(`failed to connect: password authentication failed for user "keykeep"`)
	}
	started := time.Now()
	err := waitDatabaseReady(probe, "postgres://x", 30*time.Second, 10*time.Millisecond)
	if err == nil {
		t.Fatal("非重试类错误必须返回错误")
	}
	if !strings.Contains(err.Error(), "password authentication failed") {
		t.Fatalf("错误应原样透传，实际: %v", err)
	}
	if calls != 1 {
		t.Fatalf("非重试类错误只应探测 1 次，实际 %d", calls)
	}
	if time.Since(started) > time.Second {
		t.Fatalf("非重试类错误应立即返回，实际耗时 %s", time.Since(started))
	}
}

func TestWaitDatabaseReadyTimesOut(t *testing.T) {
	probe := func(string) error {
		return errors.New("dial tcp 127.0.0.1:55522: connect: connection refused")
	}
	err := waitDatabaseReady(probe, "postgres://x", 40*time.Millisecond, 10*time.Millisecond)
	if err == nil {
		t.Fatal("一直连不上时应超时报错")
	}
	if !strings.Contains(err.Error(), "超时") {
		t.Fatalf("超时错误应可辨识，实际: %v", err)
	}
}

// 端口开、查询失败：错误文本既不含 starting up 也不含 connection refused，
// 因此属于「立即失败」，不能把它当成启动中无限重试。
func TestRetryableReadyError(t *testing.T) {
	cases := []struct {
		message string
		want    bool
	}{
		{"the database system is starting up", true},
		{"SQLSTATE 57P03", true},
		{"connect: connection refused", true},
		{"cached plan must not change result type", false},
		{"permission denied for database keykeep", false},
	}
	for _, c := range cases {
		if got := retryableReadyError(errors.New(c.message)); got != c.want {
			t.Fatalf("%q: want %v, got %v", c.message, c.want, got)
		}
	}
}
