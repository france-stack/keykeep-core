//go:build !windows

package main

import "syscall"

// 非 Windows 平台没有控制台窗口问题；保持行为一致（不附加任何属性）。
func hiddenProcAttr() *syscall.SysProcAttr { return nil }
