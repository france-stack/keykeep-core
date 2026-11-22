package desktop

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// LinkSpec 描述一个需要重建的 node_modules 链接。
type LinkSpec struct {
	LinkPath string // 链接应在的位置
	Target   string // 真实目录（.pnpm 里的实体路径）
}

// NodeModulesLinks 扫描 control\node_modules\.pnpm，算出所有必须存在的顶层链接。
//
// 为什么要在目标机上重建：Next standalone 的 node_modules 顶层（next、pg、
// react…）以及 .next\node_modules 下的 externals，在 pnpm 布局里都是符号链接。
// zip 无法保存 Windows 目录联接（会退化成空目录），安装包解压后这些链接必然
// 丢失，控制面启动即 Cannot find module 'next' / '@swc/helpers'。
// 所以链接不在打包时固化，而是首启时按 .pnpm 的真实布局重建：
//
//	node_modules\<包名>        -> .pnpm\<entry>\node_modules\<包名>
//	.next\node_modules\<extern> -> 对应真实包
func NodeModulesLinks(controlDir string) ([]LinkSpec, error) {
	pnpmRoot := filepath.Join(controlDir, "node_modules", ".pnpm")
	entries, err := os.ReadDir(pnpmRoot)
	if err != nil {
		return nil, fmt.Errorf("读取 .pnpm 失败（安装包可能不完整）: %w", err)
	}

	// 包名 -> .pnpm 里的真实目录。
	index := map[string]string{}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == "node_modules" || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		inner := filepath.Join(pnpmRoot, entry.Name(), "node_modules")
		children, readErr := os.ReadDir(inner)
		if readErr != nil {
			continue
		}
		for _, child := range children {
			if strings.HasPrefix(child.Name(), "@") {
				// 作用域包：@scope/name
				scoped, scopeErr := os.ReadDir(filepath.Join(inner, child.Name()))
				if scopeErr != nil {
					continue
				}
				for _, pkg := range scoped {
					key := child.Name() + "/" + pkg.Name()
					if _, exists := index[key]; !exists {
						index[key] = filepath.Join(inner, child.Name(), pkg.Name())
					}
				}
				continue
			}
			if _, exists := index[child.Name()]; !exists {
				index[child.Name()] = filepath.Join(inner, child.Name())
			}
		}
	}

	specs := make([]LinkSpec, 0, len(index))
	modulesDir := filepath.Join(controlDir, "node_modules")
	for name, target := range index {
		specs = append(specs, LinkSpec{
			LinkPath: filepath.Join(modulesDir, filepath.FromSlash(name)),
			Target:   target,
		})
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].LinkPath < specs[j].LinkPath })
	return specs, nil
}

// EnsureNodeModulesLinks 让每个链接指向正确目录。
//
// 只在「链接缺失」或「指向空目录」时动手：实体目录（例如安装时已解开的包）
// 一律不碰，避免把真实文件删掉。返回新建的链接数。
func EnsureNodeModulesLinks(controlDir string) (int, error) {
	specs, err := NodeModulesLinks(controlDir)
	if err != nil {
		return 0, err
	}
	created := 0
	for _, spec := range specs {
		if _, statErr := os.Stat(spec.Target); statErr != nil {
			continue // 目标不存在，跳过（由运行时错误暴露）
		}
		// 目标目录必须真的可解析：空壳目录视为需要重建。
		entries, readErr := os.ReadDir(spec.Target)
		if readErr != nil || len(entries) == 0 {
			continue
		}
		if linkOK(spec.LinkPath, spec.Target) {
			continue
		}
		if err := replaceWithJunction(spec.LinkPath, spec.Target); err != nil {
			return created, err
		}
		created++
	}
	return created, nil
}

// linkOK 判断链接是否已正确指向目标。
func linkOK(linkPath, target string) bool {
	info, err := os.Lstat(linkPath)
	if err != nil {
		return false
	}
	if info.Mode()&os.ModeSymlink != 0 {
		resolved, err := os.Readlink(linkPath)
		if err != nil {
			return false
		}
		if !filepath.IsAbs(resolved) {
			resolved = filepath.Join(filepath.Dir(linkPath), resolved)
		}
		return filepath.Clean(resolved) == filepath.Clean(target)
	}
	// Windows 的目录联接不是 ModeSymlink，用「能否读到目标内容」判断。
	info, err = os.Stat(linkPath)
	if err != nil || !info.IsDir() {
		return false
	}
	entries, err := os.ReadDir(linkPath)
	return err == nil && len(entries) > 0
}

// replaceWithJunction 删除占位的空目录，改建成指向 target 的目录联接。
func replaceWithJunction(linkPath, target string) error {
	if info, err := os.Lstat(linkPath); err == nil {
		if info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
			// 只删空目录：有内容说明是真实包，不该覆盖。
			entries, readErr := os.ReadDir(linkPath)
			if readErr != nil {
				return readErr
			}
			if len(entries) > 0 {
				return nil
			}
		}
		if err := os.Remove(linkPath); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(linkPath), 0o755); err != nil {
		return err
	}
	return linkDir(linkPath, target)
}
