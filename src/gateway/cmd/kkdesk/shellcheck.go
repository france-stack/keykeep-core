package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/keykeep/gateway/internal/desktop"
	"github.com/keykeep/gateway/internal/shell"
)

// shellCheckVector 是一条导航判定的期望值。
//
// 自检用固定向量而不是随机输入：它要回答的是「这台机器上装着的这个二进制，
// 现在会不会把外链放进窗口」，答案必须可复现、可对着源码读。
type shellCheckVector struct {
	URI  string         `json:"uri"`
	Want shell.Decision `json:"want"`
	Got  shell.Decision `json:"got"`
	OK   bool           `json:"ok"`
}

// shellCheckReport 是 `kkdesk shell-check` 的输出。
type shellCheckReport struct {
	Mode         string             `json:"mode"`
	ModeSource   string             `json:"modeSource"`
	ControlURL   string             `json:"controlUrl"`
	Origin       string             `json:"origin"`
	Runtime      string             `json:"runtime"`
	RuntimeError string             `json:"runtimeError,omitempty"`
	ProfileDir   string             `json:"profileDir"`
	DevTools     bool               `json:"devtools"`
	WindowReady  bool               `json:"windowReady"`
	Vectors      []shellCheckVector `json:"vectors"`
	Mismatches   int                `json:"mismatches"`
}

// runShellCheck 是 ADR-013 的现场自检命令，也是验收项 T-51/T-52 的证据来源。
//
// 为什么做成产品命令而不是只写单测：单测证明「源码逻辑对」，这条命令证明
// 「用户机器上正在跑的这个 exe 逻辑对」——打包、注入环境变量、settings.json
// 这三处都可能把结论改掉，只有对着真实二进制跑一遍才算验证过。
func runShellCheck(layout *desktop.Layout, asJSON, strict bool) int {
	report := buildShellCheck(layout)

	if asJSON {
		encoded, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "错误：序列化自检结果失败: %v\n", err)
			return 1
		}
		fmt.Println(string(encoded))
	} else {
		printShellCheck(report)
	}
	if strict && report.Mismatches > 0 {
		return 1
	}
	return 0
}

func buildShellCheck(layout *desktop.Layout) shellCheckReport {
	settings, exists, err := layout.LoadSettings()
	if err != nil {
		settings = desktop.DefaultSettings()
		exists = false
	}
	controlURL := shell.NormalizeControlURL(settings.ControlURL(), settings.ControlPort)
	policy, policyErr := shell.NewPolicy(controlURL)
	if policyErr != nil {
		policy, _ = shell.NewPolicy(shell.NormalizeControlURL("", desktop.DefaultControlPort))
		controlURL = policy.Origin()
	}

	settingMode := ""
	if exists {
		settingMode = settings.UIShell
	}
	envMode := os.Getenv(shell.EnvShellMode)
	mode := shell.ResolveMode(settingMode, envMode)
	modeSource := "默认（settings.json 未设置 uiShell）"
	switch {
	case strings.TrimSpace(envMode) != "":
		modeSource = shell.EnvShellMode + " 环境变量"
	case strings.EqualFold(strings.TrimSpace(settingMode), string(mode)):
		modeSource = "settings.json uiShell"
	}

	runtimeVersion, runtimeErr := shell.PlatformDetector()()
	report := shellCheckReport{
		Mode:        string(mode),
		ModeSource:  modeSource,
		ControlURL:  controlURL,
		Origin:      policy.Origin(),
		Runtime:     runtimeVersion,
		ProfileDir:  shell.ProfileDir(os.Getenv("LOCALAPPDATA")),
		DevTools:    shell.DevToolsEnabled(os.Getenv(shell.EnvDevTools)),
		WindowReady: shell.PlatformAvailable(),
	}
	if runtimeErr != nil {
		report.RuntimeError = runtimeErr.Error()
	}

	// 固定的三类向量：同源放行、外部转交浏览器、可执行伪协议拒绝。
	// 地址按当前控制台端口拼，端口改了向量跟着改，判定逻辑不变。
	vectors := []struct {
		uri  string
		want shell.Decision
	}{
		{report.ControlURL + "/login", shell.Allow},
		{report.ControlURL + "/admin/keys?tab=1", shell.Allow},
		{"https://example.com/docs", shell.OpenExternal},
		{"http://192.0.2.10:9/", shell.OpenExternal},
		{"file:///C:/Windows/win.ini", shell.OpenExternal},
		{"javascript:alert(1)", shell.Block},
		{"data:text/html,<script>alert(1)</script>", shell.Block},
		{"", shell.Block},
	}
	for _, vector := range vectors {
		got := policy.Classify(vector.uri)
		ok := got == vector.want
		if !ok {
			report.Mismatches++
		}
		report.Vectors = append(report.Vectors, shellCheckVector{
			URI: vector.uri, Want: vector.want, Got: got, OK: ok,
		})
	}
	return report
}

func printShellCheck(report shellCheckReport) {
	fmt.Printf("控制台模式    %s（来源：%s）\n", report.Mode, report.ModeSource)
	fmt.Printf("控制台地址    %s\n", report.ControlURL)
	fmt.Printf("导航白名单    %s\n", report.Origin)
	if report.RuntimeError != "" {
		fmt.Printf("WebView2      ！不可用：%s\n", report.RuntimeError)
	} else {
		fmt.Printf("WebView2      %s\n", report.Runtime)
	}
	fmt.Printf("用户数据目录  %s\n", report.ProfileDir)
	fmt.Printf("DevTools      %v\n", report.DevTools)
	fmt.Printf("窗口能力      %v\n", report.WindowReady)
	fmt.Println("导航判定：")
	for _, vector := range report.Vectors {
		mark := "✓"
		if !vector.OK {
			mark = "✗"
		}
		fmt.Printf("  %s %-12s → %-12s（期望 %s）%s\n",
			mark, vector.Got, vector.Want, vector.Want, vector.URI)
	}
	if report.Mismatches == 0 {
		fmt.Println("结论：导航白名单与模式解析全部符合预期。")
	} else {
		fmt.Printf("结论：%d 条判定与预期不符——请勿把控制台交到这个二进制上。\n", report.Mismatches)
	}
}
