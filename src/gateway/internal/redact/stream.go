package redact

import (
	"io"
	"strings"
)

// 占位符的字面形状是 ⟦PII-0001⟧（见 mapper.go）。流式还原要处理它被分包
// 切断、甚至被 ⟦/⟧ 自身的三个 UTF-8 字节切断的情况。
const (
	tokenOpen  = "⟦"
	tokenMid   = "PII-"
	tokenClose = "⟧"
	// tokenHead 是占位符中与编号无关的前缀部分。
	tokenHead = tokenOpen + tokenMid
	// maxTokenHoldback 是一个未完成占位符的最大字节数（完整长度减一），
	// 也就是流式还原最多往后压多少字节——延迟与内存的上界，与响应大小无关。
	maxTokenHoldback = len(tokenHead) + 4 + len(tokenClose) - 1
)

// isTokenPrefix 判断 t 是否是某个占位符的**严格**前缀（再喂字节就可能凑齐）。
// 完整占位符返回 false：它已由 Restore 处理，继续压住只会白等一个数据块。
func isTokenPrefix(t string) bool {
	if len(t) <= len(tokenHead) {
		return strings.HasPrefix(tokenHead, t)
	}
	t = t[len(tokenHead):]
	digits := 0
	for digits < len(t) && digits < 4 && t[digits] >= '0' && t[digits] <= '9' {
		digits++
	}
	if digits == len(t) {
		return true // 全是数字（≤4 位），还在等后续
	}
	if digits < 4 {
		return false // 数字区被非数字打断，永远凑不成占位符
	}
	rest := t[digits:]
	return len(rest) < len(tokenClose) && strings.HasPrefix(tokenClose, rest)
}

// tokenHoldback 返回 buf 尾部仍可能是占位符前缀的字节数；0 表示可整段放行。
// 只扫描最后 maxTokenHoldback 字节：更长的前缀不存在，完整 token 不在考虑之列。
func tokenHoldback(buf []byte) int {
	start := len(buf) - maxTokenHoldback
	if start < 0 {
		start = 0
	}
	for i := start; i < len(buf); i++ {
		if buf[i] != tokenOpen[0] {
			continue
		}
		if isTokenPrefix(string(buf[i:])) {
			return len(buf) - i
		}
	}
	return 0
}

// streamRestorer 在不缓冲整个响应的前提下把占位符还原为真值。
//
// 设计约束：
//   - 内存有界：任何时刻最多保留 maxTokenHoldback 个未判定字节；
//   - 延迟有界：只有"可能是占位符前缀"的尾部会被推迟到下一个数据块，其余
//     字节读一段吐一段，SSE 的逐事件到达语义不变；
//   - 只还原、不打码：不可逆打码（Sanitize）依赖完整上下文，无法增量做。
//     能走到这里的请求体早已替换过占位符或被拒，上游无原始秘密可回显。
type streamRestorer struct {
	src    io.ReadCloser
	mapper *Mapper
	held   []byte
	out    []byte
	chunk  []byte
	srcErr error
}

func newStreamRestorer(src io.ReadCloser, mapper *Mapper) *streamRestorer {
	return &streamRestorer{src: src, mapper: mapper, chunk: make([]byte, 16*1024)}
}

func (s *streamRestorer) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for len(s.out) == 0 {
		if s.srcErr != nil {
			if len(s.held) == 0 {
				return 0, s.srcErr
			}
			// 上游结束也要把压住的尾巴吐干净，否则结尾占位符永久丢失。
			s.out = s.mapper.RestoreBytes(s.held)
			s.held = nil
			continue
		}
		n, err := s.src.Read(s.chunk)
		if err != nil {
			s.srcErr = err
		}
		if n == 0 {
			continue
		}
		combined := append(s.held, s.chunk[:n]...)
		cut := len(combined) - tokenHoldback(combined)
		s.held = append([]byte(nil), combined[cut:]...)
		if out := s.mapper.RestoreBytes(combined[:cut]); len(out) > 0 {
			s.out = out
		}
	}
	n := copy(p, s.out)
	s.out = s.out[n:]
	return n, nil
}

func (s *streamRestorer) Close() error {
	return s.src.Close()
}
