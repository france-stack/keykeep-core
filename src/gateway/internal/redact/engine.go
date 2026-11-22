// Package redact 实现 M6 脱敏：规则式打码、占位符映射与三种预设。
//
// 设计约束（dev-spec §4 / ADR-008）：
//   - 映射只存内存，请求结束即销毁；不落盘、不进账本、不进日志。
//   - 占位符全局唯一，形状 ⟦PII-0001⟧。
//   - 默认 presets=rules-only；block 需要本地检测器；pass-through 必须显式选择并审计。
package redact

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"regexp"
	"sort"
	"strings"
)

// Rule 是一条已编译规则。
type Rule struct {
	Name        string
	Pattern     string
	Kind        string // regex / literal
	Target      string // request / log / diagnostic / all
	Replacement string
	Priority    int
	Enabled     bool

	re *regexp.Regexp
}

// Compile 编译规则；非法正则返回错误，调用方决定跳过还是失败。
func (r Rule) Compile() (Rule, error) {
	if r.Kind == "literal" {
		r.re = regexp.MustCompile(regexp.QuoteMeta(r.Pattern))
		return r, nil
	}
	re, err := regexp.Compile(r.Pattern)
	if err != nil {
		return r, err
	}
	r.re = re
	return r, nil
}

// Engine 是按 priority 排序的规则集合。
type Engine struct {
	rules []Rule
}

// NewEngine 编译并排序规则；非法规则跳过并记日志（控制面 redact-rules
// 路由与 lib/regex-shape.ts 的注释都依赖这条留痕）。
func NewEngine(rules []Rule) *Engine {
	compiled := make([]Rule, 0, len(rules))
	for _, rule := range rules {
		if !rule.Enabled {
			continue
		}
		done, err := rule.Compile()
		if err != nil {
			// 只记规则名（来源标识）与截断的 pattern 前缀：%q 转义控制字符、
			// 按 rune 截断 80，避免正则全文里的换行/超长内容注入日志，
			// 也不会把多字节字符切成半个留下坏 UTF-8。
			prefix := rule.Pattern
			if r := []rune(prefix); len(r) > 80 {
				prefix = string(r[:80]) + "…"
			}
			log.Printf("redact: skip invalid rule name=%q kind=%s target=%s: %v (pattern prefix=%q)",
				rule.Name, rule.Kind, rule.Target, err, prefix)
			continue
		}
		if done.Target == "" {
			done.Target = "all"
		}
		compiled = append(compiled, done)
	}
	sort.SliceStable(compiled, func(i, j int) bool { return compiled[i].Priority < compiled[j].Priority })
	return &Engine{rules: compiled}
}

// Rules 返回已编译规则数量，供启动日志使用。
func (e *Engine) RuleCount() int {
	if e == nil {
		return 0
	}
	return len(e.rules)
}

// MaskText 对一段文本应用规则；replacement 为空时使用占位符。
func (e *Engine) MaskText(text string, m *Mapper) string {
	if e == nil || text == "" {
		return text
	}
	for _, rule := range e.rules {
		if rule.Target != "all" && rule.Target != "request" {
			continue
		}
		text = rule.re.ReplaceAllStringFunc(text, func(value string) string {
			if rule.Replacement != "" {
				return rule.Replacement
			}
			return m.Placeholder(value)
		})
	}
	return text
}

// Sanitize 用于错误体/日志等不可逆出口：固定替换为 [REDACTED]，不建立映射。
func (e *Engine) Sanitize(text string) string {
	if e == nil || text == "" {
		return text
	}
	for _, rule := range e.rules {
		text = rule.re.ReplaceAllString(text, "[REDACTED]")
	}
	return text
}

// MaskJSON 只改写 JSON 字符串值，保持结构与数字原样；非 JSON 返回错误。
func (e *Engine) MaskJSON(ctx context.Context, body []byte, m *Mapper, detector Detector) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return nil, err
	}
	masked, err := e.maskValue(ctx, value, m, detector)
	if err != nil {
		return nil, err
	}
	return json.Marshal(masked)
}

