package shell

import (
	"os"
	"path/filepath"
	"strings"
)

// Mode 是控制台的呈现方式。
type Mode string

const (
	// ModeWebView2 在 kkdesk 进程内的 WebView2 窗口里呈现控制台（ADR-013 默认）。
	ModeWebView2 Mode = "webview2"
	// ModeBrowser 保留 ADR-011 时代的旧行为：交给系统默认浏览器。
	ModeBrowser Mode = "browser"
)

// WebView2DownloadURL 是微软的 Evergreen 引导器地址。
//
// 用它而不是直链安装包：直链会随版本变化，引导器永远指向当前受支持的运行时。
const WebView2DownloadURL = "https://go.microsoft.com/fwlink/p/?LinkId=2124703"

// EnvShellMode 是 kill-switch 的环境变量名。
//
// 为什么要有两套开关：settings.json 是持久的（用户改一次就一直生效），
// 环境变量是一次性的（排障、或者快捷方式上加一行就能验证「是不是壳的问题」）。
// 两者都出现时环境变量优先——它能覆盖一份用户不敢改的配置。
const EnvShellMode = "KEYKEEP_SHELL_MODE"

// EnvDevTools 是调试构建里打开 DevTools 的开关。
//
// 默认关闭：控制台页面里带着会话与 Key 相关操作，一个随手可开的
// DevTools 等于给了任何能碰到这台机器的人改写页面逻辑的能力。
const EnvDevTools = "KEYKEEP_SHELL_DEVTOOLS"

// ResolveMode 决定本次运行用哪种呈现方式。
//
// 取值规则：环境变量 > settings.json > 默认 webview2。
// 无法识别的值按默认处理，并且不报错——一个拼错的开关不该让桌面版起不来。
func ResolveMode(setting, envOverride string) Mode {
	switch strings.ToLower(strings.TrimSpace(envOverride)) {
	case string(ModeBrowser):
		return ModeBrowser
	case string(ModeWebView2):
		return ModeWebView2
	}
	if strings.ToLower(strings.TrimSpace(setting)) == string(ModeBrowser) {
		return ModeBrowser
	}
	return ModeWebView2
}

// DevToolsEnabled 报告本次运行是否打开 DevTools。
//
// 只认 "1"：任何其它值（包括 "true"）都当没设置，避免误配把调试面开在生产机上。
func DevToolsEnabled(env string) bool {
	return strings.TrimSpace(env) == "1"
}

// ProfileDir 返回 WebView2 用户数据目录。
//
// 放在 LocalAppData 而不是安装目录：安装目录在 Program Files 或
// LOCALAPPDATA\KeyKeep\app\<版本>\ 下，前者不可写、后者随升级被清理，
// 而 cookie/会话必须跨版本保留（重开窗口免登录）。
func ProfileDir(localAppData string) string {
	root := strings.TrimSpace(localAppData)
	if root == "" {
		// 非 Windows 或环境变量缺失时的兜底：用户配置目录。
		if dir, err := os.UserConfigDir(); err == nil {
			root = dir
		} else {
			root = os.TempDir()
		}
	}
	return filepath.Join(root, "KeyKeep", "webview2-profile")
}
