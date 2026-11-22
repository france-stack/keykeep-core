//go:build windows

package desktop

import (
	"fmt"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// 桌面实例的「优雅退出」信号。
//
// 为什么不用 os.Interrupt / taskkill：Windows 没有跨进程的 SIGTERM，而
// taskkill /F 会让 kkdesk 来不及停 PostgreSQL，留下一个占着端口、还持有数据
// 目录锁的孤儿 postgres——下次启动直接失败。安装与升级前必须先走这里。
//
// 用命名事件而不是「轮询删除一个文件」：事件是内核对象，SetEvent 立即唤醒
// 等待方，不产生磁盘 IO，也不会留下会被下次启动误读的标志文件。
const stopEventName = `Local\KeyKeepDesktopStop`

var (
	procCreateEventW             = kernel32.NewProc("CreateEventW")
	procOpenEventW               = kernel32.NewProc("OpenEventW")
	procSetEvent                 = kernel32.NewProc("SetEvent")
	procResetEvent               = kernel32.NewProc("ResetEvent")
	procWaitForSingleObj         = kernel32.NewProc("WaitForSingleObject")
	eventModifyState     uintptr = 0x0002
)

// StopSignal 持有停止事件句柄的生命周期。
type StopSignal struct {
	handle syscall.Handle
}

// PrepareStopSignal 创建（或接管）停止事件，并把它复位成「未请求停止」。
//
// 复位这一步不能省：事件是内核命名对象，如果上一个实例是被 SetEvent 叫停的，
// 句柄还没释放前新实例打开到的就是已置位状态，会立刻自我退出。
func PrepareStopSignal() (*StopSignal, error) {
	name, err := syscall.UTF16PtrFromString(stopEventName)
	if err != nil {
		return nil, err
	}
	handle, _, callErr := procCreateEventW.Call(
		0, // 默认安全属性
		1, // manual reset：置位后保持，等待方不会错过
		0, // 初始未置位
		uintptr(unsafe.Pointer(name)),
	)
	if handle == 0 {
		return nil, fmt.Errorf("创建停止事件失败: %v", callErr)
	}
	procResetEvent.Call(handle)
	return &StopSignal{handle: syscall.Handle(handle)}, nil
}

// Wait 阻塞直到有人请求停止。
func (s *StopSignal) Wait() {
	if s == nil || s.handle == 0 {
		return
	}
	procWaitForSingleObj.Call(uintptr(s.handle), 0xFFFFFFFF) // INFINITE
}

// Reset 把事件复位成「未置位」。
//
// 停止事件不需要它（置位之后就退出），但「显示控制台」事件是重复使用的：
// manual reset 事件不复位的话，一次点击会让等待方立刻再次返回。
func (s *StopSignal) Reset() {
	if s == nil || s.handle == 0 {
		return
	}
	procResetEvent.Call(uintptr(s.handle))
}

// Close 释放事件句柄。
func (s *StopSignal) Close() {
	if s == nil || s.handle == 0 {
		return
	}
	procCloseHandle.Call(uintptr(s.handle))
	s.handle = 0
}

// RequestStop 让正在运行的桌面实例优雅退出。没有实例在跑时返回 false。
//
// 这里只打开不创建：如果实例还没起来，就不该由安装器凭空创建一个事件，
// 否则随后启动的实例会拿到一个已置位的事件并立即退出。
func RequestStop() bool {
	name, err := syscall.UTF16PtrFromString(stopEventName)
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

// DesktopRunning 判断桌面实例是否还活着：控制台端口还能连上就认为在跑。
//
// 用端口而不是 FindWindow/进程名：控制台是最后一个就绪、最先停止的对外面，
// 它没了就意味着实例正在收尾或已经退出。调用方据此决定是否继续等待。
func DesktopRunning(controlURL string) bool {
	if controlURL == "" {
		return false
	}
	return WaitHTTP(strings.TrimRight(controlURL, "/")+"/login", 700*time.Millisecond) == nil
}
