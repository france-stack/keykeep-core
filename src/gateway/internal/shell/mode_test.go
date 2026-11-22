package shell

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestResolveModePrecedence(t *testing.T) {
	cases := []struct {
		setting string
		env     string
		want    Mode
	}{
		// 默认：WebView2。
		{"", "", ModeWebView2},
		{"webview2", "", ModeWebView2},
		// settings.json 切浏览器。
		{"browser", "", ModeBrowser},
		// 环境变量覆盖设置。
		{"webview2", "browser", ModeBrowser},
		{"browser", "webview2", ModeWebView2},
		// 大小写与空白容忍：用户在快捷方式里手填的开关不该因为空格失效。
		{"  BROWSER  ", "", ModeBrowser},
		{"", " WebView2 ", ModeWebView2},
		// 无法识别的值按默认处理，不报错。
		{"edge", "", ModeWebView2},
		{"", "chrome", ModeWebView2},
		{"garbage", "garbage", ModeWebView2},
	}
	for _, c := range cases {
		if got := ResolveMode(c.setting, c.env); got != c.want {
			t.Fatalf("ResolveMode(%q, %q) = %q，期望 %q", c.setting, c.env, got, c.want)
		}
	}
}

func TestDevToolsEnabledOnlyAcceptsOne(t *testing.T) {
	// 只认 "1"：误配成 true 不该把调试面开在生产机上。
	for _, value := range []string{"", "true", "TRUE", "yes", "on", "0", " 2 "} {
		if DevToolsEnabled(value) {
			t.Fatalf("DevToolsEnabled(%q) 应为 false", value)
		}
	}
	if !DevToolsEnabled("1") || !DevToolsEnabled(" 1 ") {
		t.Fatal("DevToolsEnabled(\"1\") 应为 true")
	}
}

func TestProfileDirUsesLocalAppData(t *testing.T) {
	dir := ProfileDir(`C:\Users\tester\AppData\Local`)
	want := filepath.Join(`C:\Users\tester\AppData\Local`, "KeyKeep", "webview2-profile")
	if dir != want {
		t.Fatalf("ProfileDir = %q，期望 %q", dir, want)
	}
	for _, token := range []string{"KeyKeep", "webview2-profile"} {
		if !strings.Contains(dir, token) {
			t.Fatalf("目录名应包含 %q，实际 %q", token, dir)
		}
	}
}

func TestProfileDirFallsBackWithoutLocalAppData(t *testing.T) {
	dir := ProfileDir("   ")
	if dir == "" {
		t.Fatal("LocalAppData 为空时应回退到可写目录，实际返回空串")
	}
	if !strings.HasSuffix(strings.ReplaceAll(dir, "\\", "/"), "KeyKeep/webview2-profile") {
		t.Fatalf("回退目录结构不对: %q", dir)
	}
}

func TestProfileDirIsNotUnderProgramFiles(t *testing.T) {
	// 用户数据目录必须在用户可写区：放进程序目录会在 Program Files 下不可写，
	// 或随版本清理被删掉（会话 cookie 就丢了）。
	dir := ProfileDir(`C:\Users\tester\AppData\Local`)
	if strings.Contains(strings.ToLower(dir), "program files") {
		t.Fatalf("用户数据目录不该在 Program Files 下: %q", dir)
	}
	_ = runtime.GOOS
}
