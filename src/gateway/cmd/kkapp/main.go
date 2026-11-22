// Command kkapp 是桌面版的稳定外壳：它自己不更新，只负责读 current.txt
// 并拉起对应版本的 kkdesk.exe。
//
// 为什么要这一层：Windows 不允许替换正在运行的可执行文件。如果更新要覆盖
// 启动器本身，就必须先退出；而「退出后由谁再拉起来」不能是刚刚被替换的那个文件。
// 外壳只有几百 KB 且功能极少，几乎不需要更新；KK 的全部功能更新都发生在
// app\<版本>\ 目录里。
//
// 子命令：
//
//	kkapp            启动当前版本（无 current 时按 app\ 下最新版本启动）
//	kkapp version    显示外壳版本
//	kkapp list       列出已安装版本与当前指针
//	kkapp rollback   切到上一版本（自动回滚的兜底手段）
package main

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/keykeep/gateway/internal/desktop"
	"github.com/keykeep/gateway/internal/version"
)

func main() {
	log.SetFlags(log.Ltime)
	log.SetPrefix("kkapp ")

	command := "run"
	args := os.Args[1:]
	if len(args) > 0 {
		switch args[0] {
		case "version", "-v", "--version":
			command = "version"
			args = args[1:]
		case "list", "rollback", "update", "stop", "reset-admin", "help", "-h", "--help",
			"shell-check", "backup":
			// shell-check / backup 是「查询现状」的一次性命令，必须直通 kkdesk：
			// 走 runCurrent 会被「已在运行」的护栏吃掉，把自检变成「聚焦窗口」，
			// 输出一个字都看不到。它们都是只读或幂等的，不需要先停服务。
			command = args[0]
			args = args[1:]
		}
	}
	// 归一化 help 的几种写法，避免落到 default 分支再退出码 2。
	switch command {
	case "-h", "--help":
		command = "help"
	case "-v", "--version":
		command = "version"
	}

	layout, err := desktop.NewLayout()
	if err != nil {
		fatal(err)
	}

	switch command {
	case "run":
		// 外壳只认自己的子命令；多余参数原样交给 kkdesk（例如 --tray）。
		if err := runCurrent(layout, args); err != nil {
			fatal(err)
		}
	case "version":
		fmt.Printf("kkapp %s\n", version.String())
	case "list":
		if err := listVersions(layout); err != nil {
			fatal(err)
		}
	case "update":
		if err := update(layout, args); err != nil {
			fatal(err)
		}
	case "rollback":
		if err := rollback(layout); err != nil {
			fatal(err)
		}
	case "stop":
		if err := stopRunning(layout); err != nil {
			fatal(err)
		}
	case "reset-admin":
		// 不能走 runCurrent：那条路径有「已在运行就不再启动」的护栏，会把找回口令
		// 静默变成「打开浏览器」；它 Release() 后立刻退出，新口令还没打印完窗口就没了。
		if err := forwardWait(layout, append([]string{"reset-admin"}, args...)); err != nil {
			fatal(err)
		}
	case "shell-check", "backup":
		// 同 reset-admin：一次性命令必须把输出留在屏幕上、退出码原样传回去。
		if err := forwardWait(layout, append([]string{command}, args...)); err != nil {
			fatal(err)
		}
	case "help":
		usage()
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Print(`kkapp —— KeyKeep 启动器

  kkapp              启动当前版本
  kkapp version      显示启动器版本
  kkapp list         列出已安装版本与当前指针
  kkapp update       检查并安装更新（--check 只看不装）
  kkapp rollback     切到上一版本
  kkapp stop         让正在运行的实例优雅退出（安装/升级前用）
  kkapp reset-admin  忘记管理员口令时重置（--enable 同时解除停用）
  kkapp shell-check  检查控制台呈现模式与导航白名单（--json / --strict）
  kkapp backup       立即备份数据库（--verify 只校验最近一份）
`)
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "错误：%v\n", err)
	os.Exit(1)
}

