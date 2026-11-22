// Command kkdesk 是 KeyKeep 桌面版的主程序：首装初始化、托管子进程、托盘与更新。
//
// 它由 <安装目录>\app\<版本>\kkdesk.exe 运行；stable 外壳 kkapp.exe 只负责
// 读 current.txt 并拉起对应版本，自身不参与更新（见 ADR-011）。
//
// 子命令：
//
//	kkdesk            启动（有托盘；--tray 由自启项传入，行为相同）
//	kkdesk serve      前台运行，不开托盘（排障用，Ctrl+C 退出）
//	kkdesk setup      只做首装初始化，不启动服务
//	kkdesk status     打印各服务与端口状态
//	kkdesk update     检查并安装更新（--check 只看不装）
//	kkdesk shell-check 检查控制台呈现模式与导航白名单（--json / --strict）
//	kkdesk reset-admin 找回管理员口令（--enable 同时解除停用）
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/keykeep/gateway/internal/desktop"
	"github.com/keykeep/gateway/internal/shell"
	"github.com/keykeep/gateway/internal/version"
)

// buildPublicKey 由构建脚本注入：-X main.buildPublicKey=<base64>
// 空值意味着这个包不是通过发布脚本构建的，更新功能会被禁用而不是降级为「不验签」。
var buildPublicKey = ""

// buildUpdateBase 由构建脚本注入，指向 Releases 的 latest/download 目录。
var buildUpdateBase = ""

func main() {
	log.SetFlags(log.Ltime)
	log.SetPrefix("kkdesk ")

	args := os.Args[1:]
	command := "run"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		command = args[0]
		args = args[1:]
	}

	layout, err := desktop.NewLayout()
	if err != nil {
		fatal(err)
	}
	// 桌面版没有控制台窗口，日志必须落盘：托盘退出、更新、监管重启这些都
	// 只在日志里留痕，缺了它排障时等于瞎。
	if logFile := openLogFile(layout); logFile != nil {
		defer logFile.Close()
		log.SetOutput(io.MultiWriter(os.Stderr, logFile))
	}

	switch command {
	case "run", "serve":
		// 单实例守卫：常驻路径才持互斥体。首启初始化要 20–60 秒，这期间
		// 控制台还没就绪，外壳只能靠互斥体判断「已有实例在启动」；不挡的话
		// 用户等急了连点几下，会叠出多个 kkdesk 抢端口和数据目录锁，表现
		// 就是「双击打不开、卡死」。
		release, alreadyRunning, holdErr := desktop.TryHoldSingleInstance()
		if holdErr != nil {
			// 拿不到互斥体不阻断启动（例如受限环境），退化为旧行为。
			log.Printf("单实例互斥体不可用（继续启动）: %v", holdErr)
		} else if alreadyRunning {
			log.Printf("已有 kkdesk 实例在运行，本次启动退出")
			return
		} else {
			defer release()
		}
		headless := command == "serve"
		fs := flag.NewFlagSet(command, flag.ExitOnError)
		// 托盘默认开：不带参数双击、以及自启项传 --tray，都应该有图标。
		// 只有 serve 子命令是无托盘的排障入口。
		showTray := fs.Bool("tray", true, "显示托盘图标（serve 子命令下无效果）")
		_ = fs.Parse(args)
		if err := runDesktop(layout, !headless && *showTray); err != nil {
			fatal(err)
		}
	case "setup":
		if _, err := setup(layout); err != nil {
			fatal(err)
		}
		fmt.Println("初始化完成")
	case "status":
		if err := printStatus(layout); err != nil {
			fatal(err)
		}
	case "reset-admin":
		fs := flag.NewFlagSet("reset-admin", flag.ExitOnError)
		enable := fs.Bool("enable", false, "同时解除账号的「停用」状态")
		_ = fs.Parse(args)
		if err := resetAdmin(layout, *enable); err != nil {
			fatal(err)
		}
	case "update":
		fs := flag.NewFlagSet("update", flag.ExitOnError)
		checkOnly := fs.Bool("check", false, "只检查，不安装")
		_ = fs.Parse(args)
		if err := runUpdate(layout, *checkOnly); err != nil {
			fatal(err)
		}
	case "backup":
		fs := flag.NewFlagSet("backup", flag.ExitOnError)
		verifyOnly := fs.Bool("verify", false, "只离线校验最近一份备份，不新建备份")
		_ = fs.Parse(args)
		if err := runBackup(layout, *verifyOnly); err != nil {
			fatal(err)
		}
	case "shell-check":
		// ADR-013 的现场自检：确认这个二进制在**这台机器上**的呈现模式与
		// 导航白名单判定，也是验收项 T-51/T-52 的证据来源。
		fs := flag.NewFlagSet("shell-check", flag.ExitOnError)
		asJSON := fs.Bool("json", false, "以 JSON 输出（证据归档用）")
		strict := fs.Bool("strict", false, "任一判定与预期不符时以非零退出")
		_ = fs.Parse(args)
		os.Exit(runShellCheck(layout, *asJSON, *strict))
	case "version", "--version", "-v":
		fmt.Printf("kkdesk %s\n", version.String())
	case "help", "--help", "-h":
		usage()
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Print(`kkdesk —— KeyKeep 桌面版主程序

  kkdesk              启动服务并显示托盘（首次运行会做初始化）
  kkdesk serve        前台运行，不开托盘（排障用）
  kkdesk setup        只做首装初始化
  kkdesk status       查看服务与端口状态
  kkdesk update       检查并安装更新（--check 只看不装）
  kkdesk backup       立即备份数据库（--verify 只校验最近一份）
  kkdesk reset-admin  忘记管理员口令时重置（--enable 同时解除停用）
  kkdesk shell-check  检查控制台呈现模式与导航白名单（--json / --strict）
  kkdesk version      显示版本
`)
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "错误：%v\n", err)
	fmt.Fprintln(os.Stderr, "日志目录：见安装目录下的 logs\\")
	os.Exit(1)
}

