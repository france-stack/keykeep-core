package desktop

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// Proc 是一个受监管的子进程。
type Proc struct {
	Name string
	Cmd  *exec.Cmd
	log  io.WriteCloser

	spec      launchSpec // 重启时复用它重建命令
	done      chan struct{}
	startedAt time.Time
	restarts  int
	lastExit  string
	running   bool
}

// ProcStatus 是对外报告的子进程状态（托盘与 kkdesk status 用）。
type ProcStatus struct {
	Name     string
	Running  bool
	Restarts int
	LastExit string
}

// launchSpec 描述「如何启动一个子进程」，供首次启动与重启复用。
type launchSpec struct {
	name    string
	logPath string
	// build 每次调用都构造一条全新的 *exec.Cmd：exec.Cmd 不能重复 Start。
	build func() (*exec.Cmd, error)
	// restart 为 true 时，非主动停止的退出会被自动拉起。
	restart bool
}

// 子进程名。postgres 单列一个常量：它的停止路径与其它三个不同（走 pg_ctl
// 而不是 Kill），两处判断必须用同一个字面量。
const (
	postgresProcName = "postgres"
)

// shutdownOrder 是 HTTP 服务的停止顺序：控制台先停（它是最后一个就绪、
// 最先收尾的对外面），再网关，最后演示上游。数据库不在其中，由 StopPostgres
// 单独收尾。
var shutdownOrder = map[string]int{"control": 0, "gateway": 1, "kkmock": 2}

// sortForShutdown 按 shutdownOrder 原地排序（插入排序，元素不超过三个）。
func sortForShutdown(procs []*Proc) {
	for i := 1; i < len(procs); i++ {
		for j := i; j > 0; j-- {
			if shutdownOrder[procs[j].Name] >= shutdownOrder[procs[j-1].Name] {
				break
			}
			procs[j], procs[j-1] = procs[j-1], procs[j]
		}
	}
}

// Supervisor 负责启动/停止/看护桌面版的各个子进程。
type Supervisor struct {
	layout   *Layout
	settings Settings
	mu       sync.Mutex
	procs    map[string]*Proc
	stopping bool
	stopCh   chan struct{}

	// health 记录健康探针连续失败次数（按进程名）。
	healthFails map[string]int
	// onState 在进程状态变化时回调，用于刷新托盘提示。
	onState func()
}

// NewSupervisor 创建一个监管器。
func NewSupervisor(layout *Layout, settings Settings) *Supervisor {
	return &Supervisor{
		layout:      layout,
		settings:    settings,
		procs:       map[string]*Proc{},
		healthFails: map[string]int{},
	}
}

// SetStateHook 注册状态变化回调（用于刷新托盘）。
func (s *Supervisor) SetStateHook(fn func()) {
	s.mu.Lock()
	s.onState = fn
	s.mu.Unlock()
}

// Settings 返回当前监管器持有的配置副本。
func (s *Supervisor) Settings() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settings
}

// UpdateSettings 替换配置副本（局域网开关切换后用）。
func (s *Supervisor) UpdateSettings(settings Settings) {
	s.mu.Lock()
	s.settings = settings
	s.mu.Unlock()
}

func (s *Supervisor) notify() {
	s.mu.Lock()
	fn := s.onState
	s.mu.Unlock()
	if fn != nil {
		fn()
	}
}

// StartDatabase 初始化并启动内置 PostgreSQL（幂等），随后确保业务库存在。
func (s *Supervisor) StartDatabase() error {
	if err := s.layout.EnsureDirs(); err != nil {
		return err
	}
	pgLog := filepath.Join(s.layout.LogDir(), "postgres.log")
	if err := s.layout.InitPGData(s.settings, pgLog); err != nil {
		return err
	}
	pg, err := s.layout.StartPostgres(s.settings, pgLog)
	if err != nil {
		return fmt.Errorf("启动 PostgreSQL 失败: %w", err)
	}
	// PostgreSQL 不自动重启：数据目录在异常退出后可能需要恢复，
	// 盲目拉起会让问题从「服务停了」变成「数据目录状态不明」。
	// 它退出时只记录，由托盘提示人工介入。
	s.adopt("postgres", pg, launchSpec{name: "postgres", restart: false})
	if err := s.layout.EnsureDatabase(s.settings); err != nil {
		return fmt.Errorf("创建数据库失败: %w", err)
	}
	return nil
}

