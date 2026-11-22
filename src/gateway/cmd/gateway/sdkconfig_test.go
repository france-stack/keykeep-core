package main

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestSDKConfigDirPID(t *testing.T) {
	tests := []struct {
		name string
		pid  int
		ok   bool
	}{
		{"keykeep-sdk-4242-123456", 4242, true},
		{"keykeep-sdk-7-", 7, true},
		{"keykeep-sdk-abc-1", 0, false},
		{"keykeep-sdk--1", 0, false},
		{"keykeep-sdk-0-1", 0, false},
		{"other-dir-1", 0, false},
	}
	for _, tc := range tests {
		pid, ok := sdkConfigDirPID(tc.name)
		if pid != tc.pid || ok != tc.ok {
			t.Errorf("%s -> (%d, %t), want (%d, %t)", tc.name, pid, ok, tc.pid, tc.ok)
		}
	}
}

func TestWriteSDKConfigIsPrivateAndCleanable(t *testing.T) {
	raw := []byte("key: secret-upstream-value\n")
	path, cleanup, err := writeSDKConfig(raw)
	if err != nil {
		t.Fatalf("writeSDKConfig: %v", err)
	}
	t.Cleanup(cleanup)

	dir := filepath.Dir(path)
	if filepath.Base(path) != "config.yaml" {
		t.Errorf("file name = %s, want config.yaml", filepath.Base(path))
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != string(raw) {
		t.Fatalf("read back: %q err=%v", got, err)
	}
	// Windows 上权限位不生效（可读性由 per-user temp 目录的 ACL 保证），只在 POSIX 断言。
	if os.PathSeparator == '/' {
		if info, err := os.Stat(dir); err != nil {
			t.Fatalf("stat dir: %v", err)
		} else if info.Mode().Perm() != 0o700 {
			t.Errorf("dir perm = %v, want 0700", info.Mode().Perm())
		}
		if info, err := os.Stat(path); err != nil {
			t.Fatalf("stat file: %v", err)
		} else if info.Mode().Perm() != 0o600 {
			t.Errorf("file perm = %v, want 0600", info.Mode().Perm())
		}
	}

	cleanup()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("dir should be gone after cleanup: %v", err)
	}
}

func TestSweepStaleSDKConfigs(t *testing.T) {
	// 4e9 远大于任何平台的 pid 上限，必然是死进程；自己的 PID 必然存活。
	dead := mkdirSDKDir(t, 4_000_000_000)
	mine := mkdirSDKDir(t, os.Getpid())

	sweepStaleSDKConfigs()

	if _, err := os.Stat(dead); !os.IsNotExist(err) {
		t.Errorf("stale dir of a dead pid should be removed: %v", err)
	}
	if _, err := os.Stat(mine); err != nil {
		t.Errorf("own pid dir must survive the sweep: %v", err)
	}
}

// mkdirSDKDir 按正式命名规则在系统临时目录里造一个配置目录，测试结束删除。
func mkdirSDKDir(t *testing.T, pid int) string {
	t.Helper()
	dir := filepath.Join(os.TempDir(), sdkConfigDirPrefix+strconv.Itoa(pid)+"-sweep")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}