// forwardWait 把子命令同步交给当前版本的 kkdesk，继承本控制台并等它结束。
//
// 与 runCurrent 的区别就是「同步」与「继承输出」：一次性命令的结果必须留在
// 屏幕上让人读到，退出码也要原样传回去供脚本判断。
func forwardWait(layout *desktop.Layout, argv []string) error {
	versionDir, err := resolveVersionDir(layout)
	if err != nil {
		return err
	}
	deskExe, err := desktop.ResolveExe(versionDir, "kkdesk.exe")
	if err != nil {
		return err
	}
	cmd := &exec.Cmd{
		Path:   deskExe,
		Args:   append([]string{deskExe}, argv...),
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	}
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		// 子进程已经把自己的原因打在 stderr 上了，外壳再补一句「错误：exit status 1」
		// 只是噪音；这里只把退出码原样传出去。
		if errors.As(err, &exitErr) {
			os.Exit(exitErr.ExitCode())
		}
		return err
	}
	return nil
}

// runCurrent 解析版本目录并把它交给 kkdesk。
func runCurrent(layout *desktop.Layout, args []string) error {
	versionDir, err := resolveVersionDir(layout)
	if err != nil {
		return err
	}
	deskExe, err := desktop.ResolveExe(versionDir, "kkdesk.exe")
	if err != nil {
		return err
	}

	// 先尝试连一下控制台：已在运行就不要起第二个实例（会抢端口）。
	//
	// ADR-013：这时用户想要的是「再看一眼控制台」，大概率是窗口被关了缩在托盘里。
	// 先走命名事件让已有实例把窗口提起来；没人监听（旧版本实例、或浏览器模式）
	// 才退回原来的「用浏览器打开」——那仍然是能看到控制台的一条确定路径。
	if settings, exists, loadErr := layout.LoadSettings(); loadErr == nil && exists {
		if desktop.WaitHTTP(settings.ControlURL()+"/login", 1500*time.Millisecond) == nil {
			fmt.Printf("KeyKeep 已在运行：%s\n", settings.ControlURL())
			if desktop.RequestShowConsole() {
				return nil
			}
			_ = desktop.OpenBrowser(settings.ControlURL())
			return nil
		}
	}

	// HTTP 探测不到 ≠ 没有实例：首启初始化（建库、迁移）要 20–60 秒，期间
	// 控制台还没就绪。互斥体还在就说明有个 kkdesk 正在启动——不能再拉一个
	// 去抢端口/数据目录锁，给用户一句「正在启动」的反馈就够了。
	if desktop.MutexExists() {
		fmt.Println("KeyKeep 正在启动或已在运行，请查看托盘图标。")
		desktop.MessageBoxInfo("KeyKeep",
			"KeyKeep 正在启动，请稍候。\r\n\r\n首次启动（建库）约需 20–60 秒，"+
				"就绪后会自动打开控制台；托盘图标出现即表示外壳已就位，"+
				"双击托盘图标也可以打开控制台。")
		return nil
	}

	cmd := &exec.Cmd{Path: deskExe, Args: append([]string{deskExe}, args...)}
	// 后台拉起：不弹控制台黑窗。kkdesk 自己把日志写进 logs\kkdesk.log
	// （cmd/kkdesk openLogFile），不需要继承外壳的控制台输出；用户要从
	// 终端读输出的一次性子命令走 forwardWait，那里仍然继承控制台。
	cmd.SysProcAttr = hiddenProcAttr()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 kkdesk 失败: %w", err)
	}
	// 外壳不等待：kkdesk 是常驻进程，托管它会让外壳跟着占一个窗口。
	// Detach 在这里不是必须的（Windows 上子进程默认独立），但显式释放句柄更稳。
	_ = cmd.Process.Release()
	return nil
}

