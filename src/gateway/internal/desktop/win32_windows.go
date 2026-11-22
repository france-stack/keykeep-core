//go:build windows

package desktop

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"unsafe"
)

var (
	advapi32            = syscall.NewLazyDLL("advapi32.dll")
	procRegCreateKeyExW = advapi32.NewProc("RegCreateKeyExW")
	procRegSetValueExW  = advapi32.NewProc("RegSetValueExW")
	procRegDeleteValueW = advapi32.NewProc("RegDeleteValueW")
	procRegCloseKey     = advapi32.NewProc("RegCloseKey")
	procShellExecuteW   = shell32.NewProc("ShellExecuteW")
)

const (
	hkeyCurrentUser = 0x80000001
	keySetValue     = 0x0002
	regSZ           = 1
	swShownormal    = 1
)

// autoStartKey 是开机自启的注册表位置（HKCU，不需要管理员权限）。
const autoStartKey = `Software\Microsoft\Windows\CurrentVersion\Run`
const autoStartValue = "KeyKeep"

func openRegKey() (syscall.Handle, error) {
	var handle syscall.Handle
	path, err := syscall.UTF16PtrFromString(autoStartKey)
	if err != nil {
		return 0, err
	}
	ret, _, callErr := procRegCreateKeyExW.Call(
		hkeyCurrentUser,
		uintptr(unsafe.Pointer(path)),
		0, 0, 0,
		keySetValue,
		0,
		uintptr(unsafe.Pointer(&handle)),
		0,
	)
	if ret != 0 {
		return 0, fmt.Errorf("打开注册表失败（code=%d）: %v", ret, callErr)
	}
	return handle, nil
}

// SetAutoStart 写入/删除 HKCU Run 项。target 是启动器可执行文件的绝对路径。
func SetAutoStart(enable bool, target string) error {
	handle, err := openRegKey()
	if err != nil {
		return err
	}
	defer procRegCloseKey.Call(uintptr(handle))

	name, err := syscall.UTF16PtrFromString(autoStartValue)
	if err != nil {
		return err
	}
	if !enable {
		procRegDeleteValueW.Call(uintptr(handle), uintptr(unsafe.Pointer(name)))
		return nil
	}

	// 带引号：路径里有空格时（C:\Users\张 三\...）不加引号会被拆成两段。
	quoted := `"` + target + `" --tray`
	value, err := syscall.UTF16FromString(quoted)
	if err != nil {
		return err
	}
	ret, _, callErr := procRegSetValueExW.Call(
		uintptr(handle),
		uintptr(unsafe.Pointer(name)),
		0,
		regSZ,
		uintptr(unsafe.Pointer(&value[0])),
		uintptr(len(value)*2),
	)
	if ret != 0 {
		return fmt.Errorf("写注册表失败（code=%d）: %v", ret, callErr)
	}
	return nil
}

// AutoStartEnabled 读回当前自启状态。
func AutoStartEnabled() bool {
	handle, err := openRegKey()
	if err != nil {
		return false
	}
	defer procRegCloseKey.Call(uintptr(handle))
	name, err := syscall.UTF16PtrFromString(autoStartValue)
	if err != nil {
		return false
	}
	// 只判断值是否存在：查询数据需要 RegQueryValueExW，这里没必要多声明一个函数。
	var data [2]uint16
	size := uint32(len(data) * 2)
	ret, _, _ := procRegQueryValueExW.Call(
		uintptr(handle),
		uintptr(unsafe.Pointer(name)),
		0, 0,
		uintptr(unsafe.Pointer(&data[0])),
		uintptr(unsafe.Pointer(&size)),
	)
	return ret == 0
}

// OpenBrowser 用系统默认浏览器打开 URL。
func OpenBrowser(url string) error {
	verb, err := syscall.UTF16PtrFromString("open")
	if err != nil {
		return err
	}
	target, err := syscall.UTF16PtrFromString(url)
	if err != nil {
		return err
	}
	ret, _, _ := procShellExecuteW.Call(
		0,
		uintptr(unsafe.Pointer(verb)),
		uintptr(unsafe.Pointer(target)),
		0, 0,
		swShownormal,
	)
	if ret <= 32 {
		return fmt.Errorf("打开浏览器失败（code=%d）", ret)
	}
	return nil
}

// OpenFolder 打开资源管理器到指定目录。
func OpenFolder(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return OpenBrowser(dir)
}

// LaunchDetached 启动一个与当前进程无关的守护进程（用于自更新后重启）。
func LaunchDetached(exe string, args ...string) error {
	abs, err := filepath.Abs(exe)
	if err != nil {
		return err
	}
	cmd := command(abs, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: 0x00000008, // DETACHED_PROCESS
	}
	return cmd.Start() // 不 Wait：父进程退出后子进程继续运行
}

// 说明：RegQueryValueExW 在这里显式声明，与上面的 Set/Delete 同属 advapi32。
var procRegQueryValueExW = advapi32.NewProc("RegQueryValueExW")

// 让 exec 包被引用（LaunchDetached 的类型依赖），避免各平台构建时未使用告警。
var _ = exec.Command
