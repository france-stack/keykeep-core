//go:build windows

package shell

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// RegistryReader 用 golang.org/x/sys/windows 读注册表，不需要 cgo。
type RegistryReader struct{}

// ReadString 读一个 REG_SZ 值。
//
// 键不存在、值不存在都返回 found=false 且 err=nil：「没有 WebView2」是
// 正常结论，不是错误。只有真正的读取失败（权限、类型不符）才是 err。
func (RegistryReader) ReadString(root, path, name string) (string, bool, error) {
	rootKey, err := rootHandle(root)
	if err != nil {
		return "", false, err
	}
	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", false, err
	}
	var key windows.Handle
	if openErr := windows.RegOpenKeyEx(rootKey, pathPtr, 0, windows.KEY_READ, &key); openErr != nil {
		if isNotFound(openErr) {
			return "", false, nil
		}
		return "", false, openErr
	}
	defer func() { _ = windows.RegCloseKey(key) }()

	namePtr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return "", false, err
	}
	var valueType uint32
	var size uint32
	// 先问一次大小：pv 是版本号，实际长度很短，但按 API 用法先探长度
	// 可以让类型不符（REG_DWORD 之类）在读取之前就暴露出来。
	if queryErr := windows.RegQueryValueEx(key, namePtr, nil, &valueType, nil, &size); queryErr != nil {
		if isNotFound(queryErr) {
			return "", false, nil
		}
		return "", false, queryErr
	}
	if valueType != windows.REG_SZ && valueType != windows.REG_EXPAND_SZ {
		return "", false, fmt.Errorf("值类型不是字符串（type=%d）", valueType)
	}
	if size == 0 {
		return "", true, nil
	}
	buf := make([]byte, size+2)
	if queryErr := windows.RegQueryValueEx(key, namePtr, nil, &valueType, &buf[0], &size); queryErr != nil {
		return "", false, queryErr
	}
	return windows.UTF16ToString(bytesToUint16(buf)), true, nil
}

func rootHandle(name string) (windows.Handle, error) {
	switch name {
	case "HKLM", "HKEY_LOCAL_MACHINE":
		return windows.HKEY_LOCAL_MACHINE, nil
	case "HKCU", "HKEY_CURRENT_USER":
		return windows.HKEY_CURRENT_USER, nil
	}
	return 0, fmt.Errorf("不支持的注册表根：%s", name)
}

func isNotFound(err error) bool {
	return err == windows.ERROR_FILE_NOT_FOUND || err == windows.ERROR_PATH_NOT_FOUND
}

// bytesToUint16 把注册表返回的字节缓冲按 UTF-16LE 解释。
func bytesToUint16(buf []byte) []uint16 {
	out := make([]uint16, 0, len(buf)/2)
	for i := 0; i+1 < len(buf); i += 2 {
		unit := uint16(buf[i]) | uint16(buf[i+1])<<8
		if unit == 0 {
			break
		}
		out = append(out, unit)
	}
	return out
}