func (e *Engine) maskValue(ctx context.Context, value any, m *Mapper, detector Detector) (any, error) {
	switch v := value.(type) {
	case string:
		return e.maskString(ctx, v, m, detector)
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			masked, err := e.maskValue(ctx, item, m, detector)
			if err != nil {
				return nil, err
			}
			out[key] = masked
		}
		return out, nil
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			masked, err := e.maskValue(ctx, item, m, detector)
			if err != nil {
				return nil, err
			}
			out[i] = masked
		}
		return out, nil
	default:
		return value, nil
	}
}

func (e *Engine) maskString(ctx context.Context, text string, m *Mapper, detector Detector) (string, error) {
	if text == "" {
		return text, nil
	}
	if detector != nil {
		spans, err := detector.Detect(ctx, text)
		if err != nil {
			return "", err
		}
		text = applySpans(text, spans, m)
	}
	return e.MaskText(text, m), nil
}

// applySpans 从后往前替换，避免偏移失效。
func applySpans(text string, spans []Span, m *Mapper) string {
	valid := make([]Span, 0, len(spans))
	for _, span := range spans {
		if span.Start >= 0 && span.End <= len(text) && span.Start < span.End {
			valid = append(valid, span)
		}
	}
	sort.Slice(valid, func(i, j int) bool { return valid[i].Start > valid[j].Start })
	for _, span := range valid {
		text = text[:span.Start] + m.Placeholder(text[span.Start:span.End]) + text[span.End:]
	}
	return text
}

// Span 是模型式检测器返回的字符区间（字节偏移）。
type Span struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// Detector 是本地 PII 检测器接口；首版不捆绑模型，ADR-008 允许为空。
type Detector interface {
	Detect(ctx context.Context, text string) ([]Span, error)
}

// DefaultRules 是离线可用的内置规则。姓名/地址只提供高置信模式，
// 其余类别由管理员通过 RedactRule 增补，避免规则式误伤。
func DefaultRules() []Rule {
	return []Rule{
		{Name: "openai-key", Pattern: `\bsk-[A-Za-z0-9_-]{20,}\b`, Target: "request", Priority: 10, Enabled: true},
		{Name: "virtual-key", Pattern: `\bkk_[A-Za-z0-9_-]{20,}\b`, Target: "request", Priority: 11, Enabled: true},
		{Name: "aws-access-key", Pattern: `\bAKIA[0-9A-Z]{16}\b`, Target: "request", Priority: 12, Enabled: true},
		{Name: "private-key", Pattern: `-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----`, Target: "request", Priority: 13, Enabled: true},
		{Name: "bearer-token", Pattern: `(?i)\bbearer\s+[A-Za-z0-9._-]{16,}\b`, Target: "request", Priority: 14, Enabled: true},
		{Name: "email", Pattern: `\b[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}\b`, Target: "request", Priority: 40, Enabled: true},
		{Name: "cn-id", Pattern: `\b[1-9]\d{5}(19|20)\d{2}(0[1-9]|1[0-2])(0[1-9]|[12]\d|3[01])\d{3}[0-9Xx]\b`, Target: "request", Priority: 41, Enabled: true},
		{Name: "cn-phone", Pattern: `\b1[3-9]\d{9}\b`, Target: "request", Priority: 42, Enabled: true},
		{Name: "bank-card", Pattern: `\b\d{16,19}\b`, Target: "request", Priority: 43, Enabled: true},
		{Name: "ipv4", Pattern: `\b(?:\d{1,3}\.){3}\d{1,3}\b`, Target: "request", Priority: 44, Enabled: true},
		{Name: "internal-id", Pattern: `\bEMP-\d+\b`, Target: "request", Priority: 45, Enabled: true},
	}
}

// MaskedText 供测试与诊断包使用：不依赖 HTTP。
func MaskedText(engine *Engine, text string) string {
	return engine.MaskText(text, NewMapper())
}

// LooksJSON 判断请求体是否值得按 JSON 脱敏。
func LooksJSON(body []byte) bool {
	trimmed := bytes.TrimSpace(body)
	return len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[')
}

// IsPlaceholder 用于测试/诊断，判断字符串是否已被打码。
func IsPlaceholder(text string) bool {
	return strings.HasPrefix(text, "⟦PII-") && strings.HasSuffix(text, "⟧")
}
