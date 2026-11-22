//go:build windows

package shell

import (
	"errors"
	"fmt"
	"log"
	"os"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/jchv/go-webview2/pkg/edge"
	"golang.org/x/sys/windows"
)

// 纯 syscall + go-webview2 的 pkg/edge 实现原生窗口。
//
// 为什么不用库里更高层的 webview2.WebView：它的窗口是自己的，WM_CLOSE 直接
// 销毁窗口并退出消息循环（等于「关窗退出」），而这套产品的语义是「关窗缩托盘、
// 服务继续跑」；它也没有导出 WebResourceRequested 回调，导航白名单无处安放。
// 所以窗口、消息泵、关闭语义都由本文件持有，只把 WebView2 宿主能力交给 edge 包。
//
// 线程约束（ADR-013 §2.2）：本文件里所有 Win32/COM 调用只发生在 uiMain
// 所在的 goroutine。其它 goroutine 一律通过 post() 投递闭包。

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	shell32D = syscall.NewLazyDLL("shell32.dll")
	k32      = syscall.NewLazyDLL("kernel32.dll")

	procRegisterClassExW              = user32.NewProc("RegisterClassExW")
	procCreateWindowExW               = user32.NewProc("CreateWindowExW")
	procDefWindowProcW                = user32.NewProc("DefWindowProcW")
	procDestroyWindow                 = user32.NewProc("DestroyWindow")
	procGetMessageW                   = user32.NewProc("GetMessageW")
	procTranslateMessage              = user32.NewProc("TranslateMessage")
	procDispatchMessageW              = user32.NewProc("DispatchMessageW")
	procPostQuitMessage               = user32.NewProc("PostQuitMessage")
	procPostMessageW                  = user32.NewProc("PostMessageW")
	procSendMessageW                  = user32.NewProc("SendMessageW")
	procShowWindow                    = user32.NewProc("ShowWindow")
	procIsIconic                      = user32.NewProc("IsIconic")
	procSetForegroundWindow           = user32.NewProc("SetForegroundWindow")
	procSetWindowTextW                = user32.NewProc("SetWindowTextW")
	procLoadImageW                    = user32.NewProc("LoadImageW")
	procLoadIconW                     = user32.NewProc("LoadIconW")
	procGetSystemMetrics              = user32.NewProc("GetSystemMetrics")
	procMessageBoxW                   = user32.NewProc("MessageBoxW")
	procSetProcessDpiAwarenessContext = user32.NewProc("SetProcessDpiAwarenessContext")
	procGetModuleHandleW              = k32.NewProc("GetModuleHandleW")
	procShellExecuteW                 = shell32D.NewProc("ShellExecuteW")
)

const (
	wsOverlappedWindow = 0x00CF0000
	swHide             = 0
	swShow             = 5
	swRestore          = 9
	wmDestroy          = 0x0002
	wmClose            = 0x0010
	wmSize             = 0x0005
	wmAppDispatch      = 0x8000 // WM_APP：窗口自己的派发队列

	iconResourceArrow = 32512 // IDC_ARROW
	iconResourceApp   = 32512 // IDI_APPLICATION

	imageIcon      = 1
	lrLoadFromFile = 0x00000010

	smCxScreen         = 0
	smCyScreen         = 1
	colorWindowClass   = colorWindow + 1
	colorWindow        = 5
	mbYesNo            = 0x00000004
	mbIconWarning      = 0x00000030
	mbSetForeground    = 0x00010000
	mbTopmost          = 0x00040000
	idYes              = 6
	swShownormal       = 1
	perMonitorAwareV2  = ^uintptr(3) // DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 == -4
	windowClassName    = "KeyKeepConsoleWindow"
	rpcEChangedModeErr = 0x80010106
)

// windowRegistry 把 HWND 映射到窗口对象。
//
// 走这张表而不是「进程内只有一个窗口」的全局变量：窗口过程中途可能收到
// 属于已销毁窗口的消息（销毁过程中的消息队列），按句柄查表能自然地把它们
// 交回 DefWindowProc，而不是打进一个已经失效的对象。
var (
	windowRegistry   = map[uintptr]*win32Window{}
	windowRegistryMu sync.RWMutex
)

