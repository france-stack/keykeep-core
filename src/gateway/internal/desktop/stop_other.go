//go:build !windows

package desktop

import (
	"strings"
	"time"
)

// StopSignal 在非 Windows 平台上是空实现：桌面运行时只面向 Windows。
type StopSignal struct{}

// PrepareStopSignal 返回空信号。
func PrepareStopSignal() (*StopSignal, error) { return &StopSignal{}, nil }

// Wait 直接返回（无托盘场景由调用方自己用信号控制）。
func (s *StopSignal) Wait() {}

// Reset 无副作用（与 Windows 实现同签名）。
func (s *StopSignal) Reset() {}

// Close 无副作用。
func (s *StopSignal) Close() {}

// RequestStop 在非 Windows 平台无法跨进程通知，返回 false。
func RequestStop() bool { return false }

// DesktopRunning 用控制台可达性判断实例是否在跑（与 Windows 实现同语义）。
func DesktopRunning(controlURL string) bool {
	if controlURL == "" {
		return false
	}
	return WaitHTTP(strings.TrimRight(controlURL, "/")+"/login", 700*time.Millisecond) == nil
}
