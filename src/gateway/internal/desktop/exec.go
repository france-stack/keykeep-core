package desktop

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// 允许拉起的可执行文件名白名单。
//
// 所有子进程路径都由本程序从安装目录拼出，但安装目录与 settings.json 都在
// 用户可写范围内，因此执行前必须过白名单 + 目录越界两道检查：
// 名字不在清单里、或解析后跑到安装目录之外，一律拒绝启动。
var allowedBinaries = map[string]struct{}{
	"node.exe":      {},
	"gateway.exe":   {},
	"kkmock.exe":    {},
	"kkconnect.exe": {},
	"kkdesk.exe":    {},
	"initdb.exe":    {},
	"postgres.exe":  {},
	"pg_ctl.exe":    {},
}

// safeExe 校验 dir/name 并返回绝对路径。
//
// 这是启动子进程前唯一的路径入口：白名单 → 目录内 → 普通文件。
// 失败即拒绝，不做「尽力而为」的降级。
func safeExe(dir, name string) (string, error) {
	if !filepath.IsAbs(dir) {
		return "", fmt.Errorf("安装目录必须是绝对路径：%q", dir)
	}
	if _, ok := allowedBinaries[name]; !ok {
		return "", fmt.Errorf("不在允许清单内的可执行文件：%q", name)
	}
	base := filepath.Clean(dir)
	target := filepath.Join(base, name)
	if rel, err := filepath.Rel(base, target); err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return "", fmt.Errorf("可执行文件路径越界：%s", target)
	}
	info, err := os.Stat(target)
	if err != nil {
		return "", fmt.Errorf("找不到 %s：安装包可能不完整", target)
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s 是目录，不是可执行文件", target)
	}
	return target, nil
}

// ResolveExe 是 safeExe 的导出包装，供 cmd 层在启动子进程前做同一套校验。
func ResolveExe(dir, name string) (string, error) { return safeExe(dir, name) }

// command 用绝对路径构造命令。
//
// 刻意不走 exec.Command：它对不带分隔符的名字会做 PATH 查找，等于把「执行哪个
// 二进制」交给环境变量决定。这里 Path 已经是 safeExe 校验过的绝对路径，Args[0]
// 与 Path 一致，任何 PATH 污染都影响不到它。
func command(absPath string, args ...string) *exec.Cmd {
	if !filepath.IsAbs(absPath) {
		// 只有绝对路径才允许；调用方都经过 safeExe，这里是最后一道保险。
		panic("desktop: command 需要绝对路径")
	}
	argv := make([]string, 0, len(args)+1)
	argv = append(argv, absPath)
	argv = append(argv, args...)
	return &exec.Cmd{Path: absPath, Args: argv}
}