func lookupWindow(hwnd uintptr) *win32Window {
	windowRegistryMu.RLock()
	defer windowRegistryMu.RUnlock()
	return windowRegistry[hwnd]
}

func registerWindow(hwnd uintptr, w *win32Window) {
	windowRegistryMu.Lock()
	windowRegistry[hwnd] = w
	windowRegistryMu.Unlock()
}

func unregisterWindow(hwnd uintptr) {
	windowRegistryMu.Lock()
	delete(windowRegistry, hwnd)
	windowRegistryMu.Unlock()
}

// win32Window 是 Window 的 Windows 实现。
type win32Window struct {
	mu       sync.Mutex
	script   ShellScript
	policy   Policy
	hwnd     uintptr
	chromium *edge.Chromium
	queue    []func(hwnd uintptr)
	initDone chan struct{}
	initErr  error
	starting bool
	closing  bool
	closed   chan struct{}

	// openExternal 是导航白名单的转交出口，由工厂注入。
	openExternal func(string) error
}

// newPlatformWindow 由平台工厂调用（见 platform_windows.go）。
func newPlatformWindow(openExternal func(string) error) *win32Window {
	return &win32Window{initDone: make(chan struct{}), openExternal: openExternal}
}

// Show 创建窗口（首次）并显示。
func (w *win32Window) Show(script ShellScript) error {
	w.mu.Lock()
	if w.hwnd == 0 && !w.starting {
		w.starting = true
		w.script = script
		go w.uiMain(script)
	}
	done := w.initDone
	w.mu.Unlock()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		return errors.New("等待 WebView2 窗口初始化超时")
	}

	w.mu.Lock()
	err := w.initErr
	w.mu.Unlock()
	if err != nil {
		return err
	}
	return w.post(func(hwnd uintptr) { bringToFront(w, hwnd) })
}

// Focus 把已存在的窗口提到前台。
func (w *win32Window) Focus() error {
	return w.post(func(hwnd uintptr) { bringToFront(w, hwnd) })
}

// Hide 隐藏窗口。关窗按钮走的就是这条路：进程与服务都继续跑。
func (w *win32Window) Hide() error {
	return w.post(func(hwnd uintptr) {
		procShowWindow.Call(hwnd, swHide)
	})
}

// Reload 重新加载控制台。窗口还没建好时是空操作。
func (w *win32Window) Reload() error {
	return w.post(func(hwnd uintptr) {
		w.mu.Lock()
		chromium := w.chromium
		w.mu.Unlock()
		if chromium == nil {
			return
		}
		chromium.Navigate(w.script.URL)
	})
}

// Close 真正销毁窗口并结束消息循环。
func (w *win32Window) Close() error {
	w.mu.Lock()
	if w.closing {
		w.mu.Unlock()
		return nil
	}
	w.closing = true
	hwnd := w.hwnd
	w.mu.Unlock()
	// 放行上面的 WM_CLOSE 分支，让它正常销毁窗口并 PostQuitMessage。
	if hwnd != 0 {
		procPostMessageW.Call(hwnd, wmClose, 0, 0)
	}
	return nil
}

// post 把一个以 hwnd 为参数的闭包投递到 UI 线程执行。
func (w *win32Window) post(fn func(hwnd uintptr)) error {
	w.mu.Lock()
	if w.hwnd == 0 {
		w.mu.Unlock()
		return errors.New("窗口尚未创建")
	}
	hwnd := w.hwnd
	w.queue = append(w.queue, fn)
	w.mu.Unlock()
	procPostMessageW.Call(hwnd, wmAppDispatch, 0, 0)
	return nil
}

func (w *win32Window) drainQueue(hwnd uintptr) {
	w.mu.Lock()
	queue := w.queue
	w.queue = nil
	w.mu.Unlock()
	for _, fn := range queue {
		fn(hwnd)
	}
}

