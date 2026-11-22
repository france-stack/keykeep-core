//go:build windows

package shell

import (
	"fmt"
	"runtime"

	"golang.org/x/sys/windows"
)

// HardenProfileACL 把 WebView2 用户数据目录的 DACL 收紧成「仅当前用户完全控制」。
//
// 为什么不是 os.Chmod：Windows 上 Go 的 0o700 只映射到只读属性，
// 对 ACL 没有影响；而 WebView2 profile 里有会话 cookie，
// 「同机其它账号不可读」必须真的写进 DACL 才算数。
//
// 失败不阻断启动（调用方只记日志）：企业策略可能禁止改 DACL，
// 那时把窗口关掉比目录权限松一点更糟。但失败一定留痕。
func HardenProfileACL(dir string) error {
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err != nil {
		return fmt.Errorf("读取当前用户 SID 失败: %w", err)
	}

	// TrusteeValue 里存着 SID 指针，必须 pin 住，否则 GC 移动后 ACL 会指向野内存。
	var pinner runtime.Pinner
	defer pinner.Unpin()
	pinner.Pin(user.User.Sid)

	entries := []windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.GENERIC_ALL,
		AccessMode:        windows.SET_ACCESS,
		Inheritance:       windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_USER,
			TrusteeValue: windows.TrusteeValueFromSID(user.User.Sid),
		},
	}}
	acl, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		return fmt.Errorf("构造 DACL 失败: %w", err)
	}

	pathPtr, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return err
	}
	// 目录需要 FILE_FLAG_BACKUP_SEMANTICS 才能用 CreateFile 打开并改安全描述符。
	handle, err := windows.CreateFile(
		pathPtr,
		windows.WRITE_DAC|windows.READ_CONTROL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
	if err != nil {
		return fmt.Errorf("打开目录失败: %w", err)
	}
	defer func() { _ = windows.CloseHandle(handle) }()

	// PROTECTED_DACL：切断继承，否则父目录上的 Users 读取权限会被继承回来，
	// 收紧就成了一句空话。
	if err := windows.SetSecurityInfo(
		handle,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, acl, nil,
	); err != nil {
		return fmt.Errorf("写入 DACL 失败: %w", err)
	}
	return nil
}
