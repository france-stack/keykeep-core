// Package desktop 是 Windows 桌面版（KeyKeep.exe / kkdesk）的运行时骨架：
// 目录布局、首装初始化、子进程监管、健康检查、更新。
//
// 设计约束（见 ADR-011）：
//   - 只绑回环地址，不触发 Windows 防火墙弹窗，也不需要管理员权限；
//   - 安装目录与数据目录分离：程序放 <安装目录>\app\<版本>\，数据放 %LOCALAPPDATA%\KeyKeep\data；
//   - 版本目录独立，升级 = 解包新版本目录 + 切 current 指针，绝不覆盖正在运行的 exe。
package desktop

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// 默认端口。全部绑回环，避开常见占用（8080/3000/5432 在开发机上常年被占）。
const (
	DefaultControlPort = 8443
	DefaultGatewayPort = 8080
	DefaultMockPort    = 19011
	DefaultPGPort      = 55432
)

// Layout 描述一套安装的全部路径。
//
// 便携版与安装版用同一套结构，区别只在 Root：
//   - 安装版：%LOCALAPPDATA%\KeyKeep（安装器写入）
//   - 便携版：解压目录
//
// Data 永远在 Root\data 下，升级时不碰它。
type Layout struct {
	Root string // 安装根目录
}

// NewLayout 从可执行文件位置推导布局：优先用 KEYKEEP_HOME，其次用 exe 所在目录的上一级。
func NewLayout() (*Layout, error) {
	if home := strings.TrimSpace(os.Getenv("KEYKEEP_HOME")); home != "" {
		return &Layout{Root: filepath.Clean(home)}, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("定位程序目录失败: %w", err)
	}
	// exe 在 <Root>\app\<版本>\ 下；便携版在 <Root>\app\<版本>\ 下同样成立。
	dir := filepath.Dir(exe)
	root := filepath.Dir(filepath.Dir(dir))
	if filepath.Base(filepath.Dir(dir)) != "app" {
		// 退化情形：直接放在某目录里运行，就用该目录。
		root = dir
	}
	return &Layout{Root: root}, nil
}

func (l *Layout) AppDir() string       { return filepath.Join(l.Root, "app") }
func (l *Layout) DataDir() string      { return filepath.Join(l.Root, "data") }
func (l *Layout) RuntimeDir() string   { return filepath.Join(l.Root, "runtime") }
func (l *Layout) LogDir() string       { return filepath.Join(l.Root, "logs") }
func (l *Layout) BinDir() string       { return filepath.Join(l.Root, "bin") }
func (l *Layout) CurrentFile() string  { return filepath.Join(l.Root, "current.txt") }
func (l *Layout) SettingsFile() string { return filepath.Join(l.Root, "settings.json") }
func (l *Layout) PGDir() string        { return filepath.Join(l.RuntimeDir(), "pgsql") }
func (l *Layout) NodeDir() string      { return filepath.Join(l.RuntimeDir(), "node") }
func (l *Layout) PGDataDir() string    { return filepath.Join(l.DataDir(), "pg") }

// WelcomeFileName 是首装生成的一次性登录信息文件。
//
// 它是初始管理员口令在这台机器上的唯一载体：settings.json 里不留副本（C13），
// 「忘了口令」这条运维路径因此只有一个出口，也只需要删这一个文件。
const WelcomeFileName = "初始登录信息.txt"

func (l *Layout) WelcomeFile() string { return filepath.Join(l.Root, WelcomeFileName) }

// WelcomeFileExists 报告初始登录信息是否还在（文件里是明文口令）。
func (l *Layout) WelcomeFileExists() bool {
	_, err := os.Stat(l.WelcomeFile())
	return err == nil
}

// CurrentVersion 读 current.txt；文件不存在时返回空串（首装）。
func (l *Layout) CurrentVersion() (string, error) {
	raw, err := os.ReadFile(l.CurrentFile())
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(raw)), nil
}

// SetCurrentVersion 原子写 current.txt（先写临时文件，压盘后再改名）。
// current.txt 是版本指针：断电后半截文件会让下次启动解析失败。
func (l *Layout) SetCurrentVersion(version string) error {
	if err := os.MkdirAll(l.Root, 0o755); err != nil {
		return err
	}
	tmp := l.CurrentFile() + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write([]byte(version + "\r\n")); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, l.CurrentFile()); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// VersionDir 返回某个版本的程序目录。
func (l *Layout) VersionDir(version string) string {
	return filepath.Join(l.AppDir(), version)
}

// InstalledVersions 列出 app/ 下所有版本目录，按名称排序。
func (l *Layout) InstalledVersions() ([]string, error) {
	entries, err := os.ReadDir(l.AppDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	versions := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		if _, statErr := os.Stat(filepath.Join(l.AppDir(), entry.Name(), exeName())); statErr != nil {
			continue // 不是完整版本目录（解包中断的残留）
		}
		versions = append(versions, entry.Name())
	}
	return versions, nil
}

// EnsureDirs 建好运行期需要的全部目录。
func (l *Layout) EnsureDirs() error {
	for _, dir := range []string{
		l.AppDir(), l.DataDir(), l.RuntimeDir(), l.LogDir(), l.BinDir(), l.PGDataDir(),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("创建目录 %s 失败: %w", dir, err)
		}
	}
	return nil
}

// exeName 返回当前平台的桌面程序名。
func exeName() string {
	if runtime.GOOS == "windows" {
		return "kkdesk.exe"
	}
	return "kkdesk"
}

// ShellPath 返回稳定外壳的路径（安装根下的 KeyKeep.exe）。
//
// 自启项必须指向它而不是当前 kkdesk.exe：kkdesk 在 app\<版本>\ 里，
// 升级并清理旧版本后该路径就失效了，自启会静默变成一个打不开的快捷方式。
func (l *Layout) ShellPath() string {
	if runtime.GOOS == "windows" {
		return filepath.Join(l.Root, "KeyKeep.exe")
	}
	return filepath.Join(l.Root, "KeyKeep")
}

// ShellName 返回平台对应的子进程文件名（psql 之类缺扩展名的除外）。
func ShellName(base string) string {
	if runtime.GOOS == "windows" && !strings.Contains(base, ".") {
		return base + ".exe"
	}
	return base
}