// StartServices 启动演示上游、网关、控制台。调用前迁移必须已完成。
func (s *Supervisor) StartServices(currentDir string) error {
	if err := s.startMock(currentDir); err != nil {
		return err
	}
	if err := s.startGateway(currentDir); err != nil {
		return err
	}
	return s.startControl(currentDir)
}

// RestartServices 只重启网关与控制台（局域网开关切换用），不动 PG 与演示上游。
func (s *Supervisor) RestartServices(currentDir string) error {
	s.mu.Lock()
	var targets []*Proc
	for _, name := range []string{"gateway", "control"} {
		if proc, ok := s.procs[name]; ok {
			targets = append(targets, proc)
		}
	}
	s.mu.Unlock()

	for _, proc := range targets {
		s.terminate(proc)
	}
	// 等端口真正释放，否则立刻重建会撞「address already in use」。
	time.Sleep(300 * time.Millisecond)

	if err := s.startGateway(currentDir); err != nil {
		return err
	}
	return s.startControl(currentDir)
}

func (s *Supervisor) startMock(currentDir string) error {
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(s.settings.MockPort))
	spec := launchSpec{
		name:    "kkmock",
		logPath: filepath.Join(s.layout.LogDir(), "kkmock.log"),
		restart: true,
		build: func() (*exec.Cmd, error) {
			exe, err := safeExe(currentDir, "kkmock.exe")
			if err != nil {
				return nil, err
			}
			cmd := command(exe, "-addr", addr, "-quiet")
			cmd.Env = baseEnv()
			return cmd, nil
		},
	}
	if err := s.spawn(spec); err != nil {
		return fmt.Errorf("启动演示上游失败: %w", err)
	}
	return nil
}

func (s *Supervisor) startGateway(currentDir string) error {
	spec := launchSpec{
		name:    "gateway",
		logPath: filepath.Join(s.layout.LogDir(), "gateway.log"),
		restart: true,
		build: func() (*exec.Cmd, error) {
			exe, err := safeExe(currentDir, "gateway.exe")
			if err != nil {
				return nil, err
			}
			cmd := command(exe)
			cmd.Env = s.gatewayEnv(filepath.Join(currentDir, "config.yaml"))
			return cmd, nil
		},
	}
	if err := s.spawn(spec); err != nil {
		return fmt.Errorf("启动网关失败: %w", err)
	}
	return nil
}

func (s *Supervisor) startControl(currentDir string) error {
	spec := launchSpec{
		name:    "control",
		logPath: filepath.Join(s.layout.LogDir(), "control.log"),
		restart: true,
		build: func() (*exec.Cmd, error) {
			nodeExe, err := safeExe(s.layout.NodeDir(), "node.exe")
			if err != nil {
				return nil, fmt.Errorf("找不到内置 Node 运行时: %w", err)
			}
			serverJS := filepath.Join(currentDir, "control", "server.js")
			if _, err := os.Stat(serverJS); err != nil {
				return nil, fmt.Errorf("找不到控制台入口（%s）", serverJS)
			}
			// 安装包解开后 pnpm 的顶层链接会丢失（zip 存不了 Windows 目录联接），
			// 必须在这里重建，否则 node 找不到 next / @swc/helpers。
			if fixed, linkErr := EnsureNodeModulesLinks(filepath.Join(currentDir, "control")); linkErr != nil {
				log.Printf("重建依赖链接失败（继续尝试启动）: %v", linkErr)
			} else if fixed > 0 {
				log.Printf("重建依赖链接 %d 个", fixed)
			}
			cmd := command(nodeExe, serverJS)
			cmd.Dir = filepath.Dir(serverJS)
			cmd.Env = s.controlEnv()
			return cmd, nil
		},
	}
	if err := s.spawn(spec); err != nil {
		return fmt.Errorf("启动控制台失败: %w", err)
	}
	return nil
}

