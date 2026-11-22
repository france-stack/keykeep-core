//go:build !windows

package desktop

import "errors"

// 非 Windows 桩：命名事件只在 Windows 上存在（见 show_console_windows.go）。
// CI 在 ubuntu-latest 上编整包时需要这些符号。

// PrepareShowConsoleSignal 在非 Windows 上不可用。
func PrepareShowConsoleSignal() (*StopSignal, error) {
	return nil, errors.New("显示控制台通知仅 Windows 支持")
}

// RequestShowConsole 在非 Windows 上总是返回 false（没有实例可通知）。
func RequestShowConsole() bool { return false }

// ErrNoConsoleWindow 表示当前没有可聚焦的窗口。
var ErrNoConsoleWindow = errors.New("没有可显示的控制台窗口")
