// SDK 生成配置的落地位置。
//
// providers.Load 产出的 YAML 里是**解密后的真实上游 Key**，而内嵌 SDK 只接受
// 一个文件路径（它会读这个文件并监听变化），所以这份明文必须落盘。
// 这里的取舍是把它关进一个只有当前用户可进的私有目录，并保证进程退出、
// 甚至崩溃之后都不会留下能读到凭据的残留文件。
package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// sdkConfigDirPrefix 是临时目录名的前缀；目录名形如 keykeep-sdk-<pid>-<随机>。
// PID 进名字是为了让下一次启动能认出自己的祖先进程留下的残留并判断其死活。
const sdkConfigDirPrefix = "keykeep-sdk-"

// writeSDKConfig 把生成配置写进私有临时目录，返回路径与清理函数。
//
// MkdirTemp 建出的目录是 0700、文件 0600：非当前用户既进不了目录也读不到文件。
// 清理函数随 main 返回执行；进程被 SIGKILL 时不会执行，那部分由下次启动的
// sweepStaleSDKConfigs 兜底。
func writeSDKConfig(raw []byte) (string, func(), error) {
	dir, err := os.MkdirTemp("", fmt.Sprintf("%s%d-", sdkConfigDirPrefix, os.Getpid()))
	if err != nil {
		return "", nil, err
	}
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return "", nil, err
	}
	return path, func() { _ = os.RemoveAll(dir) }, nil
}

// sweepStaleSDKConfigs 删除已死进程遗留的配置目录。
//
// 只认「PID 已经不存在」这一种证据：PID 被复用会让残留多留一会儿，但反过来
// 按 mtime 过期删除会误伤一个跑了几天的在线进程——它监听的那个文件被删掉后
// 上游 Key 轮换就悄悄失效了。宁可留，不可删错。
func sweepStaleSDKConfigs() {
	temp := os.TempDir()
	matches, err := filepath.Glob(filepath.Join(temp, sdkConfigDirPrefix+"*"))
	if err != nil {
		return
	}
	removed := 0
	for _, dir := range matches {
		pid, ok := sdkConfigDirPID(filepath.Base(dir))
		if !ok || pid == os.Getpid() || pidAlive(pid) {
			continue
		}
		if err := os.RemoveAll(dir); err == nil {
			removed++
		}
	}
	if removed > 0 {
		log.Printf("removed %d stale sdk config dir(s) from dead gateway process(es)", removed)
	}
}

// sdkConfigDirPID 从目录名里取出 PID。
func sdkConfigDirPID(name string) (int, bool) {
	rest := strings.TrimPrefix(name, sdkConfigDirPrefix)
	if rest == name {
		return 0, false
	}
	raw, _, _ := strings.Cut(rest, "-")
	pid, err := strconv.Atoi(raw)
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}
