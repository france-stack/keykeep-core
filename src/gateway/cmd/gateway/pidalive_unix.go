//go:build !windows

package main

import (
	"errors"
	"os"
	"syscall"
)

// pidAlive 用 signal 0 探活：ESRCH 才是「进程不在了」，EPERM 是「在，但不归我管」，
// 后者必须按存活处理，否则会去删别人目录（虽然也删不动）。
func pidAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = p.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}
