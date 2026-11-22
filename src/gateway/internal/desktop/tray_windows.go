//go:build windows

package desktop

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"unsafe"
)

// 纯 syscall 调 Win32 托盘 API：不引入 cgo、不引第三方 GUI 库。
// 只用到 6 个函数，自己声明比拖一个窗口框架进来更可控。

var (
	user32                  = syscall.NewLazyDLL("user32.dll")
	shell32                 = syscall.NewLazyDLL("shell32.dll")
	kernel32                = syscall.NewLazyDLL("kernel32.dll")
	procRegisterClassExW    = user32.NewProc("RegisterClassExW")
	procCreateWindowExW     = user32.NewProc("CreateWindowExW")
	procDefWindowProcW      = user32.NewProc("DefWindowProcW")
	procDestroyWindow       = user32.NewProc("DestroyWindow")
	procGetMessageW         = user32.NewProc("GetMessageW")
	procTranslateMessage    = user32.NewProc("TranslateMessage")
	procDispatchMessageW    = user32.NewProc("DispatchMessageW")
	procPostQuitMessage     = user32.NewProc("PostQuitMessage")
	procCreatePopupMenu     = user32.NewProc("CreatePopupMenu")
	procAppendMenuW         = user32.NewProc("AppendMenuW")
	procTrackPopupMenu      = user32.NewProc("TrackPopupMenu")
	procDestroyMenu         = user32.NewProc("DestroyMenu")
	procSetForegroundWindow = user32.NewProc("SetForegroundWindow")
	procGetCursorPos        = user32.NewProc("GetCursorPos")
	procLoadImageW          = user32.NewProc("LoadImageW")
	procLoadIconW           = user32.NewProc("LoadIconW")
	procShellNotifyIconW    = shell32.NewProc("Shell_NotifyIconW")
	procSetWindowLongPtrW   = user32.NewProc("SetWindowLongPtrW")
	procCallWindowProcW     = user32.NewProc("CallWindowProcW")
	procGetModuleHandleW    = kernel32.NewProc("GetModuleHandleW")
	procGetCurrentThreadId  = kernel32.NewProc("GetCurrentThreadId")
	procPostThreadMessageW  = user32.NewProc("PostThreadMessageW")
	procPostMessageW        = user32.NewProc("PostMessageW")
)

const (
	wsOverlappedWindow = 0x00000000
	wmDestroy          = 0x0002
	wmClose            = 0x0010
	wmCommand          = 0x0111
	wmUser             = 0x0400
	wmRButtonUp        = 0x0205
	wmLButtonUp        = 0x0202
	wmTrayIcon         = wmUser + 1 // 自定义托盘回调消息
	wmQuit             = wmUser + 2

	nimAdd     = 0x00000000
	nimModify  = 0x00000001
	nimDelete  = 0x00000002
	nifMessage = 0x00000001
	nifIcon    = 0x00000002
	nifTip     = 0x00000004

	mfString    = 0x00000000
	mfSeparator = 0x00000800
	mfChecked   = 0x00000008
	mfGrayed    = 0x00000001

	tpmLeftAlign   = 0x0000
	tpmRightButton = 0x0002

	idiApplication = 32512

	imageIcon      = 1
	lrLoadFromFile = 0x00000010
)

// menuID 已迁移到 menu_windows.go（导出为 MenuID），这里保留别名便于内部使用。
type menuID = MenuID

// notifyIconData 对应 Windows 的 NOTIFYICONDATAW（只声明用到的字段，顺序不能变）。
type notifyIconData struct {
	Size             uint32
	Wnd              syscall.Handle
	ID               uint32
	Flags            uint32
	CallbackMsg      uint32
	Icon             syscall.Handle
	Tip              [128]uint16
	State            uint32
	StateMask        uint32
	Info             [256]uint16
	TimeoutOrVersion uint32
	InfoTitle        [64]uint16
	InfoFlags        uint32
	GuidItem         [16]byte
	BalloonIcon      syscall.Handle
}

