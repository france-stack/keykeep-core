package desktop

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeDirFS 是可注入故障的内存目录系统。
//
// 存在的理由见 dirFS 的注释：真机上「node.exe 正在跑 → 目录改名被拒」复现不了，
// 而 A6 要保证的恰恰是这条失败路径不删掉唯一能启动的运行时。
// 这里只建模「哪些目录在位」和「改名/删除按指令失败」，不去模拟 Windows 句柄细节。
type fakeDirFS struct {
	dirs       map[string]bool
	renamed    [][2]string
	removed    []string
	calls      map[string]int
	failRename func(oldPath, newPath string, attempt int) error
}

func newFakeDirFS(dirs ...string) *fakeDirFS {
	set := make(map[string]bool, len(dirs))
	for _, dir := range dirs {
		set[dir] = true
	}
	return &fakeDirFS{dirs: set, calls: map[string]int{}}
}

func (f *fakeDirFS) IsDir(path string) bool { return f.dirs[path] }

func (f *fakeDirFS) Rename(oldPath, newPath string) error {
	f.calls[oldPath]++
	if f.failRename != nil {
		if err := f.failRename(oldPath, newPath, f.calls[oldPath]); err != nil {
			return err
		}
	}
	if f.dirs[newPath] {
		return fmt.Errorf("目标目录已存在: %s", newPath)
	}
	if !f.dirs[oldPath] {
		return fmt.Errorf("源目录不存在: %s", oldPath)
	}
	delete(f.dirs, oldPath)
	f.dirs[newPath] = true
	f.renamed = append(f.renamed, [2]string{oldPath, newPath})
	return nil
}

func (f *fakeDirFS) RemoveAll(path string) error {
	delete(f.dirs, path)
	f.removed = append(f.removed, path)
	return nil
}

func lockedErr() error {
	return fmt.Errorf("另一个进程正在使用此文件: %w", os.ErrPermission)
}

// TestSwapRuntimeReplacesCleanly 固定成功路径：新版就位、备份清掉、源目录不再留副本。
func TestSwapRuntimeReplacesCleanly(t *testing.T) {
	fs := newFakeDirFS("/root/runtime", "/pkg/runtime")
	if err := swapRuntime("/root/runtime", "/pkg/runtime", fs, 3, 0); err != nil {
		t.Fatalf("替换失败: %v", err)
	}
	if !fs.dirs["/root/runtime"] {
		t.Fatal("runtime 不在位")
	}
	if fs.dirs["/pkg/runtime"] {
		t.Fatal("源 runtime 应已移走")
	}
	if fs.dirs["/root/runtime.old"] {
		t.Fatal("让位后的备份应已清理")
	}
	want := [][2]string{{"/root/runtime", "/root/runtime.old"}, {"/pkg/runtime", "/root/runtime"}}
	if fmt.Sprint(fs.renamed) != fmt.Sprint(want) {
		t.Fatalf("改名顺序应为让位→就位，实际 %v", fs.renamed)
	}
}

// TestSwapRuntimeHealsInterruptedStateBeforeDeleting 是 A6 的数据丢失回归测试。
//
// 现场：上次替换在「让位之后、就位之前」断电，于是只有 runtime.old 能启动。
// 旧实现先 RemoveAll(runtime.old) 再判断 runtime 是否存在——正好把唯一可用
// 的运行时删了。这里要求恢复动作排在任何删除之前，且恢复失败时一个目录都不删。
func TestSwapRuntimeHealsInterruptedStateBeforeDeleting(t *testing.T) {
	fs := newFakeDirFS("/root/runtime.old", "/pkg/runtime")
	if err := swapRuntime("/root/runtime", "/pkg/runtime", fs, 3, 0); err != nil {
		t.Fatalf("替换失败: %v", err)
	}
	if fs.renamed[0] != [2]string{"/root/runtime.old", "/root/runtime"} {
		t.Fatalf("第一步必须把旧运行时放回原位，实际序列 %v", fs.renamed)
	}
	if !fs.dirs["/root/runtime"] {
		t.Fatal("新版没就位")
	}
}

