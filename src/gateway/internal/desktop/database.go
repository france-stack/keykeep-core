package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// pgBin 返回内置 PostgreSQL 的 bin 目录。
func (l *Layout) pgBin() string { return filepath.Join(l.PGDir(), "bin") }

// InitPGData 在数据目录里初始化一个 PostgreSQL 集群（只做一次）。
//
// 口令通过临时 pwfile 传入而不是命令行：命令行参数在 Windows 上对同用户
// 其它进程可见（任务管理器、WMI），写文件后立刻删除更稳妥。
func (l *Layout) InitPGData(settings Settings, logPath string) error {
	initdb, err := safeExe(l.pgBin(), "initdb.exe")
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(l.PGDataDir(), "PG_VERSION")); err == nil {
		return nil // 已初始化
	}
	if err := os.MkdirAll(l.PGDataDir(), 0o755); err != nil {
		return err
	}
	passwordFile := filepath.Join(l.DataDir(), ".pgpass.tmp")
	if err := os.WriteFile(passwordFile, []byte(settings.PGPassword), 0o600); err != nil {
		return err
	}
	defer os.Remove(passwordFile)

	cmd := command(initdb,
		"-D", l.PGDataDir(),
		"-U", "keykeep",
		"--pwfile="+passwordFile,
		"--auth-host=scram-sha-256",
		"--auth-local=trust",
		"--encoding=UTF8",
		"--locale=C",
	)
	cmd.Env = append(baseEnv(), "PGPASSWORD="+settings.PGPassword)
	return runLogged(cmd, logPath)
}

// StartPostgres 启动内置 PostgreSQL 并等待端口可连。
func (l *Layout) StartPostgres(settings Settings, logPath string) (*exec.Cmd, error) {
	postgres, err := safeExe(l.pgBin(), "postgres.exe")
	if err != nil {
		return nil, err
	}
	cmd := command(postgres,
		"-D", l.PGDataDir(),
		"-p", strconv.Itoa(settings.PGPort),
		"-h", "127.0.0.1",
	)
	cmd.Env = append(baseEnv(), "PGPASSWORD="+settings.PGPassword)
	if err := startLogged(cmd, logPath); err != nil {
		return nil, err
	}
	addr := "127.0.0.1:" + strconv.Itoa(settings.PGPort)
	if err := waitTCP(addr, 90*time.Second); err != nil {
		return cmd, fmt.Errorf("PostgreSQL 未能启动，请查看 logs/postgres.log：%w", err)
	}
	return cmd, nil
}

// EnsureDatabase 建业务库（幂等）。连的是默认的 postgres 维护库。
//
// 端口能连上 ≠ 数据库能接受查询：PostgreSQL 启动时要先做完 WAL 重放才会
// 接受连接，这期间它接受 TCP 但回 57P03（the database system is starting up）。
// 所以这里按「能成功执行一次查询」为就绪标准，并重试。
func (l *Layout) EnsureDatabase(settings Settings) error {
	adminURL := fmt.Sprintf("postgresql://keykeep:%s@127.0.0.1:%d/postgres?sslmode=disable",
		settings.PGPassword, settings.PGPort)

	return waitDatabaseReady(ensureDatabaseOnce, adminURL, 90*time.Second, 700*time.Millisecond)
}

// readyProbe 是「一次就绪探测」。抽成函数类型是为了让回归用例能注入
// 「端口开但查询失败」「connection refused」等状态，而不必起真实 PostgreSQL。
type readyProbe func(adminURL string) error

// waitDatabaseReady 按就绪语义重试探测。
//
// 只重试「正在启动」类错误；其它错误（认证失败、库不可查）立即返回——把它们
// 也当成就绪会让调用方在数据库根本不可用时继续起服务，表现就是「端口开了、
// 页面却全 500」这种最难排查的假阳性（F9）。
func waitDatabaseReady(probe readyProbe, adminURL string, deadline, interval time.Duration) error {
	var lastErr error
	limit := time.Now().Add(deadline)
	for time.Now().Before(limit) {
		lastErr = probe(adminURL)
		if lastErr == nil {
			return nil
		}
		if !retryableReadyError(lastErr) {
			return lastErr
		}
		time.Sleep(interval)
	}
	return fmt.Errorf("等待 PostgreSQL 就绪超时: %w", lastErr)
}

// retryableReadyError 判断错误是否属于「再等一会就好」。
func retryableReadyError(err error) bool {
	message := err.Error()
	return strings.Contains(message, "starting up") ||
		strings.Contains(message, "57P03") ||
		strings.Contains(message, "connection refused")
}

