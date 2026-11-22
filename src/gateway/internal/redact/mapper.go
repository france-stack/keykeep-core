package redact

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
)

const (
	maxMappings = 5000
	placeholder = "⟦PII-%04d⟧"
	limitToken  = "⟦PII-LIMIT⟧"
)

var placeholderRE = regexp.MustCompile(`⟦PII-\d+⟧`)

// Mapper 保存一次请求内的“真值 → 占位符”映射。
// 映射只在内存；请求结束由 GC 回收，不落盘、不进日志、不进账本。
type Mapper struct {
	mu      sync.Mutex
	byValue map[string]string
	byToken map[string]string
	next    int
	limited bool
}

func NewMapper() *Mapper {
	return &Mapper{byValue: make(map[string]string), byToken: make(map[string]string)}
}

// Placeholder 返回同一真值复用的占位符；超过上限后使用不可逆占位符。
func (m *Mapper) Placeholder(value string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if token, ok := m.byValue[value]; ok {
		return token
	}
	if m.next >= maxMappings {
		m.limited = true
		return limitToken
	}
	m.next++
	token := fmt.Sprintf(placeholder, m.next)
	m.byValue[value] = token
	m.byToken[token] = value
	return token
}

// Restore 把占位符还原为真值；不可逆占位符保持原样。
func (m *Mapper) Restore(text string) string {
	if text == "" {
		return text
	}
	return string(m.RestoreBytes([]byte(text)))
}

// RestoreBytes 是 Restore 的字节入口，供流式还原使用：占位符是纯 ASCII 数字
// 加两个固定码点，正则匹配不依赖 rune 边界，按字节切不会截坏 UTF-8。
// 无映射时原样返回，不产生额外拷贝。
func (m *Mapper) RestoreBytes(data []byte) []byte {
	if m == nil || len(data) == 0 {
		return data
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.byToken) == 0 {
		return data
	}
	return placeholderRE.ReplaceAllFunc(data, func(token []byte) []byte {
		if value, ok := m.byToken[string(token)]; ok {
			return []byte(value)
		}
		return token
	})
}

// Limited 表示本次请求的映射达到上限，部分内容不可逆。
func (m *Mapper) Limited() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.limited
}

// Count 返回已分配的占位符数量。
func (m *Mapper) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.next
}

// ContainsPlaceholder 仅供测试/诊断使用。
func ContainsPlaceholder(text string) bool {
	return strings.Contains(text, "⟦PII-")
}

type mapperKey struct{}

// WithMapper 把本次请求的映射放进上下文；响应还原与测试使用。
func WithMapper(ctx context.Context, m *Mapper) context.Context {
	return context.WithValue(ctx, mapperKey{}, m)
}

// MapperFrom 取出映射；没有则返回 nil。
func MapperFrom(ctx context.Context) *Mapper {
	m, _ := ctx.Value(mapperKey{}).(*Mapper)
	return m
}
