//go:build !windows

package desktop

// 非 Windows 平台没有命名互斥体；按「没有实例」处理，行为保持单机可用。
func TryHoldSingleInstance() (release func(), alreadyRunning bool, err error) {
	return func() {}, false, nil
}

func MutexExists() bool { return false }

func MessageBoxInfo(title, text string) {}
