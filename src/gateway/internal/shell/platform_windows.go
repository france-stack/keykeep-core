//go:build windows

package shell

import (
	"strings"

	"github.com/jchv/go-webview2/webviewloader"
)

// PlatformWindow 返回 Windows 上的原生窗口实现。
//
// openExternal 是导航白名单的转交出口：窗口里拦下的外链与用户从托盘点
// 「在浏览器中打开」走的是同一个函数，行为不会两处不一致。
func PlatformWindow(openExternal func(string) error) Window {
	return newPlatformWindow(openExternal)
}

// PlatformDetector 返回探测 WebView2 运行时的函数。
//
// 优先问 WebView2 自己的加载器（webviewloader.GetInstalledVersion）：它内嵌
// WebView2Loader.dll，从内存加载后直接回答「这个环境能不能建起来」，比查注册表
// 权威。这一步必须做，是因为库在创建环境失败时会 log.Fatalf 直接退出进程——
// 也就是说「先探测、再建窗」是硬顺序，探测错了就没有回退的机会。
// 加载器不可用时退回注册表三处探测。
func PlatformDetector() func() (string, error) {
	return func() (string, error) {
		version, err := webviewloader.GetInstalledVersion()
		if err == nil {
			if strings.TrimSpace(version) != "" {
				return strings.TrimSpace(version), nil
			}
			return "", ErrRuntimeMissing
		}
		return DetectRuntime(RegistryReader{})
	}
}

// PlatformNotifier 返回运行时缺失时的对话框。true = 改用浏览器打开。
func PlatformNotifier() func(missing bool) bool {
	return MessageBoxFallback
}

// PlatformOpenBrowser 用系统默认浏览器打开 URL。
func PlatformOpenBrowser(url string) error { return OpenExternalURL(url) }

// PlatformHardener 收紧 WebView2 用户数据目录的 ACL。
func PlatformHardener(dir string) error { return HardenProfileACL(dir) }

// PlatformAvailable 报告本平台是否具备原生窗口能力。
func PlatformAvailable() bool { return true }