// spawn 启动一个子进程并交给看护协程。
func (s *Supervisor) spawn(spec launchSpec) error {
	cmd, err := spec.build()
	if err != nil {
		return err
	}
	file, err := openLog(spec.logPath)
	if err != nil {
		return err
	}
	cmd.Stdout = file
	cmd.Stderr = file
	if err := cmd.Start(); err != nil {
		file.Close()
		return err
	}
	proc := &Proc{
		Name:      spec.name,
		Cmd:       cmd,
		log:       file,
		spec:      spec,
		done:      make(chan struct{}),
		startedAt: time.Now(),
		running:   true,
	}
	s.mu.Lock()
	// 重启时沿用同一个 Proc 的 restart 计数，所以这里只在首次创建时置零。
	if old, ok := s.procs[spec.name]; ok {
		proc.restarts = old.restarts
	}
	s.procs[spec.name] = proc
	s.mu.Unlock()

	s.notify()
	go s.watch(proc)
	return nil
}

// redialProc 是「重启但保留计数」的 spawn。
func (s *Supervisor) redialProc(proc *Proc) {
	s.mu.Lock()
	prevRestarts := proc.restarts
	s.mu.Unlock()

	if err := s.spawn(proc.spec); err != nil {
		log.Printf("%s 重启失败: %v", proc.Name, err)
		s.mu.Lock()
		if cur, ok := s.procs[proc.Name]; ok {
			cur.running = false
			cur.lastExit = "重启失败: " + err.Error()
			cur.restarts = prevRestarts + 1
		}
		s.mu.Unlock()
		s.notify()
		return
	}
	s.mu.Lock()
	if cur, ok := s.procs[proc.Name]; ok {
		cur.restarts = prevRestarts + 1
	}
	s.mu.Unlock()
	s.notify()
}

// watch 等待子进程退出，并按退避策略重启。
func (s *Supervisor) watch(proc *Proc) {
	err := proc.Cmd.Wait()
	exit := "已退出"
	if err != nil {
		exit = err.Error()
	}
	uptime := time.Since(proc.startedAt)

	s.mu.Lock()
	proc.running = false
	proc.lastExit = exit
	stopping := s.stopping
	restart := proc.spec.restart
	restarts := proc.restarts
	s.mu.Unlock()

	if proc.log != nil {
		_ = proc.log.Close()
		proc.log = nil
	}
	close(proc.done)

	if stopping || !restart {
		log.Printf("%s 已停止（%s，运行 %s）", proc.Name, exit, uptime.Round(time.Second))
		s.notify()
		return
	}

	// 连续快速退出说明是配置/端口问题，重启只会刷日志。
	if uptime < 10*time.Second && restarts >= 5 {
		log.Printf("%s 连续 %d 次在 10 秒内退出，停止自动重启（最后一次: %s）", proc.Name, restarts+1, exit)
		s.mu.Lock()
		proc.lastExit = "反复退出，已停止重试: " + exit
		s.mu.Unlock()
		s.notify()
		return
	}

	delay := backoffFor(restarts, uptime)
	log.Printf("%s 退出（%s，运行 %s），%s 后重启（第 %d 次）", proc.Name, exit, uptime.Round(time.Second), delay, restarts+1)

	go func() {
		select {
		case <-time.After(delay):
		case <-s.stopSignal():
			return
		}
		s.mu.Lock()
		sameProc := s.procs[proc.Name] == proc
		stopping := s.stopping
		s.mu.Unlock()
		if stopping || !sameProc {
			return
		}
		s.redialProc(proc)
	}()
}

// backoffFor 计算重启延迟：1s 起翻倍，封顶 60s；跑够久的进程重置为 1s。
func backoffFor(restarts int, uptime time.Duration) time.Duration {
	if uptime >= time.Minute {
		return time.Second
	}
	delay := time.Second
	for i := 0; i < restarts && delay < time.Minute; i++ {
		delay *= 2
	}
	if delay > time.Minute {
		delay = time.Minute
	}
	return delay
}

// stopSignal 返回一个在 Stop() 时关闭的通道，供退避等待提前结束。
func (s *Supervisor) stopSignal() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopCh == nil {
		s.stopCh = make(chan struct{})
	}
	return s.stopCh
}

