//go:build windows

package main

import "syscall"

// createNoWindow = CREATE_NO_WINDOW：给控制台子系统子进程分配一个不可见的
// 控制台，而不是在桌面上新开一个黑色命令行窗口。HideWindow 负责隐藏继承
// 自外壳的窗口；两者一起用，双击 KeyKeep.exe 后桌面上只剩托盘。
const createNoWindow = 0x08000000

func hiddenProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}