func TestSwapRuntimeAbortsWithoutDeletingWhenHealFails(t *testing.T) {
	fs := newFakeDirFS("/root/runtime.old", "/pkg/runtime")
	fs.failRename = func(oldPath, newPath string, _ int) error {
		if oldPath == "/root/runtime.old" {
			return lockedErr()
		}
		return nil
	}
	err := swapRuntime("/root/runtime", "/pkg/runtime", fs, 3, 0)
	if err == nil {
		t.Fatal("恢复失败必须报错，不能假装替换完成")
	}
	if !strings.Contains(err.Error(), "/root/runtime.old") {
		t.Fatalf("错误要把可用现场的位置告诉用户: %v", err)
	}
	// 关键断言：唯一能启动的运行时仍在，且没被任何删除指令碰过。
	if !fs.dirs["/root/runtime.old"] {
		t.Fatal("runtime.old 被删了——机器上再没有可启动的运行时")
	}
	for _, path := range fs.removed {
		if path == "/root/runtime.old" {
			t.Fatal("恢复失败前就执行了清理")
		}
	}
}

// TestSwapRuntimeInUseKeepsCurrentRuntime 校验占用中不动现场。
func TestSwapRuntimeInUseKeepsCurrentRuntime(t *testing.T) {
	fs := newFakeDirFS("/root/runtime", "/pkg/runtime")
	fs.failRename = func(oldPath, newPath string, _ int) error {
		if oldPath == "/root/runtime" {
			return lockedErr()
		}
		return nil
	}
	err := swapRuntime("/root/runtime", "/pkg/runtime", fs, 3, 0)
	if !errors.Is(err, ErrRuntimeInUse) {
		t.Fatalf("应返回 ErrRuntimeInUse 供调用方推迟，实际 %v", err)
	}
	if !fs.dirs["/root/runtime"] {
		t.Fatal("原 runtime 必须完整留在原位")
	}
	if !fs.dirs["/pkg/runtime"] {
		t.Fatal("新版 runtime 应保留，供下次启动再换")
	}
	if fs.calls["/root/runtime"] != 3 {
		t.Fatalf("应按 attempts 重试 3 次，实际 %d", fs.calls["/root/runtime"])
	}
}

// TestSwapRuntimeRetriesThroughTransientLock 覆盖「进程刚退出、句柄未释放」窗口。
func TestSwapRuntimeRetriesThroughTransientLock(t *testing.T) {
	fs := newFakeDirFS("/root/runtime", "/pkg/runtime")
	fs.failRename = func(oldPath, newPath string, attempt int) error {
		if oldPath == "/root/runtime" && attempt < 3 {
			return lockedErr()
		}
		return nil
	}
	if err := swapRuntime("/root/runtime", "/pkg/runtime", fs, 6, 0); err != nil {
		t.Fatalf("瞬时占用应被重试吸收: %v", err)
	}
	if fs.calls["/root/runtime"] != 3 {
		t.Fatalf("第 3 次成功即停止，实际 %d", fs.calls["/root/runtime"])
	}
}

// TestSwapRuntimeNonLockErrorFailsFast 保证重试只用于真的在等锁。
// 非占用类错误（如目标已存在）反复重试只会把失败拖成 1.5 秒卡顿。
func TestSwapRuntimeNonLockErrorFailsFast(t *testing.T) {
	fs := newFakeDirFS("/root/runtime", "/pkg/runtime")
	fs.failRename = func(oldPath, _ string, _ int) error {
		if oldPath == "/root/runtime" {
			return errors.New("设备忙但未被锁定")
		}
		return nil
	}
	err := swapRuntime("/root/runtime", "/pkg/runtime", fs, 6, 0)
	if err == nil || errors.Is(err, ErrRuntimeInUse) {
		t.Fatalf("非占用错误应原样返回: %v", err)
	}
	if fs.calls["/root/runtime"] != 1 {
		t.Fatalf("非占用错误不该重试，实际 %d 次", fs.calls["/root/runtime"])
	}
}

// TestSwapRuntimeRollbackFailureNamesBackup 要求回滚也失败时把现场位置说全。
// 这条路径发生时用户只剩手工恢复一条路，错误信息就是唯一的操作手册。
func TestSwapRuntimeRollbackFailureNamesBackup(t *testing.T) {
	fs := newFakeDirFS("/root/runtime", "/pkg/runtime")
	fs.failRename = func(oldPath, newPath string, _ int) error {
		switch {
		case oldPath == "/pkg/runtime":
			return errors.New("磁盘空间不足")
		case oldPath == "/root/runtime.old" && newPath == "/root/runtime":
			return lockedErr()
		}
		return nil
	}
	err := swapRuntime("/root/runtime", "/pkg/runtime", fs, 3, 0)
	if err == nil {
		t.Fatal("就位失败必须报错")
	}
	if !strings.Contains(err.Error(), "回滚失败") || !strings.Contains(err.Error(), "/root/runtime.old") {
		t.Fatalf("错误要说明回滚失败并给出旧运行时位置: %v", err)
	}
	if !fs.dirs["/root/runtime.old"] {
		t.Fatal("旧运行时至少该留在 runtime.old 里")
	}
}

