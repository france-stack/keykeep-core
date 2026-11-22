package shell

import (
	"errors"
	"fmt"
	"log"
	"sync"
)

// Outcome 是一次呈现尝试的结果。
type Outcome string

const (
	// Shown 表示窗口已在 WebView2 里显示（或已存在并聚焦）。
	Shown Outcome = "webview2"
	// FellBack 表示没能开窗，已交给默认浏览器。
	FellBack Outcome = "browser-fallback"
	// Opened 表示按配置直接走浏览器（没尝试开窗）。
	Opened Outcome = "browser"
	// Skipped 表示控制台还没就绪或已关闭，什么都没做。
	Skipped Outcome = "skipped"
)

// Window 是「原生窗口」的最小抽象。Windows 实现见 window_windows.go，
// 其它平台是永远返回 ErrRuntimeMissing 的桩。
type Window interface {
	// Show 显示窗口（首次调用时创建）。
	Show(ShellScript) error
	// Focus 把已存在的窗口提到前台。
	Focus() error
	// Hide 隐藏窗口（关窗按钮走这条，服务继续跑）。
	Hide() error
	// Reload 重新加载控制台（重启控制台进程后窗口里的旧页面已经断开）。
	Reload() error
	// Close 销毁窗口与消息循环。
	Close() error
}

// Presenter 决定「这一刻该用哪种方式把控制台给用户看」。
//
// 它不碰 Win32、不碰注册表：窗口与浏览器打开都是注入的，
// 于是「WebView2 缺失时一定回退浏览器、绝不静默失败」这条承诺可以在
// 任何平台上被单测钉住。
type Presenter struct {
	Policy Policy
	Script ShellScript
	Mode   Mode

	// NewWindow 在真正开窗时才调用；返回 nil 表示这个平台没有实现。
	NewWindow func() Window
	// Detect 探测 WebView2 运行时；与 NewWindow 分开是因为「检测」要能在
	// 开窗之前给出结论（缺失时直接弹对话框，而不是先建窗再失败）。
	Detect func() (string, error)
	// OpenBrowser 是回退路径与「在浏览器中打开」入口。
	OpenBrowser func(string) error
	// Notify 用于运行时缺失时给用户一个选择（Windows 上是 MessageBox）。
	// 返回 true 表示用户选择「用浏览器打开」。
	Notify func(missing bool) bool

	mu      sync.Mutex
	window  Window
	pending bool // 已请求过 Show，控制台就绪后要补一次
	shown   bool
}

// NewPresenter 组装一个 Presenter。
func NewPresenter(policy Policy, script ShellScript, mode Mode) *Presenter {
	return &Presenter{Policy: policy, Script: script, Mode: mode}
}

// Classify 暴露给上层做日志与测试。
func (p *Presenter) Classify(uri string) Decision { return p.Policy.Classify(uri) }

// Show 呈现控制台。
//
// 决策顺序：浏览器模式 → 直接用浏览器；否则探测运行时，缺失则通知并回退；
// 运行时可用就开窗，开窗失败同样回退——**任何一条失败路径都落到浏览器**，
// 不存在「什么都没发生」的结局。
func (p *Presenter) Show() (Outcome, error) {
	p.mu.Lock()
	p.pending = true
	p.mu.Unlock()

	if p.Mode == ModeBrowser {
		p.markShown()
		return Opened, p.openBrowser()
	}
	if p.Detect != nil {
		version, err := p.Detect()
		if err != nil {
			log.Printf("WebView2 运行时不可用（%v），回退浏览器", err)
			if p.Notify != nil && !p.Notify(errors.Is(err, ErrRuntimeMissing)) {
				// 用户选择「打开官方下载页」而不是「直接用浏览器」。
				return FellBack, nil
			}
			p.markShown()
			return FellBack, p.openBrowser()
		}
		log.Printf("WebView2 运行时 %s", version)
	}
	if p.NewWindow == nil {
		p.markShown()
		return FellBack, p.openBrowser()
	}

	p.mu.Lock()
	window := p.window
	if window == nil {
		window = p.NewWindow()
		p.window = window
	}
	p.mu.Unlock()
	if window == nil {
		p.markShown()
		return FellBack, p.openBrowser()
	}

	if err := window.Show(p.Script); err != nil {
		log.Printf("创建 WebView2 窗口失败（%v），回退浏览器", err)
		p.mu.Lock()
		p.window = nil
		p.mu.Unlock()
		p.markShown()
		return FellBack, p.openBrowser()
	}
	p.markShown()
	return Shown, nil
}

// Focus 在窗口已存在时聚焦；没有窗口时按设置重新呈现一次。
//
// 托盘「打开控制台」走这条：用户点它的意思永远是「现在让我看到控制台」，
// 所以窗口还没建（例如第一次点击时控制台仍在启动）就直接补开。
func (p *Presenter) Focus() (Outcome, error) {
	p.mu.Lock()
	window := p.window
	p.mu.Unlock()
	if p.Mode == ModeBrowser || window == nil {
		return p.Show()
	}
	if err := window.Focus(); err != nil {
		log.Printf("聚焦窗口失败（%v），重新呈现", err)
		return p.Show()
	}
	return Shown, nil
}

// Hide 隐藏窗口（关窗缩托盘）。没有窗口时是空操作。
func (p *Presenter) Hide() error {
	p.mu.Lock()
	window := p.window
	p.mu.Unlock()
	if window == nil {
		return nil
	}
	return window.Hide()
}

// Reload 让窗口重新加载控制台。
//
// 用在「重启服务」之后：控制台进程换了，窗口里的旧页面已经跟服务断开，
// 不重载的话用户会看到一个点不动的界面，误以为重启失败。
// 浏览器模式下没有窗口可重载，直接返回。
func (p *Presenter) Reload() error {
	p.mu.Lock()
	window := p.window
	p.mu.Unlock()
	if window == nil {
		return nil
	}
	return window.Reload()
}

// Close 销毁窗口。kkdesk 退出时调用。
func (p *Presenter) Close() error {
	p.mu.Lock()
	window := p.window
	p.window = nil
	p.mu.Unlock()
	if window == nil {
		return nil
	}
	return window.Close()
}

// OpenInBrowser 是托盘「在浏览器中打开」的显式入口（ADR-013 §2.3）。
func (p *Presenter) OpenInBrowser() error { return p.openBrowser() }

// Shown 报告是否已经呈现过（用于「启动完成后再开窗」的时序判断）。
func (p *Presenter) Shown() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.shown
}

func (p *Presenter) markShown() {
	p.mu.Lock()
	p.shown = true
	p.pending = false
	p.mu.Unlock()
}

func (p *Presenter) openBrowser() error {
	if p.OpenBrowser == nil {
		return fmt.Errorf("未配置浏览器打开方式")
	}
	return p.OpenBrowser(p.Script.URL)
}
