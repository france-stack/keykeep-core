package desktop

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 日志轮转必须真的在写满时切文件。这条用例把上限调到 1 MB 以内反复写，
// 断言备份文件出现——只看「没有报错」不算通过：lumberjack 在轮转失败时
// 也只是静默地继续往同一个文件追加。
func TestOpenLogRotates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gateway.log")
	writer, err := openLogWithLimit(path, 1, 2)
	if err != nil {
		t.Fatalf("打开日志失败: %v", err)
	}
	defer writer.Close()

	chunk := strings.Repeat("x", 64<<10) + "\n"
	for i := 0; i < 40; i++ { // 约 2.6 MB，必然越过 1 MB 上限
		if _, err := writer.Write([]byte(chunk)); err != nil {
			t.Fatalf("第 %d 次写入失败: %v", i, err)
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读取日志目录失败: %v", err)
	}
	var name string
	var backups int
	for _, entry := range entries {
		name = entry.Name()
		if strings.HasPrefix(name, "gateway-") && strings.HasSuffix(name, ".log") {
			backups++
		}
	}
	if backups == 0 {
		t.Fatalf("写满上限后没有产生备份文件，目录内容: %v", entries)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("当前日志不存在: %v", err)
	}
	if info.Size() > 1<<20 {
		t.Fatalf("当前日志 %d 字节，超过 1 MB 上限", info.Size())
	}
}

// 目录不存在时必须自己建出来：首装时机器的 logs\ 还不存在。
func TestOpenLogCreatesDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "deeper", "control.log")
	writer, err := OpenLog(path)
	if err != nil {
		t.Fatalf("打开日志失败: %v", err)
	}
	defer writer.Close()
	if _, err := writer.Write([]byte("hello\n")); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	// lumberjack 到首次写入才落盘；断言的是「目录被建出来且文件可读」。
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("日志文件未创建: %v", err)
	}
	if string(raw) != "hello\n" {
		t.Fatalf("日志内容 = %q", raw)
	}
}