// ensureDatabaseOnce 单次尝试：能查到就返回，库不存在就建。
func ensureDatabaseOnce(adminURL string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	var exists bool
	if err := pool.QueryRow(ctx,
		"SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = 'keykeep')").Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}
	_, err = pool.Exec(ctx, "CREATE DATABASE keykeep")
	// 并发首装时另一个进程可能刚建好，重复创建不算失败。
	if err != nil && strings.Contains(err.Error(), "already exists") {
		return nil
	}
	return err
}

// StopPostgres 停止内置 PostgreSQL。
//
// 优先走 pg_ctl（会等 checkpoint 落盘）；pg_ctl 不在时退化为终止进程。
// Windows 上没有 SIGTERM 语义，直接 Kill 会留下恢复标记，下次启动要重放 WAL。
func (l *Layout) StopPostgres() error {
	pgCtl := filepath.Join(l.pgBin(), "pg_ctl.exe")
	if _, err := os.Stat(pgCtl); err == nil {
		cmd := command(pgCtl, "-D", l.PGDataDir(), "-m", "fast", "-w", "-t", "30", "stop")
		cmd.Env = baseEnv()
		if err := cmd.Run(); err == nil {
			return nil
		}
	}
	return errors.New("pg_ctl 停止失败")
}

// Migrate 执行数据库迁移。
//
// 迁移器是 Node 脚本（tools\migrate-desktop.mjs）：沿用 Prisma 的迁移目录与
// _prisma_migrations 记录表约定，但只用 pg 驱动，不依赖 Prisma CLI。
// 这样容器/开发环境继续用 prisma migrate deploy 管理同一套迁移，两边互认。
func (l *Layout) Migrate(currentDir string, settings Settings, logPath string) error {
	migrations := filepath.Join(currentDir, "control", "prisma", "migrations")
	cmd, err := l.toolCommand(currentDir, "migrate-desktop.mjs", "迁移脚本", "--dir", migrations)
	if err != nil {
		return err
	}
	cmd.Env = append(baseEnv(),
		"DATABASE_URL="+settings.DatabaseURL(),
		"CHECKPOINT_DISABLE=1",
	)
	return runLogged(cmd, logPath)
}

// Seed 写入首装数据：角色、管理员、演示供应商与模型价格。
//
// 用随包的 seed 脚本而不是在 Go 里复制一份数据库逻辑：口令哈希参数
// （Argon2id）与凭据加密格式必须与控制面完全一致，两处实现迟早会漂移。
func (l *Layout) Seed(currentDir string, settings Settings, adminPassword string, logPath string) error {
	cmd, err := l.toolCommand(currentDir, "seed-desktop.mjs", "首装种子脚本")
	if err != nil {
		return err
	}
	cmd.Env = append(baseEnv(),
		"DATABASE_URL="+settings.DatabaseURL(),
		"KEYKEEP_KEK="+settings.KEK,
		"KEYKEEP_SEED_ADMIN_PASSWORD="+adminPassword,
		"KEYKEEP_ADMIN_ACCOUNT="+settings.AdminAccount,
		// 桌面首装只有管理员，而「签发 Key」的目标必须是员工账号；
		// 不建演示员工，向导第 3 步的下拉框永远是空的。
		"KEYKEEP_SEED_EMPLOYEE_ACCOUNT=demo_employee",
		"KEYKEEP_DEMO_UPSTREAM="+settings.MockURL(),
		"KEYKEEP_DEMO_MODEL=claude-3-5-sonnet-20241022",
		"CHECKPOINT_DISABLE=1",
	)
	return runLogged(cmd, logPath)
}