// uiMain 是窗口线程：LockOSThread + STA + 消息泵，全过程只在这个 goroutine 里。
func (w *win32Window) uiMain(script ShellScript) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	policy, err := NewPolicy(script.Origin)
	if err != nil {
		w.finishInit(err)
		return
	}

	// DPI 感知必须在任何窗口创建之前设置，否则高 DPI 下整窗发糊。
	setDpiAwareness()

	// STA：WebView2 的 COM 对象要求单线程套间 + 消息泵。
	// RPC_E_CHANGED_MODE 表示线程已被初始化成别的套间，不重复初始化、继续尝试。
	if coErr := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED|windows.COINIT_DISABLE_OLE1DDE); coErr != nil {
		if errno, ok := coErr.(syscall.Errno); !ok || uint32(errno) != rpcEChangedModeErr {
			log.Printf("初始化 COM 套间失败: %v", coErr)
		}
	} else {
		defer windows.CoUninitialize()
	}

	if dir := script.ProfileDir; dir != "" {
		if mkErr := os.MkdirAll(dir, 0o700); mkErr != nil {
			w.finishInit(fmt.Errorf("创建 WebView2 用户数据目录失败: %w", mkErr))
			return
		}
		// 0700 等价 ACL：会话 cookie 落在这个目录里，不该给同机其它账号读。
		if aclErr := HardenProfileACL(dir); aclErr != nil {
			log.Printf("收紧 WebView2 用户数据目录权限失败（继续启动）: %v", aclErr)
		}
	}

	hwnd, regErr := createHostWindow(w, script)
	if regErr != nil {
		w.finishInit(regErr)
		return
	}

	chromium := edge.NewChromium()
	chromium.DataPath = script.ProfileDir
	if !chromium.Embed(hwnd) {
		procDestroyWindow.Call(hwnd)
		w.finishInit(errors.New("WebView2 初始化失败（运行时缺失或版本不兼容）"))
		return
	}

	w.mu.Lock()
	w.hwnd = hwnd
	w.chromium = chromium
	w.policy = policy
	w.mu.Unlock()

	applySettings(chromium, script.DevTools)

	// 导航白名单：只对「文档级」请求设过滤器，即真正会替换当前页面的导航。
	//
	// 说明：go-webview2 没有导出 NavigationStarting（其 vtable 里有槽位，但无公开
	// 注册入口），这里用 WebView2 官方的 WebResourceRequested 文档级过滤器达成
	// 同样的语义——非白名单导航不进入窗口，转交系统浏览器或直接拒绝。
	// 实现注记见 ADR-013。
	// 先挂回调再注册过滤器：过滤器注册失败时回调不会被调用，反过来挂一个
	// 依赖未注册过滤器的回调只是空转。库的 AddWebResourceRequestedFilter
	// 在 COM 失败时直接 log.Fatal（这是它自己的取舍），所以只能放在这里、
	// 在确认运行时可用之后调用。
	chromium.WebResourceRequestedCallback = w.onDocumentRequest
	chromium.AddWebResourceRequestedFilter("*", edge.COREWEBVIEW2_WEB_RESOURCE_CONTEXT_DOCUMENT)

	chromium.Navigate(script.URL)
	chromium.Resize()
	procShowWindow.Call(hwnd, swShow)
	// 必须在窗口可见之后再置 WebView2 可见。
	//
	// WebView2 的 controller 新建出来时 IsVisible 默认是 FALSE，官方文档写得很直白：
	// 「If IsVisible is set to FALSE, the WebView2 is transparent and is not rendered」。
	// 少了这一步，窗口是显示出来了、页面也加载完了，但 WebView2 那一层从不合成，
	// 客户区只剩下窗口类的纯白背景——现象就是「白屏卡住」，而且没有任何报错。
	w.showWebView()
	bringToFront(w, hwnd)

	w.finishInit(nil)
	w.messageLoop(hwnd)
}

func (w *win32Window) finishInit(err error) {
	w.mu.Lock()
	w.initErr = err
	w.mu.Unlock()
	close(w.initDone)
}

func applySettings(chromium *edge.Chromium, devTools bool) {
	settings, err := chromium.GetSettings()
	if err != nil {
		log.Printf("读取 WebView2 设置失败（继续）: %v", err)
		return
	}
	if err := settings.PutAreDevToolsEnabled(devTools); err != nil {
		log.Printf("设置 DevTools 开关失败: %v", err)
	}
	if err := settings.PutAreDefaultContextMenusEnabled(devTools); err != nil {
		log.Printf("设置右键菜单开关失败: %v", err)
	}
	if err := settings.PutIsStatusBarEnabled(false); err != nil {
		log.Printf("关闭状态栏失败: %v", err)
	}
}

