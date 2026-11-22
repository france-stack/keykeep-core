//go:build windows

package desktop

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

var (
	kernel32Mod             = syscall.NewLazyDLL("kernel32.dll")
	procCreateSymbolicLinkW = kernel32Mod.NewProc("CreateSymbolicLinkW")
	procDeviceIoControl     = kernel32Mod.NewProc("DeviceIoControl")
	procCreateFileW         = kernel32Mod.NewProc("CreateFileW")
	procCloseHandle         = kernel32Mod.NewProc("CloseHandle")
)

const (
	// CreateSymbolicLinkW 的 dwFlags。
	symlinkFlagDirectory = 0x1
	// 允许非提权创建符号链接（需系统开启开发者模式）。
	symlinkFlagAllowUnprivilegedCreate = 0x2

	fsctlSetReparsePoint    = 0x000900A4
	maxReparseDataSize      = 16 * 1024
	genericWrite            = 0x40000000
	openExisting            = 3
	fileFlagBackupSemantics = 0x02000000
)

// reparseDataBuffer 是设置挂载点（目录联接）时传给 DeviceIoControl 的结构。
//
// 布局（Windows 文档 MOUNT_POINT_BUFFER）：
//
//	ULONG  ReparseTag
//	USHORT ReparseDataLength
//	USHORT Reserved
//	USHORT SubstituteNameOffset
//	USHORT SubstituteNameLength
//	USHORT PrintNameOffset
//	USHORT PrintNameLength
//	WCHAR  PathBuffer[]
type reparseDataBuffer struct {
	ReparseTag           uint32
	ReparseDataLength    uint16
	Reserved             uint16
	SubstituteNameOffset uint16
	SubstituteNameLength uint16
	PrintNameOffset      uint16
	PrintNameLength      uint16
	PathBuffer           [1]uint16
}

// linkDir 把 linkPath 建成指向 target 的目录链接。
//
// 优先走目录联接（junction）：不需要管理员权限、不要求开发者模式，
// node 的模块解析对它完全透明。失败才退化为符号链接。
func linkDir(linkPath, target string) error {
	absTarget, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	if _, err := os.Stat(absTarget); err != nil {
		return err
	}
	if err := createJunction(linkPath, absTarget); err == nil {
		return nil
	}
	return createSymlinkDir(linkPath, absTarget)
}

// createJunction 用 FSCTL_SET_REPARSE_POINT 写一个挂载点重解析数据。
//
// 全程 Win32 调用，不经过 shell：路径作为参数交给 CreateFileW，
// 不存在被解释成命令的路径。
func createJunction(linkPath, target string) error {
	if err := os.Mkdir(linkPath, 0o755); err != nil {
		return err
	}

	pathPtr, err := syscall.UTF16PtrFromString(linkPath)
	if err != nil {
		return err
	}
	handle, _, callErr := procCreateFileW.Call(
		uintptr(unsafe.Pointer(pathPtr)),
		genericWrite,
		0,
		0,
		openExisting,
		fileFlagBackupSemantics,
		0,
	)
	if handle == uintptr(syscall.InvalidHandle) {
		_ = os.Remove(linkPath)
		return fmt.Errorf("打开目录失败: %v", callErr)
	}
	defer procCloseHandle.Call(handle)

	// 目标是「\\?\C:\path」形式的替代名，这是挂载点格式的硬要求。
	substitute := `\??\` + absWinPath(target)
	printName := absWinPath(target)

	substituteUTF16, err := syscall.UTF16FromString(substitute)
	if err != nil {
		_ = os.Remove(linkPath)
		return err
	}
	printUTF16, err := syscall.UTF16FromString(printName)
	if err != nil {
		_ = os.Remove(linkPath)
		return err
	}

	substituteBytes := len(substituteUTF16) * 2 // 含结尾 NUL
	printBytes := len(printUTF16) * 2
	totalPathBytes := substituteBytes + printBytes
	// 结构体里 PathBuffer 已占 1 个 WCHAR（2 字节），总长度按实际需要算。
	bufferSize := int(unsafe.Offsetof(reparseDataBuffer{}.PathBuffer)) + totalPathBytes
	buffer := make([]byte, bufferSize)

	buf := (*reparseDataBuffer)(unsafe.Pointer(&buffer[0]))
	buf.ReparseTag = 0xA0000003 // IO_REPARSE_TAG_MOUNT_POINT
	buf.ReparseDataLength = uint16(8 + totalPathBytes)
	buf.SubstituteNameOffset = 0
	buf.SubstituteNameLength = uint16(substituteBytes - 2) // 不含 NUL
	buf.PrintNameOffset = uint16(substituteBytes)
	buf.PrintNameLength = uint16(printBytes - 2)

	pathStart := unsafe.Offsetof(reparseDataBuffer{}.PathBuffer)
	pathBuf := unsafe.Slice((*uint16)(unsafe.Pointer(&buffer[pathStart])), len(substituteUTF16)+len(printUTF16))
	copy(pathBuf, substituteUTF16[:len(substituteUTF16)-1])
	copy(pathBuf[len(substituteUTF16)-1:], printUTF16[:len(printUTF16)-1])

	var returned uint32
	ret, _, ioErr := procDeviceIoControl.Call(
		handle,
		fsctlSetReparsePoint,
		uintptr(unsafe.Pointer(&buffer[0])),
		uintptr(bufferSize),
		0, 0,
		uintptr(unsafe.Pointer(&returned)),
		0,
	)
	if ret == 0 {
		_ = os.Remove(linkPath)
		return fmt.Errorf("设置目录联接失败: %v", ioErr)
	}
	return nil
}

// createSymlinkDir 是退化路径：需要开发者模式或提权。
func createSymlinkDir(linkPath, target string) error {
	newPath, err := syscall.UTF16PtrFromString(linkPath)
	if err != nil {
		return err
	}
	existing, err := syscall.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	ret, _, callErr := procCreateSymbolicLinkW.Call(
		uintptr(unsafe.Pointer(newPath)),
		uintptr(unsafe.Pointer(existing)),
		symlinkFlagDirectory|symlinkFlagAllowUnprivilegedCreate,
	)
	if ret == 0 {
		return fmt.Errorf("创建目录链接失败（需要管理员权限或开发者模式）: %v", callErr)
	}
	return nil
}

// absWinPath 把 Go 路径转成 Windows 原生长路径形式（盘符 + 反斜杠）。
func absWinPath(path string) string {
	return filepath.Clean(path)
}