// ResetAdminPassword 找回管理员口令：改写哈希、清零失败计数、撤销已有会话。
//
// 这条路径存在的原因：初始口令的唯一载体是「初始登录信息.txt」，而说明书让用户
// 改完口令就删掉它。删掉之后一旦忘记，此前唯一的手段是删 data\ 重装——用全部
// 业务数据换一次登录，这个代价不该是答案。
//
// 口令经环境变量而不是命令行参数下发：Windows 的进程命令行对同机其它进程可见
// （任务管理器、WMI、ETW），环境变量只对同一用户与管理员可见。
//
// enable 对应脚本的 --enable：账号被停用时只重置口令仍登不进去，是否解除停用
// 交给操作者显式决定。返回账号原先的停用状态，调用方据此提示下一步。
func (l *Layout) ResetAdminPassword(currentDir string, settings Settings, adminPassword string, enable bool, logPath string) (wasDisabled bool, err error) {
	args := []string{}
	if enable {
		args = append(args, "--enable")
	}
	cmd, err := l.toolCommand(currentDir, "reset-admin.mjs", "口令找回脚本", args...)
	if err != nil {
		return false, err
	}
	cmd.Env = append(baseEnv(),
		"DATABASE_URL="+settings.DatabaseURL(),
		"KEYKEEP_ADMIN_ACCOUNT="+settings.AdminAccount,
		"KEYKEEP_RESET_ADMIN_PASSWORD="+adminPassword,
		"CHECKPOINT_DISABLE=1",
	)
	// 脚本输出一行 JSON 摘要（不含口令）和给人看的提示语。这里同时写日志和内存：
	// 口令找回是人在终端前等结果的动作，把结论只埋在 setup.log 里等于没提示。
	file, err := openLog(logPath)
	if err != nil {
		return false, err
	}
	defer file.Close()
	var out bytes.Buffer
	cmd.Stdout = io.MultiWriter(file, &out)
	cmd.Stderr = io.MultiWriter(file, &out)
	runErr := cmd.Run()

	for _, line := range strings.Split(out.String(), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "{") {
			continue
		}
		var summary struct {
			WasDisabled bool `json:"wasDisabled"`
		}
		if json.Unmarshal([]byte(trimmed), &summary) == nil {
			wasDisabled = summary.WasDisabled
		}
	}
	if runErr != nil {
		// 脚本失败时最后一行是它给的原因（账号不存在、口令不合策略……），
		// 比「退出状态 1」有用得多。
		reason := lastNonEmptyLine(out.String())
		if reason == "" {
			reason = runErr.Error()
		}
		return wasDisabled, fmt.Errorf("重置管理员口令失败: %s", reason)
	}
	return wasDisabled, nil
}

// DatabaseUp 报告数据库当前是否已经可查；它不启动任何东西。
//
// 口令找回常常在服务运行时执行：此时 PostgreSQL 已经占着端口与数据目录锁，
// 再拉一个 postgres.exe 只会留下一条 FATAL 日志。结果其实仍然正确（连上的是
// 在跑的那个实例），但日志里多出「数据库起不来」的假线索——排障时最耗时的
// 正是这种假线索。
//
// 必须用 GatewayDatabaseURL 而不是 DatabaseURL：后者带 Prisma 专有的
// ?schema=public，pgx 不认它，会把它当作服务端启动参数发过去，PostgreSQL
// 直接回 42704 unrecognized configuration parameter "schema"。用错那一个的
// 后果不是「探测变慢」，而是本函数恒返回 false——于是调用方以为库没在跑，
// 再拉一个 postgres.exe（撞 postmaster.pid），并在收尾时把**正在服务的那
// 个库**停掉：备份一次，网关就 503。
func DatabaseUp(settings Settings) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, settings.GatewayDatabaseURL())
	if err != nil {
		return false
	}
	defer pool.Close()
	return pool.Ping(ctx) == nil
}

// toolCommand 组装一条随包 Node 脚本命令。
//
// 迁移、首装种子、口令找回三个脚本的调用形状完全相同。cmd.Dir 设成 tools\ 不是
// 顺手：脚本用 npm 平铺依赖（不是 pnpm 符号链接），require("pg") 要靠这个目录
// 才找得到同级的 node_modules。
func (l *Layout) toolCommand(currentDir, scriptName, hint string, args ...string) (*exec.Cmd, error) {
	nodeExe, err := safeExe(l.NodeDir(), "node.exe")
	if err != nil {
		return nil, fmt.Errorf("找不到内置 Node 运行时: %w", err)
	}
	script := filepath.Join(currentDir, "tools", scriptName)
	if _, err := os.Stat(script); err != nil {
		return nil, fmt.Errorf("找不到%s（%s）", hint, script)
	}
	cmd := command(nodeExe, append([]string{script}, args...)...)
	cmd.Dir = filepath.Join(currentDir, "tools")
	return cmd, nil
}

// lastNonEmptyLine 取最后一行非空文本：脚本把失败原因打在输出的末尾。
func lastNonEmptyLine(raw string) string {
	lines := strings.Split(raw, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if trimmed := strings.TrimSpace(lines[i]); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// logTail 返回日志文件末尾若干行，用于托盘「查看状态」时给出可读线索。
func logTail(path string, maxLines int) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\r\n"), "\n")
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return strings.Join(lines, "\n")
}