// onDocumentRequest 是导航白名单的执行点。
//
// 判定本身是纯函数（Policy.Classify），这里只做「放行 / 转交浏览器 / 拒绝」
// 三种处置，绝不出现「拦下来但什么都不提示」的分支。
func (w *win32Window) onDocumentRequest(req *edge.ICoreWebView2WebResourceRequest, args *edge.ICoreWebView2WebResourceRequestedEventArgs) {
	uri, err := req.GetUri()
	if err != nil {
		log.Printf("读取导航地址失败: %v", err)
		return
	}
	if uri == "" {
		return
	}
	w.mu.Lock()
	policy := w.policy
	script := w.script
	openExternal := w.openExternal
	w.mu.Unlock()

	switch policy.Classify(uri) {
	case Allow:
		return
	case OpenExternal:
		log.Printf("外部地址转交默认浏览器：%s", uri)
		if openExternal != nil {
			target := uri
			go func() {
				if openErr := openExternal(target); openErr != nil {
					log.Printf("打开外部链接失败: %v", openErr)
				}
			}()
		}
		w.respond(args, 200, "OK", MessagePage{
			Title:   "已在浏览器中打开",
			Heading: "这个链接已交给默认浏览器",
			Detail:  uri,
			BackURL: script.URL,
		}.HTML())
	case Block:
		log.Printf("拒绝在窗口内加载：%s", uri)
		w.respond(args, 403, "Forbidden", MessagePage{
			Title:   "已阻止",
			Heading: "这个地址不能在 KeyKeep 窗口内打开",
			Detail:  uri,
			BackURL: script.URL,
		}.HTML())
	}
}

// respond 用合成响应替换被拦下的导航。
//
// WebView2 在文档级请求上给出响应就不会再去加载目标地址——这正是本方案
// 能成立的原因；合成页面把「发生了什么、怎么回控制台」直接写给用户。
func (w *win32Window) respond(args *edge.ICoreWebView2WebResourceRequestedEventArgs, status int, reason, body string) {
	w.mu.Lock()
	chromium := w.chromium
	w.mu.Unlock()
	if chromium == nil {
		return
	}
	env := chromium.Environment()
	if env == nil {
		return
	}
	resp, err := env.CreateWebResourceResponse(
		[]byte(body), status, reason, "Content-Type: text/html; charset=utf-8\r\n",
	)
	if err != nil {
		log.Printf("构造导航响应失败: %v", err)
		return
	}
	if err := args.PutResponse(resp); err != nil {
		log.Printf("替换导航响应失败: %v", err)
	}
}

// messageLoop 是窗口线程的消息泵。
func (w *win32Window) messageLoop(hwnd uintptr) {
	var msg msgT
	for {
		ret, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(ret) <= 0 {
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
	}
	w.mu.Lock()
	w.hwnd = 0
	w.chromium = nil
	w.mu.Unlock()
}

// wndProc 处理窗口消息。
func (w *win32Window) wndProc(hwnd, message, wParam, lParam uintptr) uintptr {
	switch uint32(message) {
	case wmClose:
		w.mu.Lock()
		closing := w.closing
		w.mu.Unlock()
		if closing {
			return defaultWindowProc(hwnd, message, wParam, lParam)
		}
		// 关窗 = 缩到托盘：窗口隐藏，服务与进程继续跑（ADR-013 §2.3）。
		procShowWindow.Call(hwnd, swHide)
		return 0
	case wmSize:
		w.mu.Lock()
		chromium := w.chromium
		w.mu.Unlock()
		if chromium != nil {
			// 可见性**不在这里**管，只在这里处理尺寸。
			//
			// WM_SIZE 的 SIZE_MINIMIZED 有两个来源：真最小化，以及「关窗缩托盘」的
			// ShowWindow(SW_HIDE)。后者也会派发 SIZE_MINIMIZED，而且消息到达时窗口
			// 还没真正变成不可见，任何按「当前是否可见」分流的判断都会踩空；那时对
			// controller 调 PutIsVisible 会拿到 "Invalid window handle." 并刷一行
			// 假告警。既然可见性只由 uiMain 首次显示与 bringToFront 恢复两处显式置位
			// 就够了（WebView2 的 IsVisible 一旦为 true 就随宿主窗口一起隐藏/最小化），
			// 这里再动它只会引入错误路径。
			if iconic, _, _ := procIsIconic.Call(hwnd); iconic == 0 {
				chromium.Resize()
			}
		}
		return 0
	case wmAppDispatch:
		w.drainQueue(hwnd)
		return 0
	case wmDestroy:
		unregisterWindow(hwnd)
		procPostQuitMessage.Call(0)
		return 0
	}
	return defaultWindowProc(hwnd, message, wParam, lParam)
}

func defaultWindowProc(hwnd, message, wParam, lParam uintptr) uintptr {
	ret, _, _ := procDefWindowProcW.Call(hwnd, message, wParam, lParam)
	return ret
}

type wndClassExW struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   syscall.Handle
	Icon       syscall.Handle
	Cursor     syscall.Handle
	Background syscall.Handle
	MenuName   *uint16
	ClassName  *uint16
	IconSmall  syscall.Handle
}