// resolveVersionDir 读 current.txt；没有指针时用 app\ 下最高版本（首装场景）。
func resolveVersionDir(layout *desktop.Layout) (string, error) {
	current, err := layout.CurrentVersion()
	if err != nil {
		return "", err
	}
	if current != "" {
		dir := layout.VersionDir(current)
		if _, statErr := os.Stat(filepath.Join(dir, "kkdesk.exe")); statErr == nil {
			return dir, nil
		}
		log.Printf("current.txt 指向 %s，但该版本不完整，回退到已安装的最高版本", current)
	}

	versions, err := layout.InstalledVersions()
	if err != nil {
		return "", err
	}
	if len(versions) == 0 {
		return "", errors.New("没有已安装的版本：安装包可能不完整，或 app 目录被删除")
	}
	sort.Slice(versions, func(i, j int) bool {
		return desktop.CompareVersion(versions[i], versions[j]) < 0
	})
	latest := versions[len(versions)-1]
	log.Printf("使用已安装版本 %s", latest)
	return layout.VersionDir(latest), nil
}

// buildPublicKey / buildUpdateBase 由构建脚本注入到 kkapp（与 kkdesk 同源）。
//
// 放进外壳而不是只放 kkdesk 的原因：升级要在「当前版本可能已经坏掉」的情况下
// 也能执行——外壳是唯一保证可用的可执行文件。
var (
	buildPublicKey  = ""
	buildUpdateBase = ""
)

// update 检查并安装更新。--check 只检查。
func update(layout *desktop.Layout, args []string) error {
	checkOnly := false
	for _, arg := range args {
		if arg == "--check" {
			checkOnly = true
		}
	}
	if buildPublicKey == "" || buildUpdateBase == "" {
		return fmt.Errorf("这个构建没有配置更新源（本地开发构建）")
	}
	settings, exists, err := layout.LoadSettings()
	if err != nil {
		return err
	}
	current := version.String()
	if exists && settings.Version != "" && settings.Version != "0" {
		// 以已安装版本为准：外壳自身的版本号可能落后于实际运行版本。
		current = settings.Version
	}

	fetcher := &desktop.Fetcher{
		BaseURL:   desktop.ResolveBaseURL(buildUpdateBase),
		PublicKey: buildPublicKey,
	}
	check, err := fetcher.Check(current)
	if err != nil {
		return err
	}
	if !check.Available {
		fmt.Printf("已是最新版本（%s）\n", current)
		return nil
	}
	fmt.Printf("发现新版本 %s（当前 %s）\n", check.Manifest.Version, current)
	for _, note := range check.Manifest.Notes {
		fmt.Println("  - " + note)
	}
	if checkOnly {
		return nil
	}

	packagePath, err := fetcher.Download(check.Manifest, filepath.Join(layout.DataDir(), "updates"))
	if err != nil {
		return err
	}
	fmt.Printf("已下载并校验 %s\n", filepath.Base(packagePath))

	installed, err := layout.Install(packagePath, check.Manifest.Version)
	if err != nil {
		return err
	}
	// update 不像 install 那样先 stop，服务通常还在跑；runtime 被锁住时
	// Install 会把新版运行时暂存到版本目录，由 kkdesk 下次启动换上。
	// 不说出来的话，运维会以为 Node/PostgreSQL 已经跟着版本升级了。
	if info, statErr := os.Stat(filepath.Join(installed, "runtime")); statErr == nil && info.IsDir() {
		fmt.Println("运行时（Node/PostgreSQL）正被占用，将在下次启动时自动更换；也可先 kkapp stop 再重启")
	}
	// kkapp 正是「当前版本可能已坏」时的兜底更新路径：新包必须自己能跑
	// `kkdesk version` 并报出目标版本号，才允许切指针；否则坏包会把系统
	// 切死、只能手工回退。带 15s 超时，挂死的新 exe 不能卡死更新流程。
	if err := verifyNewVersion(installed, check.Manifest.Version); err != nil {
		_ = os.RemoveAll(installed)
		return fmt.Errorf("新版本健康检查未通过，已丢弃: %w", err)
	}
	if err := layout.SetCurrentVersion(check.Manifest.Version); err != nil {
		return err
	}
	settings.Version = check.Manifest.Version
	if err := layout.SaveSettings(settings); err != nil {
		return err
	}
	fmt.Printf("已切换到 %s，重启后生效（从开始菜单重新打开 KeyKeep）\n", check.Manifest.Version)
	return nil
}

