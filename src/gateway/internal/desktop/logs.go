// 桌面版日志的轮转策略：桌面版是「装一次跑几年」的形态，日志文件没有任何
// 上限时，磁盘被自己的日志写满只是时间问题——而磁盘满之后的表现是
// PostgreSQL 拒绝写入、账本写失败，症状离「日志太大」很远。
//
// 用 lumberjack 而不是自己 rename：Windows 上无法重命名一个被进程持有的
// 文件，而子进程的输出流是父进程转发的管道，只有在写入侧按大小切换文件
// 这一条路能同时满足「不重启进程」与「文件有上限」。
package desktop

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"gopkg.in/natefinch/lumberjack.v2"
)

// 单文件上限与保留份数。
//
// 10 MB × 3 份 ≈ 单进程日志最多占 40 MB；五个文件加起来 200 MB 以内，
// 在一台 256 GB 的办公机上可以忽略，但够放几个月的事件。
//
// 初始值取小而不是取大：日志的价值在最近一段，保留半年的 500 MB 日志
// 不如保留三个月的 30 MB；真需要长期留存应当走导出而不是留在本地。
const (
	defaultLogMaxSizeMB  = 10
	defaultLogMaxBackups = 3
)

// OpenLog 打开一个按大小轮转的日志文件，调用方负责 Close。
//
// 返回接口而不是 *lumberjack.Logger：调用方只需要「能写、能关」，
// 具体实现（直接写文件还是写轮转器）不该泄漏到进程管理逻辑里。
func OpenLog(path string) (io.WriteCloser, error) {
	return openLogWithLimit(path, defaultLogMaxSizeMB, defaultLogMaxBackups)
}

func openLogWithLimit(path string, maxSizeMB, maxBackups int) (io.WriteCloser, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("创建日志目录失败: %w", err)
	}
	return &lumberjack.Logger{
		Filename:   path,
		MaxSize:    maxSizeMB,
		MaxBackups: maxBackups,
		// 不按天清理：桌面版可能几周才开一次机，按天删会把唯一的现场删掉。
		MaxAge: 0,
		// 不压缩：现场排障时第一反应是打开备份日志，多一步解压就多一次放弃。
		Compress:  false,
		LocalTime: true,
	}, nil
}