type msgT struct {
	Hwnd    syscall.Handle
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      struct{ X, Y int32 }
}

// windowProcCallback 是本包唯一注册的窗口过程，按 HWND 查表分发给对应窗口。
var windowProcCallback = syscall.NewCallback(func(hwnd, message, wParam, lParam uintptr) uintptr {
	if owner := lookupWindow(hwnd); owner != nil {
		return owner.wndProc(hwnd, message, wParam, lParam)
	}
	return defaultWindowProc(hwnd, message, wParam, lParam)
})

// createHostWindow 建一个属于我们自己的顶层窗口（不走库的窗口）。
func createHostWindow(owner *win32Window, script ShellScript) (uintptr, error) {
	instance, _, _ := procGetModuleHandleW.Call(0)

	className, err := syscall.UTF16PtrFromString(windowClassName)
	if err != nil {
		return 0, err
	}
	title := script.Title
	if title == "" {
		title = ShellTitle
	}
	titlePtr, err := syscall.UTF16PtrFromString(title)
	if err != nil {
		return 0, err
	}
	icon := loadWindowIcon(script.IconPath)

	wc := wndClassExW{
		Size:       uint32(unsafe.Sizeof(wndClassExW{})),
		WndProc:    windowProcCallback,
		Instance:   syscall.Handle(instance),
		Icon:       syscall.Handle(icon),
		Cursor:     syscall.Handle(loadSystemIcon(iconResourceArrow)),
		Background: colorWindowClass,
		ClassName:  className,
		IconSmall:  syscall.Handle(icon),
	}
	// 重复注册同一个窗口类的唯一后果是这个调用返回 0（ERROR_CLASS_ALREADY_EXISTS），
	// 那时沿用已注册的类继续建窗即可。
	procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))

	width, height := script.Width, script.Height
	if width <= 0 {
		width = DefaultShellWidth
	}
	if height <= 0 {
		height = DefaultShellHeight
	}
	x, y := centeredPosition(width, height)

	hwnd, _, callErr := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(titlePtr)),
		wsOverlappedWindow,
		uintptr(x), uintptr(y),
		uintptr(width), uintptr(height),
		0, 0, instance, 0,
	)
	if hwnd == 0 {
		return 0, fmt.Errorf("创建窗口失败: %v", callErr)
	}
	// 先登记再返回：窗口过程可能在 CreateWindowExW 期间就被调用。
	registerWindow(hwnd, owner)
	return hwnd, nil
}

// centeredPosition 在主屏居中。
func centeredPosition(width, height int) (int, int) {
	screenW, _, _ := procGetSystemMetrics.Call(smCxScreen)
	screenH, _, _ := procGetSystemMetrics.Call(smCyScreen)
	x := (int(screenW) - width) / 2
	y := (int(screenH) - height) / 2
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}
	return x, y
}

// setDpiAwareness 设置进程级 DPI 感知。
//
// SetProcessDpiAwarenessContext 是 Windows 10 1703 才有的导出：更老的机器上
// LazyProc.Call 会 panic，所以先 Find() 判断再调用，失败时保持系统默认。
func setDpiAwareness() {
	if err := procSetProcessDpiAwarenessContext.Find(); err != nil {
		return
	}
	procSetProcessDpiAwarenessContext.Call(perMonitorAwareV2)
}