// adopt 接管一个已经启动的进程（PostgreSQL 自己负责 Start 与等待就绪）。
func (s *Supervisor) adopt(name string, cmd *exec.Cmd, spec launchSpec) {
	proc := &Proc{
		Name:      name,
		Cmd:       cmd,
		spec:      spec,
		done:      make(chan struct{}),
		startedAt: time.Now(),
		running:   true,
	}
	s.mu.Lock()
	s.procs[name] = proc
	s.mu.Unlock()
	s.notify()
	go s.watch(proc)
}

// Status 返回各子进程当前状态（按固定顺序，便于稳定展示）。
func (s *Supervisor) Status() []ProcStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ProcStatus, 0, len(s.procs))
	for _, name := range []string{"postgres", "kkmock", "gateway", "control"} {
		if proc, ok := s.procs[name]; ok {
			out = append(out, ProcStatus{
				Name:     name,
				Running:  proc.running,
				Restarts: proc.restarts,
				LastExit: proc.lastExit,
			})
		}
	}
	return out
}

// StartHealthLoop 启动健康自检：连续 3 次探测失败就杀掉子进程交给看护重启。
//
// 为什么不只靠 Wait()：进程活着但卡死（PG 连接耗尽、事件循环阻塞）时
// Wait 永远不返回，只有主动探测能发现。
func (s *Supervisor) StartHealthLoop(grace time.Duration) {
	go func() {
		// 启动初期给足时间：控制台冷启动要等 Next 初始化。
		time.Sleep(grace)
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			s.mu.Lock()
			stopping := s.stopping
			gatewayPort := s.settings.GatewayPort
			controlPort := s.settings.ControlPort
			s.mu.Unlock()
			if stopping {
				return
			}
			// 探回环：局域网模式下监听 0.0.0.0，但本机真正确认可用的地址仍是回环。
			s.checkOne("gateway", fmt.Sprintf("http://127.0.0.1:%d/healthz", gatewayPort))
			s.checkOne("control", fmt.Sprintf("http://127.0.0.1:%d/login", controlPort))
		}
	}()
}

