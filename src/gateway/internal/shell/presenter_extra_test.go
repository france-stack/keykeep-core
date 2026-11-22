package shell

import (
	"errors"
	"testing"
)

// 运行时的「非缺失」错误（例如权限问题）：仍要通知，但对话框文案不同。
func TestPresenterNotifiesWithNonMissingError(t *testing.T) {
	h := newHarness(ModeWebView2)
	h.presenter.Detect = func() (string, error) { return "", errors.New("access denied") }
	outcome, err := h.presenter.Show()
	if err != nil {
		t.Fatalf("Show 报错: %v", err)
	}
	if outcome != FellBack {
		t.Fatalf("outcome = %q，期望回退", outcome)
	}
	if len(h.notices) != 1 || h.notices[0] {
		t.Fatalf("非缺失错误应当以 missing=false 通知，实际 %v", h.notices)
	}
	if h.browser != 1 {
		t.Fatalf("用户选择浏览器时应当打开浏览器，实际 %d", h.browser)
	}
}

// 浏览器打开本身失败时，错误必须冒泡（不能静默吞掉）。
func TestPresenterPropagatesBrowserError(t *testing.T) {
	h := newHarness(ModeBrowser)
	boom := errors.New("ShellExecuteW 失败")
	h.presenter.OpenBrowser = func(string) error { return boom }
	if _, err := h.presenter.Show(); !errors.Is(err, boom) {
		t.Fatalf("应当把浏览器错误带出来，实际 %v", err)
	}
}

// OpenBrowser 未注入时给出明确错误，而不是空指针。
func TestPresenterBrowserMissingIsError(t *testing.T) {
	h := newHarness(ModeBrowser)
	h.presenter.OpenBrowser = nil
	_, err := h.presenter.Show()
	if err == nil {
		t.Fatal("未配置浏览器打开方式时应当返回错误")
	}
}

// Close 之后 Focus 会重新开窗（用户退出又重开的路径）。
func TestPresenterCloseThenShowRecreates(t *testing.T) {
	h := newHarness(ModeWebView2)
	if _, err := h.presenter.Show(); err != nil {
		t.Fatalf("Show 报错: %v", err)
	}
	if err := h.presenter.Close(); err != nil {
		t.Fatalf("Close 报错: %v", err)
	}
	if _, err := h.presenter.Focus(); err != nil {
		t.Fatalf("Focus 报错: %v", err)
	}
	if h.window.shown != 2 {
		t.Fatalf("Close 后再聚焦应重新开窗，实际 shown=%d", h.window.shown)
	}
}

// 隐藏在没有窗口时是空操作，不报错（关窗缩托盘在首启阶段也可能被触发）。
func TestPresenterHideWithoutWindowIsNoop(t *testing.T) {
	h := newHarness(ModeWebView2)
	if err := h.presenter.Hide(); err != nil {
		t.Fatalf("Hide 报错: %v", err)
	}
	if h.window.hide != 0 {
		t.Fatalf("没有窗口时不该调用 Hide，实际 %d", h.window.hide)
	}
}