// loadWindowIcon 优先用安装目录里的 keykeep.ico，找不到就用系统默认图标。
func loadWindowIcon(path string) uintptr {
	if path != "" {
		if ptr, err := syscall.UTF16PtrFromString(path); err == nil {
			if handle, _, _ := procLoadImageW.Call(0, uintptr(unsafe.Pointer(ptr)), imageIcon, 0, 0, lrLoadFromFile); handle != 0 {
				return handle
			}
		}
	}
	return loadSystemIcon(iconResourceApp)
}

func loadSystemIcon(resource uintptr) uintptr {
	handle, _, _ := procLoadIconW.Call(0, resource)
	return handle
}

func bringToFront(w *win32Window, hwnd uintptr) {
	if hwnd == 0 {
		return
	}
	iconic, _, _ := procIsIconic.Call(hwnd)
	if iconic != 0 {
		procShowWindow.Call(hwnd, swRestore)
	} else {
		procShowWindow.Call(hwnd, swShow)
	}
	// 窗口从托盘恢复时 WebView2 的可见性不会自己回来：WM_SHOWWINDOW 触发的
	// 重绘只重画宿主窗口，controller 仍然是「不可见 → 不渲染」。所以每次
	// 把窗口提到前台都要顺手把 WebView2 置为可见，否则「关窗缩托盘、再打开」
	// 就是稳定的第二次白屏。
	w.showWebView()
	procSetForegroundWindow.Call(hwnd)
}

// showWebView 把 WebView2 controller 置为可见（WebView2 不会跟随父窗口自动可见）。
//
// 这一步在 uiMain 首次显示、托盘恢复、以及 WM_SIZE 从最小化还原时都要做；
// 失败只记日志不中断——可见性置位失败通常意味着 controller 已经没了，
// 那时继续跑消息泵，用户至少还能看到窗口标题和托盘。
func (w *win32Window) showWebView() {
	w.mu.Lock()
	chromium := w.chromium
	w.mu.Unlock()
	if chromium == nil {
		return
	}
	if err := chromium.Show(); err != nil {
		log.Printf("设置 WebView2 可见失败（继续）: %v", err)
	}
}

// MessageBoxFallback 在运行时缺失或开窗失败时给用户两个明确选项。
//
// 返回 true 表示「直接用浏览器打开控制台」；false 表示用户选择去装运行时
// （此时调用方负责打开官方下载页）。
func MessageBoxFallback(missing bool) bool {
	text := "这台电脑缺少 WebView2 运行时（Microsoft Edge 的网页组件），" +
		"KeyKeep 的控制台窗口无法在应用内显示。\r\n\r\n" +
		"「是」：打开微软官方下载页；装好后重新启动 KeyKeep 即恢复原生窗口。\r\n" +
		"「否」：本次直接用浏览器打开控制台（功能不受影响）。"
	if !missing {
		text = "KeyKeep 控制台窗口打开失败。\r\n\r\n" +
			"「是」：打开微软官方下载页，修复 WebView2 运行时后重试。\r\n" +
			"「否」：本次直接用浏览器打开控制台（功能不受影响）。"
	}
	caption, _ := syscall.UTF16PtrFromString("KeyKeep")
	body, _ := syscall.UTF16PtrFromString(text)
	ret, _, _ := procMessageBoxW.Call(
		0,
		uintptr(unsafe.Pointer(caption)),
		uintptr(unsafe.Pointer(body)),
		uintptr(mbYesNo|mbIconWarning|mbSetForeground|mbTopmost),
	)
	return ret != idYes
}

// OpenExternalURL 用系统默认浏览器打开 URL（导航白名单的转交出口）。
func OpenExternalURL(target string) error {
	verb, err := syscall.UTF16PtrFromString("open")
	if err != nil {
		return err
	}
	urlPtr, err := syscall.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	ret, _, _ := procShellExecuteW.Call(
		0,
		uintptr(unsafe.Pointer(verb)),
		uintptr(unsafe.Pointer(urlPtr)),
		0, 0,
		swShownormal,
	)
	if ret <= 32 {
		return fmt.Errorf("打开浏览器失败（code=%d）", ret)
	}
	return nil
}