// TestSwapRuntimeRollbackSucceeds 校验失败后回到可启动状态。
func TestSwapRuntimeRollbackSucceeds(t *testing.T) {
	fs := newFakeDirFS("/root/runtime", "/pkg/runtime")
	fs.failRename = func(oldPath, _ string, _ int) error {
		if oldPath == "/pkg/runtime" {
			return errors.New("磁盘空间不足")
		}
		return nil
	}
	err := swapRuntime("/root/runtime", "/pkg/runtime", fs, 3, 0)
	if err == nil {
		t.Fatal("应返回就位失败")
	}
	if !fs.dirs["/root/runtime"] {
		t.Fatal("回滚后原 runtime 必须回到原位")
	}
	if fs.dirs["/root/runtime.old"] {
		t.Fatal("回滚后不该留下备份")
	}
	if !strings.Contains(err.Error(), "已回滚") {
		t.Fatalf("回滚成功要告诉用户 runtime 仍可用: %v", err)
	}
	if strings.Contains(err.Error(), "回滚失败") {
		t.Fatalf("回滚成功却声称失败会误导排障: %v", err)
	}
}

// TestSwapRuntimeMissingTargetDoesNotClaimRollbackFailure 覆盖首装形态：
// 根上本来就没有 runtime，也就没有「让位」，此时不能报一个根本不存在的回滚失败。
func TestSwapRuntimeMissingTargetDoesNotClaimRollbackFailure(t *testing.T) {
	fs := newFakeDirFS() // 连源目录都不存在
	err := swapRuntime("/root/runtime", "/pkg/runtime", fs, 3, 0)
	if err == nil {
		t.Fatal("源目录不存在时应报错")
	}
	if strings.Contains(err.Error(), "回滚失败") {
		t.Fatalf("从没让位过，不该说回滚失败: %v", err)
	}
}

// TestApplyPendingRuntimeUsesRealFileSystem 走真实文件系统跑一遍推迟后的替换。
func TestApplyPendingRuntimeUsesRealFileSystem(t *testing.T) {
	root := t.TempDir()
	layout := &Layout{Root: root}
	versionDir := layout.VersionDir("0.3.0")

	writeFile(t, filepath.Join(root, "runtime", "node", "node.exe"), "old")
	writeFile(t, filepath.Join(versionDir, "runtime", "node", "node.exe"), "new")

	applied, err := layout.ApplyPendingRuntime(versionDir)
	if err != nil {
		t.Fatalf("应用待换运行时失败: %v", err)
	}
	if !applied {
		t.Fatal("有 pending 时应报告已应用")
	}
	raw, err := os.ReadFile(filepath.Join(root, "runtime", "node", "node.exe"))
	if err != nil || string(raw) != "new" {
		t.Fatalf("runtime 未更新: %v %q", err, raw)
	}
	if _, err := os.Stat(filepath.Join(versionDir, "runtime")); err == nil {
		t.Fatal("pending 目录应已消失，否则版本目录长期占双份空间")
	}
	if _, err := os.Stat(root + string(filepath.Separator) + "runtime.old"); err == nil {
		t.Fatal("成功路径应清理 runtime.old")
	}

	// 第二次启动没有 pending：不报错、不动作。
	applied, err = layout.ApplyPendingRuntime(versionDir)
	if err != nil || applied {
		t.Fatalf("无 pending 时应返回 (false, nil)，实际 (%v, %v)", applied, err)
	}
}

// TestApplyPendingRuntimeSkipsIdenticalContent 保证重复启动不会白折腾一次改名。
func TestApplyPendingRuntimeSkipsIdenticalContent(t *testing.T) {
	root := t.TempDir()
	layout := &Layout{Root: root}
	versionDir := layout.VersionDir("0.3.0")
	writeFile(t, filepath.Join(root, "runtime", "node", "node.exe"), "same")
	writeFile(t, filepath.Join(versionDir, "runtime", "node", "node.exe"), "same")

	applied, err := layout.ApplyPendingRuntime(versionDir)
	if err != nil {
		t.Fatalf("失败: %v", err)
	}
	if applied {
		t.Fatal("内容一致时不该替换")
	}
	if _, err := os.Stat(filepath.Join(versionDir, "runtime")); err == nil {
		t.Fatal("内容一致应直接删掉 pending 副本")
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("建目录失败: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("写文件失败: %v", err)
	}
}
