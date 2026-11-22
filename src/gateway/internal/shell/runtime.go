package shell

import (
	"errors"
	"fmt"
	"strings"
)

// ErrRuntimeMissing 表示这台机器没装 WebView2 Evergreen 运行时。
//
// 单独出一个哨兵错误：调用方据此弹「打开下载页 / 用浏览器打开」两项，
// 而不是把它和「窗口创建失败」混成一类，给出一个看不懂的错误框。
var ErrRuntimeMissing = errors.New("未检测到 WebView2 运行时")

// RuntimeClientGUID 是 WebView2 Evergreen 运行时的客户端标识。
const RuntimeClientGUID = "{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}"

// RuntimeProbes 是按优先级排列的检测位置（ADR-013 §2.5）。
//
// 三个位置都要查：64 位安装写 WOW6432Node（因为 Edge 更新器是 32 位组件），
// 系统级与用户级安装各写一处，企业环境可能只留其中一处。
var RuntimeProbes = []RegistryProbe{
	{Root: "HKLM", Path: `SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\` + RuntimeClientGUID},
	{Root: "HKLM", Path: `SOFTWARE\Microsoft\EdgeUpdate\Clients\` + RuntimeClientGUID},
	{Root: "HKCU", Path: `SOFTWARE\Microsoft\EdgeUpdate\Clients\` + RuntimeClientGUID},
}

// RegistryProbe 描述「去注册表的哪个位置读 pv」。
type RegistryProbe struct {
	Root string // HKLM / HKCU
	Path string // 不含根的完整键路径
}

// ValueReader 是只读注册表里一个字符串值的最小接口。
//
// 注入式：检测逻辑与真实注册表解耦，单测可以在 Linux/macOS 上跑
// 「命中 / 未命中 / 键损坏」三种情况，不需要装 WebView2。
type ValueReader interface {
	// ReadString 返回值内容；键或值不存在时 found=false 且 err=nil。
	// 只有真实的读取失败（权限、类型不符）才返回 err。
	ReadString(root, path, name string) (value string, found bool, err error)
}

// DetectRuntime 判断本机是否装有 WebView2 运行时，返回检测到的版本号。
//
// 判据是 pv 值非空且不是 "0.0.0.0"：Edge 更新器把「已安装」记成版本号，
// 卸载后有时留下一个空键——只查键存在会把卸载过的机器误判成可用。
//
// 单个位置读取失败不影响结论：企业策略常把 HKCU 锁掉，而 HKLM 仍然可读，
// 这里要回答的是「有没有」，不是「三处是不是都健康」。
func DetectRuntime(reader ValueReader) (version string, err error) {
	if reader == nil {
		return "", errors.New("未提供注册表读取器")
	}
	var firstErr error
	for _, probe := range RuntimeProbes {
		value, found, readErr := reader.ReadString(probe.Root, probe.Path, "pv")
		if readErr != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("读取 %s\\%s 失败: %w", probe.Root, probe.Path, readErr)
			}
			continue
		}
		if !found {
			continue
		}
		trimmed := strings.TrimSpace(value)
		if trimmed == "" || trimmed == "0.0.0.0" {
			continue
		}
		return trimmed, nil
	}
	// 一处都没读到版本号：如果全程有读取错误，把错误带出去便于排障；
	// 否则就是干净的「没装」。用 %w 双包一次，两个错误都能 errors.Is 到：
	// 调用方按 ErrRuntimeMissing 决定弹哪个对话框，排障时又需要原始原因。
	if firstErr != nil {
		return "", fmt.Errorf("%w：%w", ErrRuntimeMissing, firstErr)
	}
	return "", ErrRuntimeMissing
}

// ShellScript 描述一次窗口启动的参数，供 kkdesk 组装。
//
// 抽出来是为了让「什么时候该开壳、什么时候回退」能在单测里断言：
// 决策逻辑（本结构 + Presenter）与 Win32 调用分离。
type ShellScript struct {
	// URL 是窗口要加载的控制台地址（回环）。
	URL string
	// Origin 是导航白名单的基准，与 URL 同源。
	Origin string
	// ProfileDir 是 WebView2 用户数据目录。
	ProfileDir string
	// Title 是窗口标题。
	Title string
	// Width / Height 是初始尺寸。
	Width, Height int
	// IconPath 是窗口图标（可空：找不到时用系统默认图标）。
	IconPath string
	// DevTools 是否打开开发者工具（仅调试构建）。
	DevTools bool
}

// ShellTitle 是窗口标题。验收项逐条对照它。
const ShellTitle = "KeyKeep 控制台"

// DefaultShellWidth / DefaultShellHeight 是窗口默认尺寸（ADR-013 §2.3）。
const (
	DefaultShellWidth  = 1280
	DefaultShellHeight = 800
)