func (s *Supervisor) checkOne(name, url string) {
	s.mu.Lock()
	proc, ok := s.procs[name]
	if !ok || !proc.running {
		s.healthFails[name] = 0
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()

	ctx, cancel := timeoutCtx(2 * time.Second)
	defer cancel()
	if httpHealthy(ctx, url) {
		s.mu.Lock()
		s.healthFails[name] = 0
		s.mu.Unlock()
		return
	}

	s.mu.Lock()
	s.healthFails[name]++
	fails := s.healthFails[name]
	s.mu.Unlock()
	if fails < 3 {
		log.Printf("%s 健康检查失败（第 %d/3 次）", name, fails)
		return
	}
	log.Printf("%s 连续 %d 次健康检查失败，判定卡死，重启", name, fails)
	s.mu.Lock()
	s.healthFails[name] = 0
	s.mu.Unlock()
	s.killProc(name)
}

// killProc 杀掉指定子进程，watch 会接手重启。
func (s *Supervisor) killProc(name string) {
	s.mu.Lock()
	proc, ok := s.procs[name]
	s.mu.Unlock()
	if !ok || proc.Cmd == nil || proc.Cmd.Process == nil {
		return
	}
	_ = proc.Cmd.Process.Kill()
}

// Stop 依次停掉所有子进程。
//
// 顺序有讲究：HTTP 服务先停（它们还在写库），数据库最后停，而且必须走
// pg_ctl -m fast，**不能**用 Kill。
//
// 为什么不能把 PostgreSQL 也交给 terminate 强杀：Windows 上没有 SIGTERM，
// Kill 等价于进程被瞬间抹掉，PostgreSQL 来不及做关闭检查点，后果是两个：
//
//  1. data\pg\postmaster.pid 残留——安装器的 StopRunningInstance 拿它判断
//     「库真的停了」，于是每次升级都白等满 20 秒；
//  2. 下一次启动必然重放 WAL（postgres.log 里 "database system was not
//     properly shut down; automatic recovery in progress"），首启变慢。
//
// 实测对照：手工 pg_ctl 停库时 postgres.log 出现 "database system is shut
// down" 且 pid 文件随即消失；走 Kill 时两者都没有。旧实现把 postgres 也排在
// running 里先 Kill，后面的 StopPostgres 只会在库已死的情况下失败，而它的
// 返回值又被丢弃，于是这条缺陷一直没暴露。
func (s *Supervisor) Stop() {
	s.mu.Lock()
	if s.stopping {
		s.mu.Unlock()
		return
	}
	s.stopping = true
	if s.stopCh != nil {
		close(s.stopCh)
	}
	running := make([]*Proc, 0, len(s.procs))
	var database *Proc
	for _, proc := range s.procs {
		if proc.Name == postgresProcName {
			// 数据库单独收尾，不进强杀列表。
			database = proc
			continue
		}
		running = append(running, proc)
	}
	s.mu.Unlock()

	sortForShutdown(running)
	for _, proc := range running {
		s.terminate(proc)
	}

	if database == nil {
		return
	}
	// 库已经自己退了（异常退出、被外部停掉）就没什么可停的：此时再走 pg_ctl
	// 只会拿到「停止失败」，把「本来就没在跑」写成一条看起来像故障的日志。
	s.mu.Lock()
	alive := database.running
	s.mu.Unlock()
	if !alive {
		return
	}
	if err := s.layout.StopPostgres(); err != nil {
		// 干净停库不成立时才退回强杀：宁可留下恢复标记，也不能让库继续
		// 占着端口和数据目录锁把下一次启动挡死。
		log.Printf("PostgreSQL 未能干净停止（%v），退回强制终止；下次启动将重放 WAL", err)
		s.terminate(database)
		return
	}
	s.waitDone(database, 30*time.Second)
}

// terminate 杀进程并等 watch 收尾（避免与 watch 争抢 cmd.Wait）。
func (s *Supervisor) terminate(proc *Proc) {
	if proc == nil || proc.Cmd == nil || proc.Cmd.Process == nil {
		return
	}
	_ = proc.Cmd.Process.Kill()
	s.waitDone(proc, 10*time.Second)
}

// waitDone 等 watch 协程把退出收完，超时只记日志。
func (s *Supervisor) waitDone(proc *Proc, timeout time.Duration) {
	if proc == nil {
		return
	}
	select {
	case <-proc.done:
	case <-time.After(timeout):
		log.Printf("%s 停止超时（%s）", proc.Name, timeout)
	}
}

// ---------------------------------------------------------------- helpers

// baseEnv 只保留必要的系统变量，其余由我们显式给定。
//
// 不继承全部环境：父进程可能带着开发期的 DATABASE_URL / KEYKEEP_* 变量，
// 继承下来会让桌面版连到错误的库。
func baseEnv() []string {
	keep := []string{
		"SystemRoot", "SystemDrive", "windir", "TEMP", "TMP",
		"PATH", "NUMBER_OF_PROCESSORS", "PROCESSOR_ARCHITECTURE", "USERPROFILE",
	}
	env := make([]string, 0, len(keep)+16)
	for _, key := range keep {
		if value := os.Getenv(key); value != "" {
			env = append(env, key+"="+value)
		}
	}
	return env
}

func (s *Supervisor) gatewayEnv(configPath string) []string {
	s.mu.Lock()
	settings := s.settings
	s.mu.Unlock()

	env := baseEnv()
	env = append(env,
		"KEYKEEP_GATEWAY_LISTEN="+net.JoinHostPort(settings.ListenHostGateway(), strconv.Itoa(settings.GatewayPort)),
		"KEYKEEP_SDK_ADDR=127.0.0.1:18080",
		"KEYKEEP_SDK_CONFIG="+configPath,
		"KEYKEEP_DATABASE_URL="+settings.GatewayDatabaseURL(),
		"KEYKEEP_INTERNAL_KEY_PREFIX=ik_",
		"KEYKEEP_KEK="+settings.KEK,
		// 与控制面同一把审计链密钥，理由见 controlEnv。
		"KEYKEEP_AUDIT_CHAIN_KEY="+settings.AuditChainKey,
		"KEYKEEP_LOCAL_HTTP=1",
		"TZ=Asia/Shanghai",
	)
	return env
}

func (s *Supervisor) controlEnv() []string {
	s.mu.Lock()
	settings := s.settings
	s.mu.Unlock()

	env := baseEnv()
	env = append(env,
		"NODE_ENV=production",
		"NEXT_TELEMETRY_DISABLED=1",
		"PORT="+strconv.Itoa(settings.ControlPort),
		"HOSTNAME="+settings.ListenHostControl(),
		"DATABASE_URL="+settings.DatabaseURL(),
		"SESSION_SECRET="+settings.SessionSecret,
		"KEYKEEP_KEK="+settings.KEK,
		// 控制面与网关共用同一把审计链密钥。两边不一致时，先写入的一方算出的
		// 摘要会被另一方判成 hash_mismatch——所以只从同一个 settings.json 下发。
		"KEYKEEP_AUDIT_CHAIN_KEY="+settings.AuditChainKey,
		"KEYKEEP_GATEWAY_URL="+settings.GatewayURL(),
		// 员工页要展示的是对外地址，不是回环地址。
		// 局域网模式关闭时这两项就是回环，与旧行为一致。
		"KEYKEEP_PUBLIC_GATEWAY_URL="+settings.PublicGatewayURL(),
		"KEYKEEP_PUBLIC_CONSOLE_URL="+settings.PublicControlURL(),
		"GATEWAY_PORT="+strconv.Itoa(settings.GatewayPort),
		// 本地是 http://127.0.0.1；生产模式默认给 Cookie 加 Secure，浏览器会丢掉它，
		// 表现成「登录成功但仍停在登录页」。这里必须显式关掉。
		"KEYKEEP_COOKIE_SECURE=false",
		"KEYKEEP_VERSION="+settings.Version,
		"KEYKEEP_MACHINE_CODE="+settings.MachineCode,
		"KEYKEEP_MASK_MODE=rules-only",
		// 离网环境不该因为拉不到 models.dev 而刷日志。
		"MODEL_CATALOG_REFRESH_HOURS=0",
		"TZ=Asia/Shanghai",
	)
	return env
}

// timeoutCtx 是 context.WithTimeout 的薄封装，供健康探针使用。
func timeoutCtx(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

// httpHealthy 探一次 URL，2xx/3xx 视为健康。
func httpHealthy(ctx context.Context, url string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	// 只读少量字节，避免探针把响应体拖长。
	_, _ = io.CopyN(io.Discard, resp.Body, 4096)
	return resp.StatusCode < 400
}

// runLogged 同步执行并等待，输出追加到日志文件。
func runLogged(cmd *exec.Cmd, logPath string) error {
	file, err := openLog(logPath)
	if err != nil {
		return err
	}
	defer file.Close()
	cmd.Stdout = file
	cmd.Stderr = file
	return cmd.Run()
}

// startLogged 异步启动，输出追加到日志文件。
//
// 只用于不由 Supervisor 看护的进程（内置 PostgreSQL）。被看护的进程走 spawn，
// 那里有退出码记录与重启。
func startLogged(cmd *exec.Cmd, logPath string) error {
	file, err := openLog(logPath)
	if err != nil {
		return err
	}
	cmd.Stdout = file
	cmd.Stderr = file
	if err := cmd.Start(); err != nil {
		file.Close()
		return err
	}
	return nil
}

func openLog(path string) (io.WriteCloser, error) {
	return OpenLog(path)
}

// waitTCP 等端口可连（子进程启动是异步的，直接发请求会 connection refused）。
func waitTCP(addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		if err == nil {
			conn.Close()
			return nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	return fmt.Errorf("等待 %s 就绪超时（%s）", addr, timeout)
}

// WaitHTTP 等 URL 返回 2xx/3xx，用于判断控制台真正可用。
func WaitHTTP(url string, timeout time.Duration) error {
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(timeout)
	var last error
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode < 400 {
				return nil
			}
			last = fmt.Errorf("HTTP %d", resp.StatusCode)
		} else {
			last = err
		}
		time.Sleep(400 * time.Millisecond)
	}
	if last == nil {
		last = fmt.Errorf("timeout")
	}
	return fmt.Errorf("等待 %s 就绪超时: %w", url, last)
}
