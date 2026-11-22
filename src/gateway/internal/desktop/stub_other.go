//go:build !windows

// 本文件是非 Windows 平台（CI 在 ubuntu-latest 上跑 go build ./...）的构建桩。
//
// 桌面包的其余代码（布局、迁移、更新校验、设置）与平台无关，可以被 Linux/macOS
// 构建与测试；只有托盘、注册表、目录联接、提权这些是 Windows 专有。真实实现在
// *_windows.go 里，这里只提供同名符号让整包能编译通过。
//
// 这些函数在非 Windows 上永远不该被业务调用：desktop 角色只在 Windows 交付。
// 返回错误而不是 panic，是为了「误调用时给出可读原因」而不是崩掉 shell。
package desktop

import (
	"errors"
	"fmt"
	"os"
	"runtime"
)

var errWindowsOnly = errors.New("仅 Windows 支持")

// MenuID 是托盘菜单项的命令标识（Windows 实现见 menu_windows.go）。
type MenuID uint32

// 菜单项常量必须与 menu_windows.go 保持一致；跨平台代码可能引用它们。
const (
	MenuOpen            MenuID = 1000
	MenuStatus          MenuID = 1001
	MenuRestart         MenuID = 1002
	MenuCheckUpdate     MenuID = 1003
	MenuToggleAutoStart MenuID = 1004
	MenuOpenLogs        MenuID = 1005
	MenuOpenDataDir     MenuID = 1006
	MenuQuit            MenuID = 1007
	MenuToggleLan       MenuID = 1008
	MenuToggleFirewall  MenuID = 1009
	MenuAlwaysOn        MenuID = 1010
	MenuOpenInBrowser   MenuID = 1011
)

// Tray 在非 Windows 上是空壳：Run 立即返回，其余是空操作。
type Tray struct {
	title     string
	tip       string
	autoStart bool
	running   bool
	stateNote string
	lan       bool
	firewall  bool
	alwaysOn  bool
	onSelect  func(MenuID)
}

func NewTray(title string) *Tray { return &Tray{title: title, tip: title} }

func (t *Tray) SetState(tip string, autoStart, running bool, note string) {
	t.tip = tip
	t.autoStart = autoStart
	t.running = running
	t.stateNote = note
}

func (t *Tray) OnSelect(fn func(MenuID)) { t.onSelect = fn }

func (t *Tray) SetFlags(lan, firewall, alwaysOn bool) {
	t.lan = lan
	t.firewall = firewall
	t.alwaysOn = alwaysOn
}

func (t *Tray) Quit() {}

// QuitSignal 在非 Windows 上永不触发：这些平台没有托盘，退出走 SIGINT/TERM。
func (t *Tray) QuitSignal() <-chan struct{} {
	ch := make(chan struct{})
	return ch
}

func (t *Tray) Run() error {
	return fmt.Errorf("托盘界面%s（当前平台 %s）", errWindowsOnly, runtime.GOOS)
}

// linkDir 在非 Windows 上用普通符号链接。
func linkDir(linkPath, target string) error {
	if _, err := os.Stat(target); err != nil {
		return err
	}
	return os.Symlink(target, linkPath)
}

func SetAutoStart(enable bool, target string) error {
	return fmt.Errorf("开机自启%s", errWindowsOnly)
}

func AutoStartEnabled() bool { return false }

func OpenBrowser(url string) error {
	return fmt.Errorf("打开浏览器%s", errWindowsOnly)
}

func OpenFolder(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return fmt.Errorf("打开目录%s", errWindowsOnly)
}

func LaunchDetached(exe string, args ...string) error {
	return fmt.Errorf("分离启动%s", errWindowsOnly)
}

// 防火墙与提权：非 Windows 上不存在，返回可读错误。

func FirewallState(gatewayPort, consolePort int) (gw, ctl bool) { return false, false }

func AddFirewallRules(gatewayPort, consolePort int) error {
	return fmt.Errorf("防火墙规则%s", errWindowsOnly)
}

func RemoveFirewallRules() error { return fmt.Errorf("防火墙规则%s", errWindowsOnly) }

func RunElevated(script string) error { return fmt.Errorf("提权执行%s", errWindowsOnly) }

// SetAlwaysOn 关待机与休眠（Windows 用 powercfg）。
func SetAlwaysOn(enable bool) error { return fmt.Errorf("常开模式%s", errWindowsOnly) }