// openLogFile 打开 logs\kkdesk.log（按大小轮转）。失败时返回 nil，不阻塞启动：
// 日志不可写不该让整个服务起不来。
//
// 主程序日志是所有子进程生命周期的汇总（启动/退出/重启/更新），也是桌面版
// 唯一长期在写的那一份。它不轮转的话，跑几年的机器最终会被自己的日志写满。
func openLogFile(layout *desktop.Layout) io.WriteCloser {
	file, err := desktop.OpenLog(filepath.Join(layout.LogDir(), "kkdesk.log"))
	if err != nil {
		return nil
	}
	return file
}

// ---------------------------------------------------------------- 首装

// setup 保证 settings 与数据库就绪，返回 settings。
//
// 「已初始化」的判据是 settings.InitializedAt 非空，而不是 settings.json 是否存在：
// 后者在生成密钥阶段就会被写出来，用它判断会让「密钥写好了但种子没跑完」的中断
// 安装被当成已完成，结果是管理员账号根本不存在。
func setup(layout *desktop.Layout) (desktop.Settings, error) {
	// 上一次更新里因为目录被占用没换成的运行时，必须赶在任何子进程起来之前换上：
	// 这是整台机器上唯一一段 runtime\ 里没有正在运行的 exe 的时间窗（A6）。
	// 失败不打断启动——现场没动，下次启动还会再试一次。
	if replaced, err := layout.ApplyPendingRuntime(currentVersionDir(layout)); err != nil {
		log.Printf("应用待换运行时失败（下次启动继续尝试）: %v", err)
	} else if replaced {
		log.Print("已应用上次更新暂存的运行时")
	}
	settings, exists, err := layout.LoadSettings()
	if err != nil {
		return settings, err
	}
	if exists && layout.WelcomeFileExists() {
		log.Print(`提示：「初始登录信息.txt」仍在，内含明文初始口令；改完管理员口令请删除该文件`)
	}
	fresh := settings.InitializedAt == ""
	if err := settings.PrepareSecrets(); err != nil {
		return settings, fmt.Errorf("生成密钥失败: %w", err)
	}
	// 首装时探测 PostgreSQL 端口：Windows 常把高位端口段保留给 Hyper-V/WSL，
	// 写死的 55432 在部分机器上会直接 bind 失败。
	settings.EnsurePorts(fresh)
	// 版本号以「正在运行的这个可执行文件」为准。settings.json 的 version 只是
	// 上次写下的基准：安装器/更新只切 current.txt，不会改它，于是升级后界面一直
	// 显示首装那天的版本（实测 test.6 在跑、界面写 test.3）。开发构建不带真实
	// 版本号，不能拿它覆盖已安装版本。
	if running := version.String(); running != "" && running != "0.0.0-dev" {
		settings.Version = running
	} else if settings.Version == "" || settings.Version == "0" {
		settings.Version = running
	}
	if err := layout.SaveSettings(settings); err != nil {
		return settings, fmt.Errorf("写入配置失败: %w", err)
	}
	if !fresh {
		return settings, nil
	}

	log.Printf("首次运行，开始初始化（数据目录 %s）", layout.DataDir())
	adminPassword, err := initialize(layout, &settings)
	if err != nil {
		return settings, err
	}
	settings.InitializedAt = time.Now().Format(time.RFC3339)
	if err := layout.SaveSettings(settings); err != nil {
		return settings, err
	}
	writeWelcome(layout, settings, adminPassword)
	return settings, nil
}

// initialize 跑一遍建库、迁移、种子，返回首装随机管理员口令。
//
// 口令只在返回值里存在，绝不写进 settings.json：那份文件会被托盘反复重写，
// 也不是给人看的东西（C13）。唯一的载体是 writeWelcome 写的一次性文件。
func initialize(layout *desktop.Layout, settings *desktop.Settings) (string, error) {
	currentDir := currentVersionDir(layout)
	logPath := filepath.Join(layout.LogDir(), "setup.log")

	supervisor := desktop.NewSupervisor(layout, *settings)
	defer supervisor.Stop()
	if err := supervisor.StartDatabase(); err != nil {
		return "", err
	}
	if err := layout.Migrate(currentDir, *settings, logPath); err != nil {
		return "", fmt.Errorf("数据库迁移失败（详见 logs\\setup.log）: %w", err)
	}
	adminPassword, err := desktop.RandomPassword(16)
	if err != nil {
		return "", err
	}
	if err := layout.Seed(currentDir, *settings, adminPassword, logPath); err != nil {
		return "", fmt.Errorf("写入首装数据失败（详见 logs\\setup.log）: %w", err)
	}
	return adminPassword, nil
}

// currentVersionDir 返回当前运行的版本目录。
func currentVersionDir(layout *desktop.Layout) string {
	exe, err := os.Executable()
	if err != nil {
		return layout.Root
	}
	return filepath.Dir(exe)
}