// wndClassEx 对应 WNDCLASSEXW。
type wndClassEx struct {
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

// point 对应 POINT。
type point struct {
	X int32
	Y int32
}

// msg 对应 MSG。
type msg struct {
	Hwnd    syscall.Handle
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      point
}

// Tray 是一个托盘图标 + 菜单。菜单项由回调处理。
type Tray struct {
	hwnd      syscall.Handle
	icon      syscall.Handle
	title     string
	tip       string
	autoStart bool
	running   bool
	stateNote string
	lan       bool
	firewall  bool
	alwaysOn  bool
	onSelect  func(menuID)
	ready     chan struct{}
	quit      chan struct{}
	// threadID 是消息循环所在线程。PostQuitMessage 只作用于「调用线程」的
	// 消息队列，从别的 goroutine 调它等于往错误的队列投递——托盘图标会消失
	// 但进程永远不退出。Quit 用 PostThreadMessage 定向到这个线程。
	threadID uint32
	quitOnce sync.Once
	// quitState 处理「请求早于窗口就绪」的竞态（F10）。Quit 可能在
	// CreateWindowEx 之前被调用，这时没有 hwnd/threadID 可投递。
	quitState quitState
}

// NewTray 创建托盘对象（不启动消息循环）。
func NewTray(title string) *Tray {
	return &Tray{title: title, tip: title, ready: make(chan struct{}), quit: make(chan struct{})}
}

// SetState 更新提示文本与自启状态（可从其它 goroutine 调用）。
func (t *Tray) SetState(tip string, autoStart, running bool, note string) {
	t.tip = tip
	t.autoStart = autoStart
	t.running = running
	t.stateNote = note
	if t.hwnd != 0 {
		t.updateTip()
	}
}

// SetFlags 更新菜单的勾选状态（局域网 / 防火墙 / 常开）。
//
// 与 SetState 分开：SetState 的签名已经在 cmd 层用得很广，改签名会把
// 「运行状态」和「功能开关」混成一个参数列表。
func (t *Tray) SetFlags(lan, firewall, alwaysOn bool) {
	t.lan = lan
	t.firewall = firewall
	t.alwaysOn = alwaysOn
}

// OnSelect 注册菜单点击回调。
func (t *Tray) OnSelect(fn func(menuID)) { t.onSelect = fn }

// Quit 让消息循环退出。可从任意 goroutine 调用多次。
//
// 两条路径都尝试，因为两条都可能失败：
//   - PostThreadMessage 要求目标线程已有消息队列；窗口刚创建、Run 还没
//     跑进 GetMessage 时会返回 ERROR_INVALID_THREAD_ID。
//   - 向窗口投 WM_CLOSE 依赖 hwnd 已就绪，但它是同步销毁，最可靠。
//
// 早先只投 WM_QUIT 并直接 return，一旦那次投递失败（返回值没人看）就再也
// 没有第二次机会：数据库停了、服务还在跑、进程永远不退出。
func (t *Tray) Quit() {
	t.quitOnce.Do(func() { close(t.quit) })

	// 窗口未就绪时 request() 返回 false：把请求记在状态机里，Run 建好
	// 窗口后会补投。早先直接比对 hwnd/threadID，两者为 0 时消息无处可投，
	// 进程就永远留在 GetMessage 里（F10）。
	if !t.quitState.request() {
		return
	}
	t.deliverQuit()
}

// QuitSignal 返回一个在任一退出请求发出时关闭的通道。
//
// kkdesk 的主流程同时等这个通道与 SIGINT/SIGTERM：停止请求可能来自
// `kkapp stop` 的命名事件（它直接调 Quit），也可能来自托盘菜单。只靠
// tray.Run() 返回做判据是不够的——Run 可能因为消息循环异常提前返回，
// 那时 Stop 还没跑，进程看起来「退了」实际子进程还在（F10）。
func (t *Tray) QuitSignal() <-chan struct{} { return t.quit }

// deliverQuit 真正投递退出消息。只有确实投出去才 take()，投递失败时
// 保持「欠投递」，kkdesk 侧的兜底路径仍能看到 pending。
func (t *Tray) deliverQuit() {
	posted := false
	if threadID := t.threadID; threadID != 0 {
		if ret, _, _ := procPostThreadMessageW.Call(uintptr(threadID), wmQuit, 0, 0); ret != 0 {
			posted = true
		}
	}
	if t.hwnd != 0 {
		if ret, _, _ := procPostMessageW.Call(uintptr(t.hwnd), wmClose, 0, 0); ret != 0 {
			posted = true
		}
	}
	if posted {
		t.quitState.take()
		return
	}
	t.quitState.reset()
}

var activeTray *Tray

// Run 启动托盘消息循环（阻塞）。必须在主 goroutine 上调用：
// Windows 的窗口消息队列与线程绑定。
func (t *Tray) Run() error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	activeTray = t

	instance, _, _ := procGetModuleHandleW.Call(0)
	className, err := syscall.UTF16PtrFromString("KeyKeepTrayWindow")
	if err != nil {
		return err
	}
	caption, err := syscall.UTF16PtrFromString(t.title)
	if err != nil {
		return err
	}
	wndProc := syscall.NewCallback(trayWindowProc)

	wc := wndClassEx{
		Size:      uint32(unsafe.Sizeof(wndClassEx{})),
		WndProc:   wndProc,
		Instance:  syscall.Handle(instance),
		ClassName: className,
	}
	if ret, _, callErr := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); ret == 0 {
		return fmt.Errorf("RegisterClassExW 失败: %v", callErr)
	}

	hwnd, _, callErr := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(caption)),
		wsOverlappedWindow,
		0, 0, 0, 0,
		0, 0, instance, 0,
	)
	if hwnd == 0 {
		return fmt.Errorf("CreateWindowExW 失败: %v", callErr)
	}
	t.hwnd = syscall.Handle(hwnd)
	// 记下消息循环线程号：Quit 要往这个线程投 WM_QUIT。
	threadID, _, _ := procGetCurrentThreadId.Call()
	t.threadID = uint32(threadID)

	icon := t.loadIcon()
	t.icon = icon
	t.addIcon()

	// 退出请求可能早于窗口创建（首装初始化 20–60 秒窗口内用户点了退出）。
	// 窗口一就绪就补投，否则消息循环会一直等下去（F10）。
	if t.quitState.ready() {
		t.deliverQuit()
	}

	close(t.ready)

	var m msg
	for {
		ret, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(ret) <= 0 {
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
	t.removeIcon()
	return nil
}

// loadIcon 优先用同目录的 keykeep.ico，找不到就用系统默认图标。
func (t *Tray) loadIcon() syscall.Handle {
	// 优先安装目录下的 keykeep.ico（安装器会放一份）。
	if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "keykeep.ico")
		if _, statErr := os.Stat(candidate); statErr == nil {
			if ptr, convErr := syscall.UTF16PtrFromString(candidate); convErr == nil {
				if handle, _, _ := procLoadImageW.Call(0, uintptr(unsafe.Pointer(ptr)), imageIcon, 0, 0, lrLoadFromFile); handle != 0 {
					return syscall.Handle(handle)
				}
			}
		}
	}
	handle, _, _ := procLoadIconW.Call(0, idiApplication)
	return syscall.Handle(handle)
}

