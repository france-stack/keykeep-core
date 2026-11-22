// Package breaker 实现 KeyKeep 自有的上游凭据三态熔断状态机（FR-26 / FR-27）。
//
// 为什么不用 SDK 内置 cooling：内嵌 CLIProxyAPI 的 cooling 只有「冷却多久」，
// 没有可取证的三态迁移（闭合/打开/半开）、没有指数冷却序列、也不暴露每凭据
// 健康度。验收 FA-26 要求「三态迁移可取证 + 冷却时长按指数增长 + 恢复后自动
// 回闭合」，所以状态机放在网关层，SDK 的 cooling 仍然保留（两级互不依赖）。
//
// 接入方式是控制器在 Relay 重载时把「当前打开（open）的凭据 ID」剔除出
// 生成给 SDK 的配置——SDK 看不到该凭据就不会再选它，这就是 FR-27 的
// 「不盲目重试已判定故障的凭据」。半开探测用一个允许名单：冷却到期后
// 只放行凭据重新进入配置，若下一个观测窗口内再次失败则回到 open 并翻倍冷却。
//
// 观测源：SDK 的执行结果（auth.Result）→ 网关的 ResultPolicy 回调。
// 结果里的 AuthID 形如 "claude:apikey:<hash>"，由 SDK 按 api-key/base-url
// 等字段哈希生成；网关按下发配置时的同一组字段计算同一 ID（见 CredentialID），
// 两者必须一致，否则状态机永远观测不到失败——这条不变量有单测钉住。
package breaker

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// State 是三态熔断状态。
type State string

const (
	// StateClosed 闭合：正常放行，累计失败。
	StateClosed State = "closed"
	// StateOpen 打开：拒绝该凭据，冷却计时。
	StateOpen State = "open"
	// StateHalfOpen 半开：冷却到期，放行探测；再失败即回打开并翻倍冷却。
	StateHalfOpen State = "half_open"
)

// Transition 是一次状态迁移的取证记录（FA-26 证据来源：迁移日志）。
type Transition struct {
	At       time.Time `json:"at"`
	Key      string    `json:"key"`
	From     State     `json:"from"`
	To       State     `json:"to"`
	Reason   string    `json:"reason"`
	Failures int       `json:"failures"`
	Cooldown int       `json:"cooldownSeconds"`
}

// Options 是状态机的可配置参数。零值走 DefaultOptions。
type Options struct {
	// FailureThreshold 是连续失败多少次后打开。默认 3。
	FailureThreshold int
	// BaseCooldown 是首次打开的冷却时长。默认 5 秒。
	BaseCooldown time.Duration
	// MaxCooldown 是冷却上限。默认 5 分钟。
	MaxCooldown time.Duration
	// Now 是可注入时钟（测试用）。nil 时用 time.Now。
	Now func() time.Time
}

