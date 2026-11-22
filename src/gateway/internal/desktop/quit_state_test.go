package desktop

import "testing"

// F10：停止请求可能早于托盘窗口创建。状态机必须保证请求不会丢，
// 也不会被重复投递（重复投递会在消息循环里制造多条 WM_CLOSE）。

func TestQuitStateRequestBeforeWindow(t *testing.T) {
	var q quitState

	if got := q.request(); got {
		t.Fatal("窗口未就绪时 request 不应要求立即投递")
	}
	if !q.pending() {
		t.Fatal("请求应被记住，状态为待投递")
	}
	if got := q.ready(); !got {
		t.Fatal("窗口就绪后应报告有欠投递")
	}
	q.take()
	if q.pending() {
		t.Fatal("投递完成后不应再有待投递")
	}
	if got := q.ready(); got {
		t.Fatal("已投递的请求不应在再次就绪时复活")
	}
}

func TestQuitStateRequestAfterWindow(t *testing.T) {
	var q quitState
	q.ready()

	if got := q.request(); !got {
		t.Fatal("窗口已就绪时 request 应立即要求投递")
	}
	q.take()
	if got := q.request(); got {
		t.Fatal("重复请求不应重复投递")
	}
}

func TestQuitStateResetKeepsPending(t *testing.T) {
	var q quitState
	q.ready()
	if got := q.request(); !got {
		t.Fatal("窗口已就绪时 request 应立即要求投递")
	}
	q.take()
	q.reset()
	if !q.pending() {
		t.Fatal("投递失败复位后应回到待投递")
	}
}
