//go:build windows

package desktop

import (
	"fmt"
	"syscall"
	"unsafe"
)

// 单实例守卫的名字。Local\ 前缀 = 每登录会话一个命名空间，够用（KeyKeep
// 只在交互会话里跑）；不加 Global\ 免得跨会话误伤多用户的并行调试。
const singleInstanceMutexName = `Local\KeyKeep.kkdesk-singleton`

var (
	procCreateMutexW = kernel32.NewProc("CreateMutexW")
	procOpenMutexW   = kernel32.NewProc("OpenMutexW")
)

const (
	errAlreadyExists = 183 // ERROR_ALREADY_EXISTS
	synchronize      = 0x00200000
	mbOK             = 0x00000000
	mbIconInfo       = 0x00000040
	mbSetForeground  = 0x00010000
)

var procMessageBoxW = user32.NewProc("MessageBoxW")

// TryHoldSingleInstance 尝试持有单实例互斥体。
//
// 为什么放在 kkdesk 而不是外壳：首装初始化（建库、迁移）要 20–60 秒，这期间
// 控制台 HTTP 还没就绪，外壳只能靠「互斥体是否存在」判断有没有实例在启动。
// 互斥体随 kkdesk 进程生灭，崩溃/退出后自动释放，不需要手工清理。
// alreadyRunning=true 时调用方应当直接退出（启动路径），一次性子命令
// （stop/status/reset-admin/update）不经过这条路径，不受影响。
func TryHoldSingleInstance() (release func(), alreadyRunning bool, err error) {
	name, nameErr := syscall.UTF16PtrFromString(singleInstanceMutexName)
	if nameErr != nil {
		return func() {}, false, nameErr
	}
	handle, _, callErr := procCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(name)))
	if handle == 0 {
		return func() {}, false, fmt.Errorf("CreateMutexW 失败: %v", callErr)
	}
	if errno, ok := callErr.(syscall.Errno); ok && errno == errAlreadyExists {
		_ = syscall.CloseHandle(syscall.Handle(handle))
		return func() {}, true, nil
	}
	return func() { _ = syscall.CloseHandle(syscall.Handle(handle)) }, false, nil
}

// MutexExists 探测是否已有 kkdesk 实例（含正在首启初始化的）。外壳在拉起
// 子进程前用它防叠实例。
func MutexExists() bool {
	name, nameErr := syscall.UTF16PtrFromString(singleInstanceMutexName)
	if nameErr != nil {
		return false
	}
	handle, _, _ := procOpenMutexW.Call(synchronize, 0, uintptr(unsafe.Pointer(name)))
	if handle != 0 {
		_ = syscall.CloseHandle(syscall.Handle(handle))
		return true
	}
	return false
}

// MessageBoxInfo 弹一个信息提示框。外壳是后台拉起 kkdesk 的，首启的
// 20–60 秒里桌面上一无所有，双击的人需要一句「正在启动」的反馈。
//
// 参数顺序容易写反：MessageBoxW 的第二个参数是正文，第三个才是标题
// （hWnd, lpText, lpCaption, uType）。早先这里把标题传给了正文，结果对话框
// 的标题栏变成整段说明文字、正文空白——服务启动失败时用户看到的正是这样
// 一个畸形框。
func MessageBoxInfo(title, text string) {
	tp, _ := syscall.UTF16PtrFromString(title)
	mp, _ := syscall.UTF16PtrFromString(text)
	procMessageBoxW.Call(0,
		uintptr(unsafe.Pointer(mp)), // lpText
		uintptr(unsafe.Pointer(tp)), // lpCaption
		uintptr(mbOK|mbIconInfo|mbSetForeground))
}
