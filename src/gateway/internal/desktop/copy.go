package desktop

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// copyTree 递归复制目录，用于跨卷安装（Rename 失败时）。
//
// 只复制普通文件与目录：更新包已经过 sha256 与签名校验，这里不再做内容判断，
// 但仍拒绝符号链接，避免包里夹带指向系统目录的链接。
func copyTree(source, target string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("更新包里不允许符号链接: %s", source)
	}
	if !info.IsDir() {
		return copyFile(source, target, info.Mode().Perm())
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(source)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := copyTree(filepath.Join(source, entry.Name()), filepath.Join(target, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

// copyFile 复制单个文件并保留权限位。
func copyFile(source, target string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
