package desktop

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 备份目录的「最近一份」判定决定了要不要补备份，也决定了托盘上那句
// 「上次备份」是不是真的。这里覆盖三种状态：没有备份、有备份且新鲜、有备份但过期。
func TestBackupDue(t *testing.T) {
	layout := &Layout{Root: t.TempDir()}

	due, err := layout.BackupDue(time.Now())
	if err != nil {
		t.Fatalf("空目录检查失败: %v", err)
	}
	if !due {
		t.Fatal("没有任何备份时必须判定为需要备份")
	}

	path := writeFakeManifest(t, layout, time.Now().Add(-2*time.Hour))
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("假 manifest 没写出来: %v", err)
	}

	due, err = layout.BackupDue(time.Now())
	if err != nil {
		t.Fatalf("新鲜备份检查失败: %v", err)
	}
	if due {
		t.Fatal("两小时前的备份不应判定为过期")
	}

	// 换一个干净的目录再放「30 小时前」那份：同一目录里的新鲜备份会让
	// LatestBackup 仍然指向它，测的就不是过期判定了。
	stale := &Layout{Root: t.TempDir()}
	writeFakeManifest(t, stale, time.Now().Add(-30*time.Hour))
	due, err = stale.BackupDue(time.Now())
	if err != nil {
		t.Fatalf("过期备份检查失败: %v", err)
	}
	if !due {
		t.Fatal("30 小时前的备份必须判定为过期（阈值 26 小时）")
	}
}

// 备份时刻必须从文件名解析，而不是 mtime：备份目录被同步盘碰过之后
// mtime 会集体变成「刚刚」，那时「是否过期」的判断就完全失效了。
func TestLatestBackupUsesFilenameTimestamp(t *testing.T) {
	layout := &Layout{Root: t.TempDir()}
	old := time.Date(2026, 3, 1, 3, 0, 0, 0, time.UTC)
	path := writeFakeManifest(t, layout, old)
	// 把 mtime 改成「刚刚」，模拟同步工具或杀软碰过目录。
	future := time.Now()
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatalf("改 mtime 失败: %v", err)
	}

	_, created, err := layout.LatestBackup()
	if err != nil {
		t.Fatalf("解析最近备份失败: %v", err)
	}
	if want := time.Date(2026, 3, 1, 3, 0, 0, 0, time.UTC); !created.Equal(want) {
		t.Fatalf("备份时刻 = %s，期望从文件名解析出 %s", created, want)
	}
}

// 名字不符合约定的 manifest 不能让整条备份循环停摆。
func TestLatestBackupFallsBackForUnusualName(t *testing.T) {
	layout := &Layout{Root: t.TempDir()}
	dir := layout.BackupDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("建目录失败: %v", err)
	}
	path := filepath.Join(dir, "keykeep-manual.manifest.json")
	if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
		t.Fatalf("写文件失败: %v", err)
	}

	manifest, created, err := layout.LatestBackup()
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if manifest != path {
		t.Fatalf("manifest = %q，期望 %q", manifest, path)
	}
	if created.IsZero() {
		t.Fatal("名字不合约定时应当退回 mtime，而不是返回零值时间")
	}
}

// writeFakeManifest 按 backup.mjs 的命名规则写一份空 manifest。
func writeFakeManifest(t *testing.T, layout *Layout, at time.Time) string {
	t.Helper()
	dir := layout.BackupDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("建备份目录失败: %v", err)
	}
	// 与 backup.mjs 的 timestamp() 同形：UTC ISO 去标点。
	stamp := at.UTC().Format("2006-01-02-150405")
	name := "keykeep-" + stamp + ".manifest.json"
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(`{"format":1,"createdAt":"`+at.UTC().Format(time.RFC3339)+`"}`), 0o644); err != nil {
		t.Fatalf("写 manifest 失败: %v", err)
	}
	return path
}
