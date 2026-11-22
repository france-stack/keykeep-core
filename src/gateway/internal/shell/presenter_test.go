package shell

import (
	"errors"
	"testing"
)

// fakeWindow 记录调用次序，不碰任何 Win32。
type fakeWindow struct {
	shown   int
	focus   int
	hide    int
	reload  int
	close   int
	script  ShellScript
	showErr error
}

func (f *fakeWindow) Show(script ShellScript) error {
	f.shown++
	f.script = script
	return f.showErr
}

func (f *fakeWindow) Focus() error  { f.focus++; return nil }
func (f *fakeWindow) Hide() error   { f.hide++; return nil }
func (f *fakeWindow) Reload() error { f.reload++; return nil }
func (f *fakeWindow) Close() error  { f.close++; return nil }

type harness struct {
	presenter *Presenter
	window    *fakeWindow
	browser   int
	notices   []bool
	lastURL   string
}

func newHarness(mode Mode) *harness {
	h := &harness{window: &fakeWindow{}}
	policy := mustPolicyT(nil, "http://127.0.0.1:8443")
	h.presenter = NewPresenter(policy, ShellScript{
		URL:    "http://127.0.0.1:8443",
		Origin: "http://127.0.0.1:8443",
	}, mode)
	h.presenter.NewWindow = func() Window { return h.window }
	h.presenter.Detect = func() (string, error) { return "120.0.2210.91", nil }
	h.presenter.OpenBrowser = func(url string) error {
		h.browser++
		h.lastURL = url
		return nil
	}
	h.presenter.Notify = func(missing bool) bool {
		h.notices = append(h.notices, missing)
		return true // 选「用浏览器打开」
	}
	return h
}

// mustPolicyT 是给测试初始化用的薄封装（t 为 nil 时直接 panic，便于表驱动）。
func mustPolicyT(t *testing.T, controlURL string) Policy {
	policy, err := NewPolicy(controlURL)
	if err != nil {
		if t != nil {
			t.Fatalf("NewPolicy(%q): %v", controlURL, err)
		}
		panic(err)
	}
	return policy
}

func TestPresenterShowsWebView2ByDefault(t *testing.T) {
	h := newHarness(ModeWebView2)
	outcome, err := h.presenter.Show()
	if err != nil {
		t.Fatalf("Show 报错: %v", err)
	}
	if outcome != Shown {
		t.Fatalf("outcome = %q，期望 %q", outcome, Shown)
	}
	if h.window.shown != 1 || h.browser != 0 {
		t.Fatalf("应当开窗一次且不碰浏览器，实际 shown=%d browser=%d", h.window.shown, h.browser)
	}
	if !h.presenter.Shown() {
		t.Fatal("Shown() 应为 true")
	}
}

// kill-switch：浏览器模式下不尝试开窗，直接走浏览器。
func TestPresenterBrowserModeSkipsWindow(t *testing.T) {
	h := newHarness(ModeBrowser)
	outcome, err := h.presenter.Show()
	if err != nil {
		t.Fatalf("Show 报错: %v", err)
	}
	if outcome != Opened {
		t.Fatalf("outcome = %q，期望 %q", outcome, Opened)
	}
	if h.window.shown != 0 {
		t.Fatalf("浏览器模式不该创建窗口，实际 shown=%d", h.window.shown)
	}
	if h.browser != 1 || h.lastURL != "http://127.0.0.1:8443" {
		t.Fatalf("应当用浏览器打开控制台，实际 browser=%d url=%q", h.browser, h.lastURL)
	}
}

// 运行时缺失：必须通知用户并回退浏览器，不能静默失败。
func TestPresenterFallsBackWhenRuntimeMissing(t *testing.T) {
	h := newHarness(ModeWebView2)
	h.presenter.Detect = func() (string, error) { return "", ErrRuntimeMissing }
	outcome, err := h.presenter.Show()
	if err != nil {
		t.Fatalf("Show 报错: %v", err)
	}
	if outcome != FellBack {
		t.Fatalf("outcome = %q，期望 %q", outcome, FellBack)
	}
	if len(h.notices) != 1 || !h.notices[0] {
		t.Fatalf("应当弹出「运行时缺失」对话框并标记 missing=true，实际 %v", h.notices)
	}
	if h.browser != 1 {
		t.Fatalf("用户选择浏览器时应当打开浏览器，实际 browser=%d", h.browser)
	}
	if h.window.shown != 0 {
		t.Fatalf("运行时缺失时不该尝试建窗，实际 shown=%d", h.window.shown)
	}
}

// 用户选择「去装运行时」：不打开浏览器，但结论仍是回退（本次没开窗）。
func TestPresenterRespectsInstallChoice(t *testing.T) {
	h := newHarness(ModeWebView2)
	h.presenter.Detect = func() (string, error) { return "", ErrRuntimeMissing }
	h.presenter.Notify = func(bool) bool { return false }
	outcome, err := h.presenter.Show()
	if err != nil {
		t.Fatalf("Show 报错: %v", err)
	}
	if outcome != FellBack {
		t.Fatalf("outcome = %q，期望 %q", outcome, FellBack)
	}
	if h.browser != 0 {
		t.Fatalf("用户选择安装运行时后不该抢着开浏览器，实际 browser=%d", h.browser)
	}
}

