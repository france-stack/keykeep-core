//go:build windows

package desktop

import (
	"errors"
	"fmt"
	"syscall"
	"unsafe"
)

// 「显示控制台」的跨进程通知（ADR-013 §2.3）。
//
// 用户第二次双击桌面图标时，外壳 kkapp 发现控制台已经活着，就该让已有实例
// 把窗口提到前台——而不是再开一个实例（会撞端口）或者只弹一句「已在运行」。
//
// 用命名事件而不是新增网络端点：控制台是本机回环服务，但「聚焦窗口」是
// 进程间的一次性动作，走内核对象不需要认证、不占端口、不进入任何 HTTP 日志。
// 命名模式对齐现有的 Local\KeyKeep.kkdesk-singleton。
const showConsoleEventName = `Local\KeyKeep.ShowConsole`

var procCreateEventShow = kernel32.NewProc("CreateEventW")

// PrepareShowConsoleSignal 创建（或接管）显示事件，并复位成「未请求」。
//
// 复位不能省：事件是内核命名对象，上一个实例被 SetEvent 叫过之后，
// 新实例打开到的可能就是已置位状态，会立刻自我聚焦一次。
func PrepareShowConsoleSignal() (*StopSignal, error) {
	name, err := syscall.UTF16PtrFromString(showConsoleEventName)
	if err != nil {
		return nil, err
	}
	handle, _, callErr := procCreateEventShow.Call(
		0, // 默认安全属性
		1, // manual reset：置位后保持，等待方不会错过
		0, // 初始未置位
		uintptr(unsafe.Pointer(name)),
	)
	if handle == 0 {
		return nil, fmt.Errorf("创建显示事件失败: %v", callErr)
	}
	procResetEvent.Call(handle)
	return &StopSignal{handle: syscall.Handle(handle)}, nil
}

// RequestShowConsole 通知正在运行的实例「把控制台窗口提到前台」。
//
// 只打开不创建：实例还没起来时不该由外壳凭空造一个事件，否则随后启动的
// 实例会拿到一个已置位的事件并立刻弹窗。
// 返回 false 表示没有实例在监听。
func RequestShowConsole() bool {
	name, err := syscall.UTF16PtrFromString(showConsoleEventName)
	if err != nil {
		return false
	}
	handle, _, _ := procOpenEventW.Call(eventModifyState, 0, uintptr(unsafe.Pointer(name)))
	if handle == 0 {
		return false
	}
	defer procCloseHandle.Call(handle)
	ret, _, _ := procSetEvent.Call(handle)
	return ret != 0
}

// ErrNoConsoleWindow 表示当前没有可聚焦的窗口（浏览器模式或尚未创建）。
var ErrNoConsoleWindow = errors.New("没有可显示的控制台窗口")
