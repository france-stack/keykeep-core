//go:build windows

package desktop

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

// 防火墙规则的固定名字。用固定名字而不是随机名，是因为「撤销」必须能按名字找到它，
// 而且用户可以在「高级安全 Windows Defender 防火墙」里一眼认出这两条。
// 名字刻意不含空格：netsh 参数里就不需要引号，绕开 cmd.exe 不认 \" 转义的坑。
// 旧版本用过带空格的 "KeyKeep Gateway"/"KeyKeep Console"，AddFirewallRules 时一并清理。
const (
	ruleGateway    = "KeyKeepGateway"
	ruleConsole    = "KeyKeepConsole"
	ruleGatewayOld = "KeyKeep Gateway"
	ruleConsoleOld = "KeyKeep Console"
)

// shellExecuteErrorUserCancelled 是 UAC 被用户取消时 ShellExecuteW 的返回码。
const shellExecuteErrorUserCancelled = 1223

// FirewallState 报告两条入站规则是否已存在。
//
// 用 netsh 的退出码判断：规则存在返回 0，不存在返回非 0。
// 这里刻意不解析输出文本——中文/英文/其它语言的输出格式不同。
func FirewallState(gatewayPort, consolePort int) (gw bool, ctl bool) {
	return ruleExists(ruleGateway), ruleExists(ruleConsole)
}

func ruleExists(name string) bool {
	netsh, err := systemExe("netsh.exe")
	if err != nil {
		return false
	}
	cmd := &exec.Cmd{
		Path: netsh,
		Args: []string{netsh, "advfirewall", "firewall", "show", "rule", "name=" + name},
	}
	// 不设置 Stdout/Stderr：输出用不到，丢弃即可。
	if err := cmd.Run(); err != nil {
		return false
	}
	return true
}

// AddFirewallRules 提权添加两条入站放行规则（一次 UAC，两条一起加）。
//
// 规则限定 profile=domain,private 与 remoteip=localsubnet：公用网络（咖啡馆/
// 机场 Wi-Fi）下规则不生效，端口不再对公网环境开放；也把可达范围收窄到本网段。
func AddFirewallRules(gatewayPort, consolePort int) error {
	script := fmt.Sprintf(
		`netsh advfirewall firewall delete rule name=%s & `+
			`netsh advfirewall firewall delete rule name=%s & `+
			`netsh advfirewall firewall add rule name=%s dir=in action=allow protocol=TCP localport=%d profile=domain,private remoteip=localsubnet & `+
			`netsh advfirewall firewall add rule name=%s dir=in action=allow protocol=TCP localport=%d profile=domain,private remoteip=localsubnet`,
		ruleGatewayOld, ruleConsoleOld,
		ruleGateway, gatewayPort, ruleConsole, consolePort)
	return runElevated(script)
}

// RemoveFirewallRules 提权删除两条规则（含旧版本带空格的规则名）。
func RemoveFirewallRules() error {
	script := fmt.Sprintf(
		`netsh advfirewall firewall delete rule name=%s & netsh advfirewall firewall delete rule name=%s & `+
			`netsh advfirewall firewall delete rule name=%s & netsh advfirewall firewall delete rule name=%s`,
		ruleGateway, ruleConsole, ruleGatewayOld, ruleConsoleOld)
	return runElevated(script)
}

// RunElevated 用管理员权限执行一段 cmd 命令。
//
// 参数只来自本包的常量与 NormalizePort 过的端口，不含任何用户输入；
// 因此这里没有走 safeExe 白名单——白名单的职责是校验「安装目录里的哪个二进制」，
// 而 cmd.exe / netsh.exe 是系统目录里的固定程序，由 UAC 提权执行。
func RunElevated(script string) error { return runElevated(script) }

func runElevated(script string) error {
	cmdExe, err := systemExe("cmd.exe")
	if err != nil {
		return err
	}
	verb, err := syscall.UTF16PtrFromString("runas")
	if err != nil {
		return err
	}
	target, err := syscall.UTF16PtrFromString(cmdExe)
	if err != nil {
		return err
	}
	// /c 执行完即退出；/d 跳过 AutoRun；/s 保证引号按原样传递。
	params, err := syscall.UTF16PtrFromString("/d /s /c " + quoteCmdArg(script))
	if err != nil {
		return err
	}
	ret, _, callErr := procShellExecuteW.Call(
		0,
		uintptr(unsafe.Pointer(verb)),
		uintptr(unsafe.Pointer(target)),
		uintptr(unsafe.Pointer(params)),
		0,
		swShownormal,
	)
	if ret <= 32 {
		if ret == shellExecuteErrorUserCancelled {
			return errors.New("已取消管理员授权，规则未添加")
		}
		return fmt.Errorf("提权执行失败（code=%d）: %v", ret, callErr)
	}
	return nil
}

// systemExe 返回 System32 下的系统程序绝对路径。
//
// 优先用 SystemRoot 拼，避免 PATH 污染让它指向别处；Windows 会做文件系统重定向，
// 这里不需要额外处理 SysWOW64。
func systemExe(name string) (string, error) {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	path := filepath.Join(root, "System32", name)
	if err := validateSystemPath(path); err != nil {
		return "", err
	}
	return path, nil
}

// validateSystemPath 确认路径确实落在 System32 下且是普通文件。
//
// 与 safeExe 是同一套「先校验再执行」的思路，只是允许的目录不同：
// safeExe 管安装目录，这里管系统目录。
func validateSystemPath(path string) error {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	base := filepath.Clean(filepath.Join(root, "System32"))
	clean := filepath.Clean(path)
	rel, err := filepath.Rel(base, clean)
	if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return fmt.Errorf("系统程序路径越界：%s", path)
	}
	info, err := os.Stat(clean)
	if err != nil {
		return fmt.Errorf("找不到系统程序 %s: %w", clean, err)
	}
	if info.IsDir() {
		return fmt.Errorf("%s 是目录", clean)
	}
	return nil
}

// quoteCmdArg 把整段命令用双引号包起来交给 cmd /c。
//
// cmd 的引号规则很怪：外层引号会被剥掉，所以整段必须以引号开头和结尾。
// 命令里已经含双引号的规则名，这里把内部引号换成 \" 让 netsh 收到正确参数。
func quoteCmdArg(script string) string {
	escaped := strings.ReplaceAll(script, `"`, `\"`)
	return `"` + escaped + `"`
}

// SetAlwaysOn 设置/取消常开模式：关待机、关休眠。
//
// 交付约束（写进 ADR-012 与部署文档）：桌面版的开机自启是 HKCU Run 项，
// **必须有用户登录**。所以常开模式只是「不睡」，不等于「不用登录」。
// 真正的无人值守需要自动登录或 Windows 服务化（后者本轮不做）。
func SetAlwaysOn(enable bool) error {
	var script string
	if enable {
		script = `powercfg /change standby-timeout-ac 0 & powercfg /change hibernate-timeout-ac 0 & powercfg /hibernate off`
	} else {
		// 恢复默认：30 分钟待机、60 分钟休眠。不重新打开休眠，
		// 因为是否启用休眠属于用户的系统设置，我们只关不主动开。
		script = `powercfg /change standby-timeout-ac 30 & powercfg /change hibernate-timeout-ac 60`
	}
	return runElevated(script)
}