// writeWelcome 把初始登录信息写到桌面文件，并打开一次。
//
// 口令只在首装打印一次（与容器版 seed.mjs 的策略一致）；桌面版没有控制台窗口，
// 所以落到一个明文文件里，并提示用户首次登录后修改口令再删掉该文件。
// 这个文件是明文口令的唯一载体，别在 settings.json 里再抄一份。
func writeWelcome(layout *desktop.Layout, settings desktop.Settings, adminPassword string) {
	path := layout.WelcomeFile()
	body := fmt.Sprintf(`KeyKeep 初始登录信息

控制台地址   %s
管理员账号   %s
管理员口令   %s

这是首次安装随机生成的一次性口令，请登录后立即修改，然后删除本文件。
数据目录     %s
日志目录     %s

演示上游已经内置并启用，无需真实厂商凭据即可试跑：
  1) 登录控制台 → 首次部署向导会引导你改密 → 接供应商（可跳过）→ 签发 Key
     （向导第 3 步的下拉里已预置「演示员工」账号，直接选它即可）
  2) 到「我的接口」页按三条命令安装并使用连接器
  3) 模型别名填 demo 即可走内置 Mock 上游（每次调用固定计 12 输入 / 5 输出 token）

忘记口令：在安装目录运行 KeyKeep.exe reset-admin，会随机生成一个新口令，
          数据与配置都不受影响。
重置试用：删除 data 目录会丢失全部数据，请先备份。
`, settings.ControlURL(), settings.AdminAccount, adminPassword,
		layout.DataDir(), layout.LogDir())
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		log.Printf("写初始登录信息失败: %v", err)
		return
	}
	_ = desktop.OpenBrowser(path)
}

// ---------------------------------------------------------------- 口令找回

// resetAdmin 忘记管理员口令时的兜底：随机一个新口令原地改掉，只在终端打印一次。
//
// 刻意不写文件。「初始登录信息.txt」是明文口令的唯一载体（C13），说明书又让用户
// 改完口令就删掉它；如果这里再落一个「新口令.txt」，等于把同一个问题原样搬回来，
// 下一次忘记时还是只能靠删 data\ 重装。代价是必须当场抄下来。
//
// 必须由命令行运行而不是双击：桌面版双击起来的进程没有控制台，打印了也看不见。
func resetAdmin(layout *desktop.Layout, enable bool) error {
	settings, exists, err := layout.LoadSettings()
	if err != nil {
		return err
	}
	if !exists || settings.InitializedAt == "" {
		return errors.New("这台机器还没完成首装，不需要找回口令；直接启动即可自动初始化")
	}
	if !desktop.DatabaseUp(settings) {
		// 服务没在跑才自己拉一个 PostgreSQL；已经在跑就直接借用现有实例。
		supervisor := desktop.NewSupervisor(layout, settings)
		defer supervisor.Stop()
		if err := supervisor.StartDatabase(); err != nil {
			return err
		}
	}
	adminPassword, err := desktop.RandomPassword(16)
	if err != nil {
		return fmt.Errorf("生成新口令失败: %w", err)
	}
	wasDisabled, err := layout.ResetAdminPassword(
		currentVersionDir(layout), settings, adminPassword, enable,
		filepath.Join(layout.LogDir(), "reset-admin.log"),
	)
	if err != nil {
		return err
	}
	fmt.Printf("控制台地址   %s\n", settings.ControlURL())
	fmt.Printf("管理员账号   %s\n", settings.AdminAccount)
	fmt.Printf("新登录口令   %s\n", adminPassword)
	fmt.Println("旧登录会话已全部失效；请立即登录修改成自己的口令，本条打印不会写入任何文件。")
	if wasDisabled && !enable {
		fmt.Println("提示：该账号仍处于「停用」状态，需要再加 --enable 才能登录。")
	}
	return nil
}

// runBackup 立即备份一次，或用 --verify 只校验最近一份备份。
//
// 服务没在跑时自己拉一个 PostgreSQL：备份是「现在就要一份」的动作，
// 不该要求用户先把托盘点起来。已经在跑就直接借用（数据库本来就允许多连接）。
//
// 「借用的那个库绝不能在收尾时被停掉」是这里的硬约束：备份是运维的日常动作，
// 备份完把正在服务的库停了，等于用户点一次备份就把网关打成 503。
// 所以停库这件事只在**本次自己拉起来**的库上发生，用一个局部 supervisor 承载，
// 并且只 defer 它自己的 Stop。
func runBackup(layout *desktop.Layout, verifyOnly bool) error {
	settings, exists, err := layout.LoadSettings()
	if err != nil {
		return err
	}
	if !exists || settings.InitializedAt == "" {
		return errors.New("这台机器还没完成首装，没有可备份的数据")
	}
	currentDir := currentVersionDir(layout)
	logPath := filepath.Join(layout.LogDir(), "backup.log")

	if verifyOnly {
		manifest, created, err := layout.LatestBackup()
		if err != nil {
			return err
		}
		if manifest == "" {
			return fmt.Errorf("还没有任何备份（备份目录 %s）", layout.BackupDir())
		}
		if err := layout.VerifyBackup(currentDir, manifest, logPath); err != nil {
			return err
		}
		fmt.Printf("备份校验通过   %s\n", filepath.Base(manifest))
		fmt.Printf("备份时刻       %s\n", created.Local().Format("2006-01-02 15:04:05"))
		return nil
	}

	if !desktop.DatabaseUp(settings) {
		supervisor := desktop.NewSupervisor(layout, settings)
		// 只停自己起的库；借用的库不进这个分支，函数返回时也就不会被停。
		defer supervisor.Stop()
		if err := supervisor.StartDatabase(); err != nil {
			return err
		}
	}
	manifest, err := layout.Backup(currentDir, settings, logPath)
	if err != nil {
		return err
	}
	fmt.Printf("备份完成       %s\n", manifest)
	fmt.Printf("备份目录       %s\n", layout.BackupDir())
	fmt.Println("提示：备份含加密后的上游凭据，恢复需要同一份 settings.json 里的 KEK。")
	return nil
}

