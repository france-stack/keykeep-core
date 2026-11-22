package desktop

import (
	"archive/zip"
	"net"
	"os"
	"testing"
)

// writeZip 造一个 zip，用于测试解包与路径穿越校验。
func writeZip(t *testing.T, path string, files map[string]string) {
	t.Helper()
	handle, err := os.Create(path)
	if err != nil {
		t.Fatalf("创建 zip 失败: %v", err)
	}
	defer handle.Close()
	writer := zip.NewWriter(handle)
	for name, body := range files {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatalf("写 zip 条目失败: %v", err)
		}
		if _, err := entry.Write([]byte(body)); err != nil {
			t.Fatalf("写 zip 内容失败: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("关闭 zip 失败: %v", err)
	}
}

// occupyPort 占住一个空闲端口并返回监听器与端口号。
func occupyPort(t *testing.T) (net.Listener, int) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		listener.Close()
		t.Fatalf("地址类型不符: %T", listener.Addr())
	}
	return listener, addr.Port
}