func (o Options) withDefaults() Options {
	if o.FailureThreshold <= 0 {
		o.FailureThreshold = 3
	}
	if o.BaseCooldown <= 0 {
		o.BaseCooldown = 5 * time.Second
	}
	if o.MaxCooldown <= 0 {
		o.MaxCooldown = 5 * time.Minute
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return o
}

// entry 是单个凭据的熔断状态。
type entry struct {
	state     State
	failures  int
	openedAt  time.Time
	cooldown  time.Duration
	successes int
}

// Machine 是并发安全的三态熔断状态机。
// 键是凭据 ID；nil Machine 的所有方法都安全（no-op / 放行），
// 便于无数据库形态与关闭熔断的场景不写分支。
type Machine struct {
	opts Options

	mu          sync.Mutex
	entries     map[string]*entry
	transitions []Transition
	// maxTransitions 限制内存中的迁移日志条数（取证用，不是审计账本）。
	maxTransitions int
}

// New 构造状态机。
func New(opts Options) *Machine {
	return &Machine{
		opts:           opts.withDefaults(),
		entries:        make(map[string]*entry),
		maxTransitions: 2000,
	}
}

// CredentialID 复现 SDK 对 claude/api-key 条目的 Auth ID 派生规则。
//
// SDK（internal/watcher/synthesizer）对 keys 里的每一项做：
// sha256("claude:apikey" + "\0"+api-key + ...)，取前 12 位十六进制，
// 形如 "claude:apikey:<12hex>"。网关必须用同一组字段、同一顺序计算，
// 否则 ResultPolicy 里收到的 AuthID 对不上任何配置项。
//
// 注意：SDK 的 idGen.Next 对重复键会追加 "-N" 后缀；凭据表里同一
// (name, base-url, api-key) 三元组重复出现的概率可忽略，这里不做后缀处理。
func CredentialID(apiKey, baseURL string) string {
	hasher := sha256.New()
	hasher.Write([]byte("claude:apikey"))
	for _, part := range []string{apiKey, baseURL} {
		hasher.Write([]byte{0})
		hasher.Write([]byte(strings.TrimSpace(part)))
	}
	digest := hex.EncodeToString(hasher.Sum(nil))
	return fmt.Sprintf("claude:apikey:%s", digest[:12])
}

// Allow 报告给定凭据当前是否可参与路由。
// 打开且冷却未到期 → false；冷却已到期 → 迁到 half_open 并放行（false 之后
// 由 Controller 在重载时把该凭据放回去，所以这里返回 true 表示「该探测了」）。
func (m *Machine) Allow(key string) bool {
	if m == nil || key == "" {
		return true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.entries[key]
	if e == nil || e.state == StateClosed {
		return true
	}
	if e.state == StateOpen {
		if m.opts.Now().Sub(e.openedAt) < e.cooldown {
			return false
		}
		m.transitionLocked(key, e, StateHalfOpen, "cooldown elapsed", e.failures, e.cooldown)
		return true
	}
	// half_open：允许探测。探测在途期间的并发请求也放行——
	// 探测窗口很短，多放几个不会把上游压垮；若确实全失败会立刻回 open。
	return true
}

// Open 报告凭据是否处于打开状态（被剔除出路由）。
func (m *Machine) Open(key string) bool {
	if m == nil || key == "" {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.entries[key]
	if e == nil {
		return false
	}
	if e.state == StateOpen && m.opts.Now().Sub(e.openedAt) >= e.cooldown {
		m.transitionLocked(key, e, StateHalfOpen, "cooldown elapsed", e.failures, e.cooldown)
		return false
	}
	return e.state == StateOpen
}

// Observe 记录一次执行结果。失败按连续计数，达到阈值即打开；成功立即回闭合。
func (m *Machine) Observe(key string, success bool) {
	if m == nil || key == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.entries[key]
	if e == nil {
		e = &entry{state: StateClosed}
		m.entries[key] = e
	}
	if success {
		if e.state != StateClosed {
			m.transitionLocked(key, e, StateClosed, "probe succeeded", 0, 0)
		}
		e.failures = 0
		e.cooldown = 0
		e.successes++
		return
	}

	e.failures++
	e.successes = 0
	switch e.state {
	case StateClosed:
		if e.failures >= m.opts.FailureThreshold {
			cd := m.opts.BaseCooldown
			m.transitionLocked(key, e, StateOpen, fmt.Sprintf("%d consecutive failures", e.failures), e.failures, cd)
		}
	case StateHalfOpen:
		// 半开探测失败：回打开，冷却翻倍（指数增长，封顶 MaxCooldown）。
		cd := e.cooldown * 2
		if cd < m.opts.BaseCooldown {
			cd = m.opts.BaseCooldown
		}
		if cd > m.opts.MaxCooldown {
			cd = m.opts.MaxCooldown
		}
		m.transitionLocked(key, e, StateOpen, "half-open probe failed; cooldown doubled", e.failures, cd)
	case StateOpen:
		// 打开期间的迟到失败（并发请求）不改变状态，也不重置冷却起点。
	}
}

// transitionLocked 落一条迁移记录并更新状态。调用方必须持锁。
func (m *Machine) transitionLocked(key string, e *entry, to State, reason string, failures int, cooldown time.Duration) {
	from := e.state
	now := m.opts.Now()
	tr := Transition{
		At: now, Key: key, From: from, To: to, Reason: reason, Failures: failures,
	}
	if to == StateOpen {
		e.openedAt = now
		e.cooldown = cooldown
		tr.Cooldown = int(cooldown / time.Second)
	} else if to == StateClosed {
		e.cooldown = 0
		e.failures = 0
	} else {
		tr.Cooldown = int(e.cooldown / time.Second)
	}
	e.state = to
	m.transitions = append(m.transitions, tr)
	if len(m.transitions) > m.maxTransitions {
		m.transitions = m.transitions[len(m.transitions)-m.maxTransitions:]
	}
}

// Transitions 返回迁移日志副本（旧→新）。
func (m *Machine) Transitions() []Transition {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Transition, len(m.transitions))
	copy(out, m.transitions)
	return out
}

// Snapshot 是单个凭据的只读健康视图（控制面展示用）。
type Snapshot struct {
	Key       string    `json:"key"`
	State     State     `json:"state"`
	Failures  int       `json:"failures"`
	Cooldown  int       `json:"cooldownSeconds"`
	OpenUntil time.Time `json:"openUntil,omitempty"`
}

// Snapshot 返回全部凭据当前的健康快照，按 key 排序。
func (m *Machine) Snapshot() []Snapshot {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Snapshot, 0, len(m.entries))
	for key, e := range m.entries {
		s := Snapshot{Key: key, State: e.state, Failures: e.failures, Cooldown: int(e.cooldown / time.Second)}
		if e.state == StateOpen {
			s.OpenUntil = e.openedAt.Add(e.cooldown)
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// FilterOpen 从凭据 ID 列表里剔除处于打开状态的项。
// ids 为下发顺序；返回保持原顺序的可用列表与被剔除的列表。
func (m *Machine) FilterOpen(ids []string) (usable, blocked []string) {
	if m == nil {
		return ids, nil
	}
	for _, id := range ids {
		if m.Open(id) {
			blocked = append(blocked, id)
			continue
		}
		usable = append(usable, id)
	}
	return usable, blocked
}

// Reset 清空全部状态（测试与运维手动恢复用）。
func (m *Machine) Reset() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = make(map[string]*entry)
	m.transitions = nil
}