// appState 是运行期共享状态。
//
// 为什么不再像以前那样把 settings 按值传给 handleMenu：局域网开关一改，
// 外层持有的那份副本就过期了——菜单点完「允许局域网访问」，之后点「打开控制台」
// 仍然打开旧地址。这里统一由一个可变对象承载，开关改完就同步回去。
type appState struct {
	layout     *desktop.Layout
	supervisor *desktop.Supervisor
	tray       *desktop.Tray
	currentDir string
	// presenter 是控制台的呈现层（ADR-013）：默认 WebView2 原生窗口，
	// 缺失运行时或开窗失败时回退浏览器。它自己不做平台判断，
	// 平台能力由 shell 包按构建标签提供。
	presenter *shell.Presenter
	// requestQuit 把所有退出入口（托盘菜单、停止事件、信号）汇到同一条路径。
	// 由 runDesktop 注入；未注入时退化为直接 Quit 托盘。
	requestQuit func(reason string)

	mu       sync.Mutex
	settings desktop.Settings
}

func newAppState(layout *desktop.Layout, settings desktop.Settings, supervisor *desktop.Supervisor, tray *desktop.Tray, currentDir string, presenter *shell.Presenter) *appState {
	return &appState{
		layout:     layout,
		supervisor: supervisor,
		tray:       tray,
		currentDir: currentDir,
		presenter:  presenter,
		settings:   settings,
		requestQuit: func(string) {
			tray.Quit()
		},
	}
}

func (a *appState) Settings() desktop.Settings {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.settings
}

func (a *appState) setSettings(settings desktop.Settings) {
	a.mu.Lock()
	a.settings = settings
	a.mu.Unlock()
}

// refreshTray 把当前的运行状态与开关状态刷到托盘。
func (a *appState) refreshTray(note string) {
	settings := a.Settings()
	tip := trayTip(a.supervisor, settings)
	if note != "" {
		tip = note
	}
	a.tray.SetState(tip, settings.AutoStart, true, "")
	a.tray.SetFlags(settings.LanEnabled, isFirewallOpen(settings), settings.AlwaysOn)
}

func isFirewallOpen(settings desktop.Settings) bool {
	if !settings.LanEnabled {
		return false
	}
	gw, ctl := desktop.FirewallState(settings.GatewayPort, settings.ControlPort)
	return gw && ctl
}

// trayTip 用子进程状态拼提示文本。
func trayTip(sup *desktop.Supervisor, settings desktop.Settings) string {
	if sup == nil {
		return "运行中 · " + settings.ControlURL()
	}
	statuses := sup.Status()
	var down []string
	restarts := 0
	for _, st := range statuses {
		restarts += st.Restarts
		if !st.Running {
			down = append(down, st.Name)
		}
	}
	switch {
	case len(down) > 0:
		return fmt.Sprintf("失败：%s 未运行（右键查看日志）", strings.Join(down, "、"))
	case restarts > 0:
		return fmt.Sprintf("自愈中 · 已重启 %d 次 · %s", restarts, settings.ControlURL())
	default:
		return "运行中 · " + settings.ControlURL()
	}
}

// ---------------------------------------------------------------- 运行