// 开窗失败同样落到浏览器：任何失败路径都有可见结果。
func TestPresenterFallsBackWhenWindowFails(t *testing.T) {
	h := newHarness(ModeWebView2)
	h.window.showErr = errors.New("WebView2 初始化失败")
	outcome, err := h.presenter.Show()
	if err != nil {
		t.Fatalf("Show 报错: %v", err)
	}
	if outcome != FellBack {
		t.Fatalf("outcome = %q，期望 %q", outcome, FellBack)
	}
	if h.browser != 1 {
		t.Fatalf("开窗失败后必须回退浏览器，实际 browser=%d", h.browser)
	}
}

// 平台没有窗口实现时也要回退，而不是什么都不做。
func TestPresenterFallsBackWhenNoWindowFactory(t *testing.T) {
	h := newHarness(ModeWebView2)
	h.presenter.NewWindow = nil
	outcome, err := h.presenter.Show()
	if err != nil {
		t.Fatalf("Show 报错: %v", err)
	}
	if outcome != FellBack || h.browser != 1 {
		t.Fatalf("outcome=%q browser=%d，期望回退浏览器一次", outcome, h.browser)
	}
}

// 托盘「打开控制台」：已有窗口时聚焦，不重开。
func TestPresenterFocusReusesWindow(t *testing.T) {
	h := newHarness(ModeWebView2)
	if _, err := h.presenter.Show(); err != nil {
		t.Fatalf("Show 报错: %v", err)
	}
	outcome, err := h.presenter.Focus()
	if err != nil {
		t.Fatalf("Focus 报错: %v", err)
	}
	if outcome != Shown || h.window.focus != 1 {
		t.Fatalf("应当聚焦已有窗口一次，实际 outcome=%q focus=%d", outcome, h.window.focus)
	}
	if h.window.shown != 1 {
		t.Fatalf("不应重复创建窗口，实际 shown=%d", h.window.shown)
	}
}

// 还没开过窗口时点「打开控制台」= 补开一次（首启期间点托盘的路径）。
func TestPresenterFocusCreatesWindowWhenAbsent(t *testing.T) {
	h := newHarness(ModeWebView2)
	outcome, err := h.presenter.Focus()
	if err != nil {
		t.Fatalf("Focus 报错: %v", err)
	}
	if outcome != Shown || h.window.shown != 1 {
		t.Fatalf("应当补开窗口一次，实际 outcome=%q shown=%d", outcome, h.window.shown)
	}
}

// 关窗缩托盘：窗口隐藏，但对象保留，之后还能被托盘重新聚焦。
func TestPresenterHideKeepsWindow(t *testing.T) {
	h := newHarness(ModeWebView2)
	if _, err := h.presenter.Show(); err != nil {
		t.Fatalf("Show 报错: %v", err)
	}
	if err := h.presenter.Hide(); err != nil {
		t.Fatalf("Hide 报错: %v", err)
	}
	if h.window.hide != 1 {
		t.Fatalf("应当隐藏窗口一次，实际 %d", h.window.hide)
	}
	if _, err := h.presenter.Focus(); err != nil {
		t.Fatalf("Focus 报错: %v", err)
	}
	if h.window.focus != 1 || h.window.shown != 1 {
		t.Fatalf("隐藏后聚焦应复用同一窗口，实际 focus=%d shown=%d", h.window.focus, h.window.shown)
	}
}

// 退出：销毁窗口。
func TestPresenterCloseDestroysWindow(t *testing.T) {
	h := newHarness(ModeWebView2)
	if _, err := h.presenter.Show(); err != nil {
		t.Fatalf("Show 报错: %v", err)
	}
	if err := h.presenter.Close(); err != nil {
		t.Fatalf("Close 报错: %v", err)
	}
	if h.window.close != 1 {
		t.Fatalf("应当销毁窗口一次，实际 %d", h.window.close)
	}
}

// 托盘「在浏览器中打开」是显式入口，不受模式影响。
func TestPresenterOpenInBrowserAlwaysWorks(t *testing.T) {
	for _, mode := range []Mode{ModeWebView2, ModeBrowser} {
		h := newHarness(mode)
		if err := h.presenter.OpenInBrowser(); err != nil {
			t.Fatalf("模式 %q: OpenInBrowser 报错: %v", mode, err)
		}
		if h.browser != 1 {
			t.Fatalf("模式 %q: 应当打开浏览器一次，实际 %d", mode, h.browser)
		}
	}
}

func TestPresenterClassifyDelegatesPolicy(t *testing.T) {
	h := newHarness(ModeWebView2)
	if got := h.presenter.Classify("https://example.com"); got != OpenExternal {
		t.Fatalf("Classify 应委托给 Policy，实际 %q", got)
	}
	if got := h.presenter.Classify("http://127.0.0.1:8443/login"); got != Allow {
		t.Fatalf("同源地址应放行，实际 %q", got)
	}
}
