//go:build !windows

// 非 Windows 桩：CI 在 ubuntu-latest 上跑 go build ./... ，没有这些符号整包编不过。
//
// 桌面角色只在 Windows 交付，这些函数不该被业务调用；返回「不可用」而不是
// panic，是为了误调用时能读到原因，并且让 Presenter 自然走到浏览器回退分支。

package shell

import "errors"

var errWindowsOnly = errors.New("WebView2 原生窗口仅 Windows 支持")

// PlatformWindow 在非 Windows 上返回 nil：Presenter 会据此回退浏览器。
func PlatformWindow(openExternal func(string) error) Window { return nil }

// PlatformDetector 在非 Windows 上报告运行时缺失。
func PlatformDetector() func() (string, error) {
	return func() (string, error) { return "", ErrRuntimeMissing }
}

// PlatformNotifier 在非 Windows 上不弹对话框，直接选择浏览器回退。
func PlatformNotifier() func(missing bool) bool {
	return func(bool) bool { return true }
}

// PlatformOpenBrowser 在非 Windows 上没有系统默认浏览器可调。
func PlatformOpenBrowser(url string) error { return errWindowsOnly }

// PlatformHardener 在非 Windows 上不需要 ACL 收紧。
func PlatformHardener(dir string) error { return nil }

// PlatformAvailable 报告本平台是否具备原生窗口能力。
func PlatformAvailable() bool { return false }