func runDesktop(layout *desktop.Layout, withTray bool) error {
	settings, err := setup(layout)
	if err != nil {
		return err
	}

	log.Printf("启动 %s（控制台 %s）", version.String(), settings.ControlURL())
	supervisor := desktop.NewSupervisor(layout, settings)
	currentDir := currentVersionDir(layout)

	// 托盘先就绪，用户才能在服务启动失败时看到「查看日志」而不是一个闪退的窗口。
	tray := desktop.NewTray("KeyKeep")
	presenter := buildPresenter(layout, settings)
	defer func() { _ = presenter.Close() }()
	app := newAppState(layout, settings, supervisor, tray, currentDir, presenter)
	supervisor.SetStateHook(func() { app.refreshTray("") })
	app.refreshTray("")

	if withTray {
		tray.OnSelect(func(id desktop.MenuID) {
			handleMenu(id, app)
		})
	}

	// 服务启动放到后台：初始化数据库可能耗时数十秒，托盘要先能点。
	startErr := make(chan error, 1)
	go func() {
		startErr <- startServices(app)
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	// quit 是所有退出路径（信号 / 命名事件 / 托盘菜单）的汇合点。
	// 主流程只等它，不再依赖 tray.Run() 是否返回——那正是 F10 的坑：
	// 消息投不出去时 Run 永不返回，子进程也永远不会被 Stop。
	quit := make(chan struct{})
	var quitOnce sync.Once
	requestQuit := func(reason string) {
		quitOnce.Do(func() {
			log.Print(reason)
			close(quit)
		})
		tray.Quit()
	}
	app.requestQuit = requestQuit
	go func() {
		<-stop
		requestQuit("收到退出信号")
	}()

	// 安装器 / kkapp stop 通过命名事件请求优雅退出：先停子进程（尤其是
	// PostgreSQL），再退出本进程。硬杀会让 postgres 变成占端口的孤儿进程。
	stopSignal, sigErr := desktop.PrepareStopSignal()
	if sigErr != nil {
		log.Printf("准备停止信号失败（不影响运行）: %v", sigErr)
	}
	if stopSignal != nil {
		defer stopSignal.Close()
		go func() {
			stopSignal.Wait()
			requestQuit("收到停止请求")
		}()
	}

	// 二次双击桌面图标（外壳 kkapp 走命名事件）时把窗口提到前台。
	// 与「停止请求」分开用两个事件：一个是「显示」，一个是「退出」，
	// 合成一个事件会让「想看一眼」变成「把服务关了」。
	showSignal, showErr := desktop.PrepareShowConsoleSignal()
	if showErr != nil {
		log.Printf("准备显示事件失败（不影响运行）: %v", showErr)
	}
	if showSignal != nil {
		defer showSignal.Close()
		go waitShowRequests(showSignal, func() error {
			_, err := app.presenter.Focus()
			return err
		})
	}

	if withTray {
		go watchStartup(startErr, app)
		// 托盘消息循环放到独立 goroutine：即使退出消息投递失败，主流程
		// 也不会卡在 GetMessage 上（F10）。Windows 的消息队列与线程绑定，
		// Run 内部自己 LockOSThread。
		go func() {
			if err := tray.Run(); err != nil {
				log.Printf("托盘启动失败: %v", err)
			}
		}()
		<-quit
	} else {
		if err := <-startErr; err != nil {
			supervisor.Stop()
			return err
		}
		<-quit
	}

	supervisor.Stop()
	log.Print("已停止")
	return nil
}

// startServices 依次启动数据库与三个 HTTP 服务，并在控制台可访问后打开浏览器。
func startServices(app *appState) error {
	settings := app.Settings()
	if err := app.supervisor.StartDatabase(); err != nil {
		app.tray.SetState("启动失败：数据库", settings.AutoStart, false, err.Error())
		return err
	}
	// 迁移必须在任何 HTTP 服务起来之前跑完，而且每次启动都要跑，不能只在首装跑：
	// 升级包带来的新迁移若等到用户手工执行，升级后控制面会对着旧表结构报错
	// （实测 test.3→test.4 升级：审计页整页失败，且窗口期内审计写入静默丢失）。
	// 迁移器自身按 _prisma_migrations 跳过已应用项，重复执行是空操作。
	if err := app.layout.Migrate(app.currentDir, settings, filepath.Join(app.layout.LogDir(), "setup.log")); err != nil {
		app.tray.SetState("启动失败：数据库迁移", settings.AutoStart, false, err.Error())
		return err
	}
	if err := app.supervisor.StartServices(app.currentDir); err != nil {
		app.tray.SetState("启动失败：服务", settings.AutoStart, false, err.Error())
		return err
	}
	// 控制台冷启动要等 Next 编译期完成（standalone 首请求仍需初始化）。
	if err := desktop.WaitHTTP(settings.ControlURL()+"/login", 90*time.Second); err != nil {
		app.tray.SetState("启动失败：控制台未就绪", settings.AutoStart, false, err.Error())
		return err
	}
	app.refreshTray("")
	log.Printf("就绪：%s", settings.ControlURL())
	// 健康自检从就绪之后开始计时，避免把冷启动当成卡死。
	app.supervisor.StartHealthLoop(30 * time.Second)
	// 定时备份：桌面版用户不会自己去配计划任务，而丢账是这套系统里唯一
	// 不可逆的故障。启动时若最近一份备份已过期会立刻补一次，之后每天凌晨跑。
	app.layout.StartBackupLoop(app.currentDir, settings, filepath.Join(app.layout.LogDir(), "backup.log"), func(err error) {
		log.Printf("自动备份失败: %v", err)
		app.refreshTray("自动备份失败（数据仍有风险）：" + err.Error())
	})
	// 控制台真就绪之后才呈现：服务没起来时开窗只会给用户一个白屏，
	// 而「正在启动」的反馈已经由外壳的提示框承担（ADR-013 §2.3）。
	outcome, presentErr := app.presenter.Show()
	if presentErr != nil {
		log.Printf("呈现控制台失败（%s）: %v", outcome, presentErr)
		app.refreshTray("控制台窗口未打开：" + presentErr.Error())
		return nil
	}
	log.Printf("控制台呈现：%s（模式 %s）", outcome, app.presenter.Mode)
	return nil
}

// buildPresenter 组装控制台的呈现层（ADR-013）。
//
// 平台能力全部来自 shell 包按构建标签提供的工厂：这里只做「把当前进程的
// 现实」告诉它（地址、profile 目录、图标、模式、调试开关），
// 不出现任何 runtime.GOOS 分支——分支散落在业务代码里，就是下一次改错的地方。
func buildPresenter(layout *desktop.Layout, settings desktop.Settings) *shell.Presenter {
	controlURL := shell.NormalizeControlURL(settings.ControlURL(), settings.ControlPort)
	policy, err := shell.NewPolicy(controlURL)
	if err != nil {
		// 控制台地址来自 settings.json（可被本机改写）。解析不了时用它兜底：
		// 白名单退化成默认地址，窗口仍然可用，绝不因为一个坏配置让桌面版起不来。
		log.Printf("控制台地址不合法（%v），按默认地址处理", err)
		policy, _ = shell.NewPolicy(shell.NormalizeControlURL("", desktop.DefaultControlPort))
		controlURL = policy.Origin()
	}

	mode := shell.ResolveMode(settings.UIShell, os.Getenv(shell.EnvShellMode))
	if mode == shell.ModeBrowser {
		log.Print("控制台模式：浏览器（settings.json uiShell 或 KEYKEEP_SHELL_MODE）")
	}

	script := shell.ShellScript{
		URL:        controlURL,
		Origin:     policy.Origin(),
		ProfileDir: shell.ProfileDir(os.Getenv("LOCALAPPDATA")),
		Title:      shell.ShellTitle,
		Width:      shell.DefaultShellWidth,
		Height:     shell.DefaultShellHeight,
		IconPath:   iconPath(layout),
		DevTools:   shell.DevToolsEnabled(os.Getenv(shell.EnvDevTools)),
	}

	presenter := shell.NewPresenter(policy, script, mode)
	presenter.Detect = shell.PlatformDetector()
	presenter.Notify = shell.PlatformNotifier()
	presenter.OpenBrowser = shell.PlatformOpenBrowser
	presenter.NewWindow = func() shell.Window { return shell.PlatformWindow(shell.PlatformOpenBrowser) }
	return presenter
}

// iconPath 找安装目录里的 keykeep.ico；找不到时返回空串（窗口用系统默认图标）。
func iconPath(layout *desktop.Layout) string {
	candidates := []string{
		filepath.Join(layout.Root, "keykeep.ico"),
		filepath.Join(currentVersionDir(layout), "keykeep.ico"),
	}
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return ""
}

// watchStartup 把异步启动结果反映到托盘提示上。
//
// 失败时除了写日志，还必须让用户看见：桌面版是外壳在后台拉起 kkdesk 的，
// 服务起不来时桌面上不会有任何窗口——用户看到的就是「双击没反应」，然后再
// 双击几次（被单实例互斥体挡掉，连日志都不多一行）。实测 test.8 那两个
// 「服务启动失败: exit status 1」就是这么呈现的：日志里有，屏幕上没有。
//
// 托盘提示只解决「用户已经去看托盘」的情况；对话框负责剩下的情况。两者都
// 不做，故障就只存在于日志里。运行在独立 goroutine 里，阻塞等用户点「确定」
// 不影响托盘消息循环与停止信号。
func watchStartup(startErr <-chan error, app *appState) {
	if err := <-startErr; err != nil {
		log.Printf("服务启动失败: %v", err)
		app.refreshTray("启动失败：" + err.Error())
		desktop.MessageBoxInfo("KeyKeep 启动失败",
			"KeyKeep 没能启动，服务未就绪。\n\n"+
				"原因："+err.Error()+"\n\n"+
				"详细日志在安装目录的 logs\\ 下（kkdesk.log / postgres.log / setup.log）。\n"+
				"托盘图标仍在，右键可以退出后重试。")
		return
	}
	log.Print("服务已就绪")
}

// handleMenu 处理托盘菜单点击。
func handleMenu(id desktop.MenuID, app *appState) {
	layout := app.layout
	settings := app.Settings()

	switch id {
	case desktop.MenuOpen:
		// ADR-013：默认弹原生窗口；窗口模式被关掉或不可用时 Presenter
		// 自己落到浏览器，这里不需要再判断一次。
		if _, err := app.presenter.Focus(); err != nil {
			log.Printf("打开控制台失败: %v", err)
		}
	case desktop.MenuOpenInBrowser:
		if err := app.presenter.OpenInBrowser(); err != nil {
			log.Printf("在浏览器中打开失败: %v", err)
		}
	case desktop.MenuStatus:
		// 设置页的 canonical 地址是 /me/settings（P6：员工页迁出 /admin 前缀）。
		// 旧地址 /admin/settings 仍保留 307，但这里是可控的调用点，不该多跳一次。
		_ = desktop.OpenBrowser(settings.ControlURL() + "/me/settings")
	case desktop.MenuCheckUpdate:
		go func() {
			if err := runUpdate(layout, false); err != nil {
				log.Printf("更新失败: %v", err)
			}
		}()
	case desktop.MenuRestart:
		// 真正重启网关与控制台（不动 PG）：以前这里只是退出并提示用户
		// 手工从开始菜单重开，等于没重启。
		go func() {
			log.Print("重启服务…")
			if err := app.supervisor.RestartServices(app.currentDir); err != nil {
				log.Printf("重启服务失败: %v", err)
				app.refreshTray("重启失败：" + err.Error())
				return
			}
			app.refreshTray("")
			// 控制台进程换了，窗口里的旧页面已经跟服务断开：不重载的话
			// 用户看到的是一个点不动的界面，会以为重启失败。
			if err := app.presenter.Reload(); err != nil {
				log.Printf("重启后重载窗口失败（可在托盘里重新打开）: %v", err)
			}
		}()
	case desktop.MenuToggleAutoStart:
		next := !settings.AutoStart
		// 自启项指向稳定外壳，不指向正在运行的 kkdesk：kkdesk 在版本目录里，
		// 升级清理旧版本后自启会失效。
		shell := layout.ShellPath()
		if _, err := os.Stat(shell); err != nil {
			log.Printf("找不到启动器 %s，无法设置开机自启: %v", shell, err)
			return
		}
		if err := desktop.SetAutoStart(next, shell); err != nil {
			log.Printf("切换开机自启失败: %v", err)
			return
		}
		settings.AutoStart = next
		_ = layout.SaveSettings(settings)
		app.setSettings(settings)
		app.refreshTray("")
	case desktop.MenuToggleLan:
		go toggleLan(app)
	case desktop.MenuAlwaysOn:
		go toggleAlwaysOn(app)
	case desktop.MenuOpenLogs:
		_ = desktop.OpenFolder(layout.LogDir())
	case desktop.MenuOpenDataDir:
		_ = desktop.OpenFolder(layout.DataDir())
	case desktop.MenuQuit:
		// 托盘菜单的「退出」也要走汇合通道，否则主流程只看到 tray.Run 返回，
		// 而 Run 返回并不保证子进程已停（F10）。
		app.requestQuit("托盘菜单退出")
	}
}

// toggleLan 开关局域网访问。
//
// 顺序是有讲究的：先写配置、再放行防火墙、最后重启服务。任何一步失败都要
// 把开关回滚，否则会出现「配置说开着、实际连不上」这种最难查的状态。
func toggleLan(app *appState) {
	settings := app.Settings()
	next := !settings.LanEnabled

	if next {
		app.refreshTray("正在申请管理员权限…")
		gw, ctl := desktop.FirewallState(settings.GatewayPort, settings.ControlPort)
		if !gw || !ctl {
			if err := desktop.AddFirewallRules(settings.GatewayPort, settings.ControlPort); err != nil {
				log.Printf("添加防火墙规则失败: %v", err)
				app.refreshTray("局域网未开启：" + err.Error())
				return
			}
		}
	} else {
		if err := desktop.RemoveFirewallRules(); err != nil {
			log.Printf("删除防火墙规则失败（继续关闭局域网）: %v", err)
		}
	}

	settings.LanEnabled = next
	if next {
		// 记下当前广播地址，免得每次开机都重新探测（换网时会由用户重新开关一次）。
		if settings.AdvertisedGatewayURL == "" {
			settings.AdvertisedGatewayURL = settings.PublicGatewayURL()
		}
	}
	if err := app.layout.SaveSettings(settings); err != nil {
		log.Printf("保存局域网开关失败: %v", err)
		app.refreshTray("局域网未切换：" + err.Error())
		return
	}
	app.setSettings(settings)
	app.supervisor.UpdateSettings(settings)

	if err := app.supervisor.RestartServices(app.currentDir); err != nil {
		// 回滚：配置写得进去但服务起不来时，别把用户留在半开状态。
		settings.LanEnabled = !next
		_ = app.layout.SaveSettings(settings)
		app.setSettings(settings)
		app.supervisor.UpdateSettings(settings)
		_ = app.supervisor.RestartServices(app.currentDir)
		log.Printf("切换局域网失败，已回滚: %v", err)
		app.refreshTray("局域网切换失败：" + err.Error())
		return
	}

	if next {
		log.Printf("局域网模式已开启：员工填 %s", settings.PublicGatewayURL())
		app.refreshTray("局域网已开启 · " + settings.PublicGatewayURL())
	} else {
		log.Print("局域网模式已关闭，仅本机可访问")
		app.refreshTray("")
	}
}

// toggleAlwaysOn 开关常开模式（不待机、不休眠）。
func toggleAlwaysOn(app *appState) {
	settings := app.Settings()
	next := !settings.AlwaysOn
	app.refreshTray("正在设置常开模式…")
	if err := desktop.SetAlwaysOn(next); err != nil {
		log.Printf("设置常开模式失败: %v", err)
		app.refreshTray("常开模式未切换：" + err.Error())
		return
	}
	settings.AlwaysOn = next
	if err := app.layout.SaveSettings(settings); err != nil {
		log.Printf("保存常开模式失败: %v", err)
	}
	app.setSettings(settings)
	if next {
		log.Print("常开模式已开启（仍需要用户登录才能启动服务）")
	}
	app.refreshTray("")
}

// ---------------------------------------------------------------- 更新

// runUpdate 检查并（可选）安装更新。
func runUpdate(layout *desktop.Layout, checkOnly bool) error {
	if buildPublicKey == "" || buildUpdateBase == "" {
		return errors.New("这个版本没有配置更新源（本地开发构建），跳过")
	}
	fetcher := &desktop.Fetcher{
		BaseURL:   desktop.ResolveBaseURL(buildUpdateBase),
		PublicKey: buildPublicKey,
	}
	current := version.String()
	check, err := fetcher.Check(current)
	if err != nil {
		return err
	}
	if !check.Available {
		fmt.Printf("已是最新版本（%s）\n", current)
		return nil
	}
	fmt.Printf("发现新版本 %s（当前 %s）\n", check.Manifest.Version, current)
	if len(check.Manifest.Notes) > 0 {
		fmt.Println("更新内容：")
		for _, note := range check.Manifest.Notes {
			fmt.Println("  - " + note)
		}
	}
	if checkOnly {
		return nil
	}

	packagePath, err := fetcher.Download(check.Manifest, filepath.Join(layout.DataDir(), "updates"))
	if err != nil {
		return err
	}
	fmt.Printf("更新包已下载并校验：%s\n", filepath.Base(packagePath))
	installed, err := layout.Install(packagePath, check.Manifest.Version)
	if err != nil {
		return err
	}
	fmt.Printf("已解包到 %s\n", installed)
	// 运行时被占用没换成时，包里的 runtime 会留在版本目录里等下次启动（A6）。
	// 这句话必须让用户看见，否则「重启后生效」会被理解成已经换好了。
	if info, statErr := os.Stat(filepath.Join(installed, "runtime")); statErr == nil && info.IsDir() {
		fmt.Println("Node/PostgreSQL 运行时将在下次启动时更换（本次更新期间服务不用手工退出）")
	}

	// 健康检查：新版本能正常启动才切指针，否则保留当前版本。
	if err := verifyNewVersion(layout, installed); err != nil {
		_ = os.RemoveAll(installed)
		return fmt.Errorf("新版本健康检查未通过，已回滚到 %s: %w", current, err)
	}
	if err := layout.SetCurrentVersion(check.Manifest.Version); err != nil {
		return err
	}
	// 与 kkapp update 保持一致：settings.Version 是下次比较的基准，必须一起推进。
	if settings, exists, loadErr := layout.LoadSettings(); loadErr == nil && exists {
		settings.Version = check.Manifest.Version
		_ = layout.SaveSettings(settings)
	}
	fmt.Printf("已切换到 %s，重启后生效\n", check.Manifest.Version)
	return nil
}

// verifyNewVersion 做最小可验证检查：新版本能被执行并打印出目标版本号。
//
// 只 Stat 文件不够——下载和解包都可能留下一个 0 字节或损坏的 exe，
// 那种包一旦切了指针，用户重启就会看到「什么都没发生」。这里直接跑
// `kkdesk version`：它会加载全部依赖并退出，失败即判定为坏包。
// 输出必须包含目标版本号：能打印 "kkdesk" 字样的损坏包不能算通过；
// 带 15s 超时，挂死的新 exe 不能把更新协程永久卡住。
func verifyNewVersion(layout *desktop.Layout, versionDir string) error {
	exe, err := desktop.ResolveExe(versionDir, "kkdesk.exe")
	if err != nil {
		return fmt.Errorf("新版本缺少 kkdesk.exe: %w", err)
	}
	want := filepath.Base(versionDir)
	output, err := runWithTimeout(exe, []string{"version"}, 15*time.Second)
	if err != nil {
		return fmt.Errorf("新版本无法运行: %w", err)
	}
	if !strings.Contains(string(output), want) {
		return fmt.Errorf("新版本版本号输出 %q 不含目标版本 %s", strings.TrimSpace(string(output)), want)
	}
	if err := layout.PruneVersions(want); err != nil {
		log.Printf("清理旧版本失败（不影响升级）: %v", err)
	}
	return nil
}

// runWithTimeout 运行一个已知路径的系统外程序并限时回收。
// 路径来自 desktop.ResolveExe 的白名单校验；这里只做进程级隔离与超时兜底。
func runWithTimeout(exe string, args []string, timeout time.Duration) ([]byte, error) {
	cmd := &exec.Cmd{Path: exe, Args: append([]string{exe}, args...)}
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stdout
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			return stdout.Bytes(), err
		}
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
		<-done
		return stdout.Bytes(), errors.New("执行超时")
	}
	return stdout.Bytes(), nil
}