// stopRunning 让正在运行的本安装实例优雅退出。
//
// 安装器与升级流程都会先调它：kkdesk 收到停止请求后会按 control → gateway →
// mock → postgres 的顺序停子进程。直接 taskkill /F 会留下占端口的孤儿 postgres，
// 下一次启动就会失败在「数据库起不来」。
func stopRunning(layout *desktop.Layout) error {
	if !desktop.RequestStop() {
		fmt.Println("没有正在运行的 KeyKeep 实例")
		return nil
	}
	// 等 kkdesk 真正退出：它会按 control → gateway → mock → postgres 的顺序
	// 停子进程，最后一步是 PostgreSQL（pg_ctl fast，可能要几秒）。
	// 判据用「进程还在不在」而不是「端口通不通」——端口先释放，数据库却可能
	// 还在做检查点，这时返回会让安装器抢着替换 exe。
	settings, exists, err := layout.LoadSettings()
	if err != nil || !exists {
		fmt.Println("已发送停止请求")
		return nil
	}
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		if !desktop.DesktopRunning(settings.ControlURL()) {
			fmt.Println("已停止")
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("等待实例退出超时，请手动结束 kkdesk.exe 后重试")
}

func listVersions(layout *desktop.Layout) error {
	current, _ := layout.CurrentVersion()
	versions, err := layout.InstalledVersions()
	if err != nil {
		return err
	}
	if len(versions) == 0 {
		fmt.Println("没有已安装的版本")
		return nil
	}
	for _, item := range versions {
		marker := "  "
		if item == current {
			marker = "* "
		}
		fmt.Printf("%s%s\n", marker, item)
	}
	return nil
}

// rollback 切到当前版本之前的那个版本。
func rollback(layout *desktop.Layout) error {
	current, err := layout.CurrentVersion()
	if err != nil {
		return err
	}
	versions, err := layout.InstalledVersions()
	if err != nil {
		return err
	}
	sort.Slice(versions, func(i, j int) bool {
		return desktop.CompareVersion(versions[i], versions[j]) < 0
	})
	var previous string
	for _, item := range versions {
		if desktop.CompareVersion(item, current) < 0 {
			previous = item
		}
	}
	if previous == "" {
		return fmt.Errorf("没有可回滚的旧版本（当前 %q）", current)
	}
	if err := layout.SetCurrentVersion(previous); err != nil {
		return err
	}
	// settings.Version 是「已安装版本」的权威记录，update 用它算当前版本。
	// 只改指针不改它，下次检查更新就会拿一个已经不在运行的版本做比较。
	if settings, exists, err := layout.LoadSettings(); err == nil && exists {
		settings.Version = previous
		if saveErr := layout.SaveSettings(settings); saveErr != nil {
			log.Printf("同步 settings.json 版本号失败（不影响本次回滚）: %v", saveErr)
		}
	}
	fmt.Printf("已回滚到 %s（原 %s）\n", previous, current)
	return nil
}

// verifyNewVersion 检查新解包的版本能真正运行并报出目标版本号。
//
// 只 Stat kkdesk.exe 不够：解包可能留下 0 字节或损坏的 exe，一旦切指针，
// 下次启动就是黑屏。这里跑 `kkdesk version` 并核对输出含目标版本；
// 15s 超时保证挂死的新 exe 不会卡死更新流程。路径来自 ResolveExe 的
// 安装目录白名单校验。
func verifyNewVersion(versionDir, wantVersion string) error {
	exe, err := desktop.ResolveExe(versionDir, "kkdesk.exe")
	if err != nil {
		return fmt.Errorf("新版本缺少 kkdesk.exe: %w", err)
	}
	cmd := &exec.Cmd{Path: exe, Args: []string{exe, "version"}}
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stdout
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("新版本无法运行: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("新版本无法运行: %w", err)
		}
	case <-time.After(15 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		return errors.New("新版本健康检查超时（15s）")
	}
	if !strings.Contains(stdout.String(), wantVersion) {
		return fmt.Errorf("新版本版本号输出 %q 不含目标版本 %s", strings.TrimSpace(stdout.String()), wantVersion)
	}
	return nil
}
