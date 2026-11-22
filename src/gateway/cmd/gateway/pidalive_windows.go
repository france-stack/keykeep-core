//go:build windows

package main

import "os"

// pidAlive 在 Windows 上靠打开进程句柄判断：Go 的 FindProcess 会真的调用
// OpenProcess，进程不存在时直接返回错误。Release 关掉句柄，避免泄漏。
func pidAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	_ = p.Release()
	return true
}