// printStatus 打印本地状态，便于排障与脚本化检查。
func printStatus(layout *desktop.Layout) error {
	settings, exists, err := layout.LoadSettings()
	if err != nil {
		return err
	}
	if !exists {
		fmt.Println("尚未初始化（首次运行 kkdesk 会自动初始化）")
		return nil
	}
	current, _ := layout.CurrentVersion()
	versions, _ := layout.InstalledVersions()

	fmt.Printf("版本        %s\n", version.String())
	fmt.Printf("当前指针    %s\n", orDash(current))
	fmt.Printf("已装版本    %s\n", strings.Join(versions, ", "))
	fmt.Printf("控制台      %s\n", settings.ControlURL())
	fmt.Printf("数据面网关  %s\n", settings.GatewayURL())
	fmt.Printf("演示上游    %s\n", settings.MockURL())
	fmt.Printf("数据目录    %s\n", layout.DataDir())
	fmt.Printf("日志目录    %s\n", layout.LogDir())
	fmt.Printf("开机自启    %v\n", settings.AutoStart)
	fmt.Printf("常开模式    %v\n", settings.AlwaysOn)
	fmt.Printf("局域网模式  %v\n", settings.LanEnabled)
	if settings.LanEnabled {
		fmt.Printf("员工填地址  %s\n", settings.PublicGatewayURL())
		fmt.Printf("控制台对外  %s\n", settings.PublicControlURL())
		gwOpen, ctlOpen := desktop.FirewallState(settings.GatewayPort, settings.ControlPort)
		fmt.Printf("防火墙放行  网关 %v / 控制台 %v\n", gwOpen, ctlOpen)
		if !gwOpen || !ctlOpen {
			fmt.Println("            注意：规则缺失时员工连不上，托盘里可重新放行")
		}
	}

	for name, url := range map[string]string{
		"控制台":  settings.ControlURL() + "/login",
		"网关":   settings.GatewayURL() + "/healthz",
		"演示上游": settings.MockURL() + "/healthz",
	} {
		if err := desktop.WaitHTTP(url, 2*time.Second); err != nil {
			fmt.Printf("%-10s ✗ %v\n", name, err)
		} else {
			fmt.Printf("%-10s ✓ %s\n", name, url)
		}
	}
	return nil
}

func orDash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

// 供 supervisor 之外的调用方等待服务停止（serve 子命令用）。
var _ = context.Background
var _ = sync.Once{}
