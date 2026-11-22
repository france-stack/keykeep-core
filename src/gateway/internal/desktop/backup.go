package desktop

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// 桌面版的定时备份。
//
// 为什么要在程序里做而不是留给用户配计划任务：桌面版的目标用户是「装完就用」
// 的中小团队，没有任何人会在周末想起来配一个 schtasks。而「丢账」是这套系统
// 里唯一不可逆的故障——备份没跑起来这件事，必须由软件自己负责。
//
// 保留策略（份数）由 backup.mjs 的 --retain 执行：删除发生在一次成功备份之后，
// 顺序反了会出现「备份失败 + 清理成功 = 最后一份可用备份也没了」。

const (
	// backupRetain 是保留的备份份数。按每日一次算约一周，足够覆盖「周一才发现
	// 周五出问题」的常见节奏；再多会把办公机的磁盘吃出可感知的占用。
	backupRetain = 7
	// backupHour 是每天执行备份的本地时刻（24 小时制）。选凌晨是因为桌面版
	// 夜间通常空闲，备份的 IO 抖动不会落在有人用的时候。
	backupHour = 3
	// backupStaleAfter 判定「该补一次备份」的阈值：启动时若最近一份备份比这更旧，
	// 立刻补一份。关机数周后开机的机器不会等到下一个凌晨才有备份。
	backupStaleAfter = 26 * time.Hour
)

// BackupDir 返回备份目录（与 settings.json、KEK 不同目录）。
func (l *Layout) BackupDir() string { return filepath.Join(l.Root, "backups") }

// BackupManifestPattern 用于识别备份清单文件。
const backupManifestPattern = "keykeep-"

// LatestBackup 返回最近一份备份的 manifest 路径与它被写下的时刻；没有任何备份时返回空。
//
// 时间从文件名里的 UTC 时间戳解析，不取 mtime：备份目录常被同步盘/杀软碰过，
// mtime 会漂，而文件名是备份时写下的，不会变。
func (l *Layout) LatestBackup() (string, time.Time, error) {
	entries, err := os.ReadDir(l.BackupDir())
	if err != nil {
		if os.IsNotExist(err) {
			return "", time.Time{}, nil
		}
		return "", time.Time{}, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, backupManifestPattern) && strings.HasSuffix(name, ".manifest.json") {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return "", time.Time{}, nil
	}
	sort.Strings(names)
	latest := names[len(names)-1]
	created, ok := parseBackupStamp(latest)
	if !ok {
		// 名字不合约定时退回 mtime：宁可让「是否过期」的判断略偏，也不要
		// 因为一份手改过名的备份让整个备份循环停摆。
		info, statErr := os.Stat(filepath.Join(l.BackupDir(), latest))
		if statErr != nil {
			return "", time.Time{}, statErr
		}
		created = info.ModTime()
	}
	return filepath.Join(l.BackupDir(), latest), created, nil
}

// backupStampPattern 对应 backup.mjs 的时间戳：UTC ISO 去掉标点后
// 形如 2026-11-08-133029123（末尾的毫秒位数可变）。
var backupStampPattern = regexp.MustCompile(`^keykeep-(\d{4})-(\d{2})-(\d{2})-(\d{2})(\d{2})(\d{2})\d*(?:\.manifest\.json)$`)

// parseBackupStamp 从 manifest 文件名解析备份时刻。
func parseBackupStamp(name string) (time.Time, bool) {
	match := backupStampPattern.FindStringSubmatch(name)
	if match == nil {
		return time.Time{}, false
	}
	parts := make([]int, 0, 6)
	for _, field := range match[1:7] {
		value, err := strconv.Atoi(field)
		if err != nil {
			return time.Time{}, false
		}
		parts = append(parts, value)
	}
	// 时间戳由 toISOString() 生成，即 UTC。
	return time.Date(parts[0], time.Month(parts[1]), parts[2], parts[3], parts[4], parts[5], 0, time.UTC), true
}