func (t *Tray) addIcon() {
	var data notifyIconData
	data.Size = uint32(unsafe.Sizeof(data))
	data.Wnd = t.hwnd
	data.ID = 1
	data.Flags = nifMessage | nifIcon | nifTip
	data.CallbackMsg = wmTrayIcon
	data.Icon = t.icon
	copyTip(&data.Tip, t.tip)
	procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&data)))
}

// updateTip 更新已存在图标的提示文本。
//
// 必须用 NIM_MODIFY：对同一个 {hwnd, id} 重复 NIM_ADD 会被系统拒绝（返回 FALSE），
// 于是提示文本在图标建好之后就再也不会变——正是「服务已经挂了托盘还显示运行中」的原因。
func (t *Tray) updateTip() {
	var data notifyIconData
	data.Size = uint32(unsafe.Sizeof(data))
	data.Wnd = t.hwnd
	data.ID = 1
	data.Flags = nifTip
	copyTip(&data.Tip, t.tip)
	procShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(&data)))
}

func (t *Tray) removeIcon() {
	var data notifyIconData
	data.Size = uint32(unsafe.Sizeof(data))
	data.Wnd = t.hwnd
	data.ID = 1
	procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&data)))
}

func (t *Tray) showMenu() {
	menu, _, _ := procCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	defer procDestroyMenu.Call(menu)

	appendItem := func(id menuID, label string, flags uint32) {
		text, err := syscall.UTF16PtrFromString(label)
		if err != nil {
			return
		}
		procAppendMenuW.Call(menu, uintptr(mfString|flags), uintptr(id), uintptr(unsafe.Pointer(text)))
	}

	appendItem(MenuOpen, "打开控制台", 0)
	// ADR-013：窗口是默认形态，浏览器是显式入口——所以它是独立一项，
	// 而不是「打开控制台」失败后的兜底。
	appendItem(MenuOpenInBrowser, "在浏览器中打开", 0)
	appendItem(MenuStatus, "查看状态", 0)
	procAppendMenuW.Call(menu, mfSeparator, 0, 0)
	appendItem(MenuCheckUpdate, "检查更新", 0)
	appendItem(MenuRestart, "重启服务", 0)
	autoFlags := uint32(0)
	if t.autoStart {
		autoFlags = mfChecked
	}
	appendItem(MenuToggleAutoStart, "开机自启", autoFlags)
	procAppendMenuW.Call(menu, mfSeparator, 0, 0)
	lanFlags := uint32(0)
	if t.lan {
		lanFlags = mfChecked
	}
	appendItem(MenuToggleLan, "允许局域网访问（需要管理员）", lanFlags)
	fwFlags := uint32(0)
	if t.firewall {
		fwFlags = mfChecked
	}
	appendItem(MenuToggleFirewall, "防火墙已放行 8080/8443", fwFlags|mfGrayed)
	alwaysOnFlags := uint32(0)
	if t.alwaysOn {
		alwaysOnFlags = mfChecked
	}
	appendItem(MenuAlwaysOn, "常开模式（不待机不休眠）", alwaysOnFlags)
	procAppendMenuW.Call(menu, mfSeparator, 0, 0)
	appendItem(MenuOpenLogs, "打开日志目录", 0)
	appendItem(MenuOpenDataDir, "打开数据目录", 0)
	procAppendMenuW.Call(menu, mfSeparator, 0, 0)
	appendItem(MenuQuit, "退出", 0)

	var pt point
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	procSetForegroundWindow.Call(uintptr(t.hwnd))
	procTrackPopupMenu.Call(menu, tpmLeftAlign|tpmRightButton, uintptr(pt.X), uintptr(pt.Y), 0, uintptr(t.hwnd), 0)
}

func trayWindowProc(hwnd, message, wParam, lParam uintptr) uintptr {
	switch uint32(message) {
	case wmTrayIcon:
		if uint32(lParam) == wmRButtonUp || uint32(lParam) == wmLButtonUp {
			if activeTray != nil {
				activeTray.showMenu()
			}
		}
		return 0
	case wmCommand:
		if activeTray != nil && activeTray.onSelect != nil {
			activeTray.onSelect(menuID(wParam & 0xffff))
		}
		return 0
	case wmClose:
		procDestroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		procPostQuitMessage.Call(0)
		return 0
	}
	ret, _, _ := procDefWindowProcW.Call(hwnd, message, wParam, lParam)
	return ret
}

func copyTip(dst *[128]uint16, text string) {
	encoded, err := syscall.UTF16FromString(text)
	if err != nil {
		return
	}
	limit := len(dst) - 1
	if len(encoded) < limit {
		limit = len(encoded)
	}
	copy(dst[:limit], encoded[:limit])
}
