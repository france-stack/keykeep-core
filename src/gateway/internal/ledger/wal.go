// 账本写失败时的本地补偿队列（WAL）。
//
// HandleUsage 的失败此前只记日志：PG 抖动期间的全部 token 消耗
// 不入账、不结算预算、不补记限流窗口——对计量账本这是资金级丢失。
// 现在失败记录按 JSONL 追加到 WAL 文件，重放器周期性把队列里的记录
// 重新走完整入账管线；UsageRecord.rid 唯一约束保证重放幂等
// （已入账的 rid 写入会命中 ON CONFLICT DO NOTHING，等于成功）。
package ledger

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

// walRecord 是 WAL 文件里的一行。
type walRecord struct {
	Version    int          `json:"v"`
	EnqueuedAt int64        `json:"enqueuedAt"` // Unix 秒，用于观测积压时长
	Rec        usage.Record `json:"rec"`
}

const walVersion = 1

// wal 是追加式补偿队列；同进程内所有 HandleUsage 共享一把锁。
type wal struct {
	mu   sync.Mutex
	path string
}

// newWAL 打开（或创建）WAL 文件。KEYKEEP_LEDGER_WAL 可指定路径，
// 默认 <系统临时目录>/keykeep-ledger-wal.jsonl。
// 环境变量指定的路径必须通过校验（绝对路径、无 .. 段）——配置错误时
// 回退默认值并记日志，不把越界路径当文件名用。
func newWAL(path string) *wal {
	if path == "" {
		return &wal{path: filepath.Join(os.TempDir(), "keykeep-ledger-wal.jsonl")}
	}
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) || hasTraversalSegment(clean) {
		log.Printf("ledger: invalid KEYKEEP_LEDGER_WAL %q (need absolute path without ..), falling back to temp dir", path)
		return &wal{path: filepath.Join(os.TempDir(), "keykeep-ledger-wal.jsonl")}
	}
	return &wal{path: clean}
}

// hasTraversalSegment 判断路径里是否有独立的 .. 段（Clean 后仍保留即真越界意图）。
func hasTraversalSegment(path string) bool {
	for _, seg := range strings.Split(filepath.ToSlash(path), "/") {
		if seg == ".." {
			return true
		}
	}
	return false
}

// append 把失败记录落盘。任何错误只返回给调用方记日志，不阻塞上游响应。
func (w *wal) append(rec usage.Record) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	data, err := json.Marshal(walRecord{Version: walVersion, EnqueuedAt: time.Now().Unix(), Rec: rec})
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(w.path), 0o700); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	// 失败记录是唯一的资金补偿来源：压盘，断电也不能丢。
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync: %w", err)
	}
	return nil
}

// replay 把 WAL 里的记录逐条重放；成功的从文件中移除，失败的保留待下轮。
// process 即完整的入账管线（identity/price/write）；返回成功重放的条数。
// 文件不存在视为空队列（首次运行），返回 0。
func (w *wal) replay(process func(usage.Record) error) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	raw, err := os.ReadFile(w.path)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read: %w", err)
	}

	var remaining bytes.Buffer
	replayed := 0
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024) // usage.Record 带响应头快照，给足行宽
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var entry walRecord
		if err := json.Unmarshal(line, &entry); err != nil || entry.Rec.RequestID == "" && entry.Rec.APIKey == "" {
			// 损坏行无法定位唯一 rid：保留在文件里人工排查，不能静默丢。
			remaining.Write(line)
			remaining.WriteByte('\n')
			continue
		}
		if err := process(entry.Rec); err != nil {
			remaining.Write(line)
			remaining.WriteByte('\n')
			continue
		}
		replayed += 1
	}
	if err := scanner.Err(); err != nil {
		return replayed, fmt.Errorf("scan: %w", err)
	}
	if replayed == 0 {
		return 0, nil // 文件原样保留
	}
	if remaining.Len() == 0 {
		if err := os.Remove(w.path); err != nil {
			return replayed, err
		}
		return replayed, nil
	}
	tmp := w.path + ".replay.tmp"
	if err := os.WriteFile(tmp, remaining.Bytes(), 0o600); err != nil {
		return replayed, err
	}
	if err := os.Rename(tmp, w.path); err != nil {
		return replayed, err
	}
	return replayed, nil
}

// count 返回当前积压条数；仅用于观测与测试。
func (w *wal) count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	raw, err := os.ReadFile(w.path)
	if err != nil {
		return 0
	}
	n := 0
	for _, line := range bytes.Split(raw, []byte("\n")) {
		if len(bytes.TrimSpace(line)) > 0 {
			n += 1
		}
	}
	return n
}