// Backup 执行一次备份并立即校验产物。
//
// 校验是独立的一步而不是信任备份脚本的退码：脚本只保证「写完了」，
// 保证不了「写对了」。离线校验能发现截断、哈希不符、行数不符——
// 这些正是「恢复那天才发现备份是坏的」的成因。
func (l *Layout) Backup(currentDir string, settings Settings, logPath string) (string, error) {
	outDir := l.BackupDir()
	cmd, err := l.toolCommand(currentDir, "backup.mjs", "备份脚本",
		"--out", outDir, "--retain", fmt.Sprint(backupRetain))
	if err != nil {
		return "", err
	}
	cmd.Env = append(baseEnv(),
		"DATABASE_URL="+settings.DatabaseURL(),
		"KEYKEEP_KEK="+settings.KEK,
		"CHECKPOINT_DISABLE=1",
	)
	if err := runCaptured(cmd, logPath); err != nil {
		return "", fmt.Errorf("数据库备份失败: %w", err)
	}
	manifest, _, err := l.LatestBackup()
	if err != nil {
		return "", err
	}
	if manifest == "" {
		return "", fmt.Errorf("备份脚本成功退出但没有产出 manifest（%s）", outDir)
	}
	if err := l.VerifyBackup(currentDir, manifest, logPath); err != nil {
		return manifest, err
	}
	return manifest, nil
}

// VerifyBackup 离线校验一份备份（不连数据库）。
func (l *Layout) VerifyBackup(currentDir, manifestPath, logPath string) error {
	cmd, err := l.toolCommand(currentDir, "verify-backup.mjs", "备份校验脚本", "--manifest", manifestPath)
	if err != nil {
		return err
	}
	cmd.Env = append(baseEnv(), "CHECKPOINT_DISABLE=1")
	if err := runCaptured(cmd, logPath); err != nil {
		return fmt.Errorf("备份校验失败（%s）: %w", filepath.Base(manifestPath), err)
	}
	return nil
}

// BackupDue 判断是否该补一次备份。
func (l *Layout) BackupDue(now time.Time) (bool, error) {
	manifest, modified, err := l.LatestBackup()
	if err != nil {
		return false, err
	}
	if manifest == "" {
		return true, nil
	}
	return now.Sub(modified) >= backupStaleAfter, nil
}

// StartBackupLoop 在后台按日执行备份。
//
// 启动时先补一次（如果最近一份已过期），之后每天在 backupHour 跑。
// 用「睡到下一个整点再判断」而不是固定 ticker：机器休眠/休眠唤醒会让
// ticker 的累积时间漂移，按钟点判断才能保证「每天凌晨一次」的语义。
func (l *Layout) StartBackupLoop(currentDir string, settings Settings, logPath string, onError func(error)) {
	go func() {
		for {
			now := time.Now()
			if due, err := l.BackupDue(now); err != nil {
				log.Printf("备份状态检查失败: %v", err)
			} else if due {
				if _, err := l.Backup(currentDir, settings, logPath); err != nil {
					log.Printf("%v", err)
					if onError != nil {
						onError(err)
					}
				} else {
					log.Printf("已自动备份数据库到 %s", l.BackupDir())
				}
			}
			next := now.Truncate(time.Hour).Add(time.Hour)
			for next.Hour() != backupHour {
				next = next.Add(time.Hour)
			}
			time.Sleep(time.Until(next))
		}
	}()
}

// runCaptured 同步执行并把输出追加到日志文件，失败时把最后一行带进错误。
//
// 与 runLogged 的区别：这里需要「失败原因」这一段文本给托盘提示用，
// 所以额外留一份尾部输出，而不是只写进文件。
func runCaptured(cmd *exec.Cmd, logPath string) error {
	file, err := openLog(logPath)
	if err != nil {
		return err
	}
	defer file.Close()
	var tail strings.Builder
	writer := &tailWriter{file: file, tail: &tail}
	cmd.Stdout = writer
	cmd.Stderr = writer
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w（%s）", err, lastNonEmptyLine(tail.String()))
	}
	return nil
}

// tailWriter 把输出写进日志的同时保留最后一段，供错误提示使用。
type tailWriter struct {
	file io.Writer
	tail *strings.Builder
}

func (w *tailWriter) Write(data []byte) (int, error) {
	const keep = 4096
	if w.tail.Len() < keep {
		w.tail.Write(data)
	}
	return w.file.Write(data)
}

// backupSummary 是 manifest 的极小子集：只用于日志与托盘提示。
type backupSummary struct {
	CreatedAt string `json:"createdAt"`
}

// ReadBackupSummary 读一份备份的创建时间（托盘展示用）。
func ReadBackupSummary(manifestPath string) (backupSummary, error) {
	var summary backupSummary
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return summary, err
	}
	if err := json.Unmarshal(raw, &summary); err != nil {
		return summary, err
	}
	return summary, nil
}
