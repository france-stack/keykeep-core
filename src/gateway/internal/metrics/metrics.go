// Package metrics 暴露网关的 Prometheus 指标。
//
// 为什么现在才加：评审把「无 metrics / 无告警 / 无追踪」列为架构短板，而排障
// 现状是靠用户报障 + 人工翻日志。这个包里只有一件事——把进程里**已经存在**
// 的计数导出来（过载闸门的排队深度、限流拒绝、账本 WAL 待重放、审计写入失败），
// 不新增统计逻辑，避免指标与真实行为各说一套。
//
// 口径约定：
//   - 只导出聚合值，标签基数受控（status / reason 是有限枚举）；
//   - 不导出 Key / 员工 / 路径这类高基数或可识别信息；
//   - 指标端点默认只在本机回环可读（由 main 的路由决定），与 /healthz 同级。

package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/keykeep/gateway/internal/overload"
)

// Registry 是网关自己的指标注册表；不挂在 prometheus.DefaultRegisterer 上，
// 免得第三方库（SDK）顺手注册的东西混进我们的口径。
type Registry struct {
	registry *prometheus.Registry

	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec

	rateLimitDecisions *prometheus.CounterVec
	auditWrites        *prometheus.CounterVec
	ledgerWrites       *prometheus.CounterVec

	ready *prometheus.GaugeVec

	// bound 标记依赖数据库的指标是否已经接进来；重复绑定会 panic。
	bound bool
}

// Options 是 BindDatabase 需要的运行时依赖；每个都可为 nil（对应指标就不注册）。
type Options struct {
	// Gate 提供并发槽 / 排队数据；nil 时相关指标恒为 0。
	Gate *overload.Gate
	// Ready 返回网关依赖（数据库 / SDK）当前是否可用，1 = 可用。
	Ready func() bool
	// RateLimitFailClosed 返回自启动以来因 fail-closed 拒绝的请求数。
	RateLimitFailClosed func() uint64
}

// New 构造注册表并注册与运行时依赖无关的计数（转发量、限流判定、审计与账本写入）。
//
// 依赖运行时对象的指标（并发槽、就绪、fail-closed 计数）留到 BindDatabase：
// 那时代理链与数据库才建好。反过来做——在 New 里先注册一个闭包到 nil 的闸门——
// 会让这些指标永远停在 0，而端点看起来一切正常，比没有指标更坏。
func New() *Registry {
	r := &Registry{registry: prometheus.NewRegistry()}

	r.requests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "keykeep_gateway_requests_total",
		Help: "网关处理的 HTTP 请求数（按方法、状态码、拒绝原因分类）。",
	}, []string{"method", "status", "reason"})
	r.duration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "keykeep_gateway_request_duration_seconds",
		Help:    "转发请求耗时（秒）。",
		Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60},
	}, []string{"status"})
	r.rateLimitDecisions = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "keykeep_gateway_ratelimit_decisions_total",
		Help: "限流判定次数（allow / deny / unavailable）。",
	}, []string{"tier", "outcome"})
	r.auditWrites = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "keykeep_gateway_audit_writes_total",
		Help: "审计事件写入次数（ok / error）。",
	}, []string{"outcome"})
	r.ledgerWrites = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "keykeep_gateway_ledger_writes_total",
		Help: "账本行写入次数（ok / error / replayed）。",
	}, []string{"outcome"})

	r.registry.MustRegister(r.requests, r.duration, r.rateLimitDecisions, r.auditWrites, r.ledgerWrites)
	return r
}

func boolToFloat(ok bool) float64 {
	if ok {
		return 1
	}
	return 0
}

// Handler 返回 /metrics 的处理器。
func (r *Registry) Handler() http.Handler {
	return promhttp.HandlerFor(r.registry, promhttp.HandlerOpts{})
}

// ObserveRequest 记录一次请求结果（供访问日志中间件调用）。
// reason 是有限枚举（rate_limited / unavailable / …），标签基数受控。
func (r *Registry) ObserveRequest(method string, status int, reason string, seconds float64) {
	code := itoa(status)
	r.requests.WithLabelValues(method, code, reason).Inc()
	r.duration.WithLabelValues(code).Observe(seconds)
}

// ObserveRateLimit 记录一次限流判定。
func (r *Registry) ObserveRateLimit(tier, outcome string) {
	r.rateLimitDecisions.WithLabelValues(tier, outcome).Inc()
}

// ObserveAuditWrite 记录一次审计写入。
func (r *Registry) ObserveAuditWrite(ok bool) {
	if ok {
		r.auditWrites.WithLabelValues("ok").Inc()
		return
	}
	r.auditWrites.WithLabelValues("error").Inc()
}

// ObserveLedgerWrite 记录一次账本写入。
func (r *Registry) ObserveLedgerWrite(outcome string) {
	r.ledgerWrites.WithLabelValues(outcome).Inc()
}

// SetReady 更新依赖就绪状态。
func (r *Registry) SetReady(ok bool) {
	if r.ready == nil {
		return
	}
	r.ready.WithLabelValues("database").Set(boolToFloat(ok))
}

// itoa 是小整数到字符串的无分配实现（状态码 100..599）。
func itoa(value int) string {
	const digits = "0123456789"
	if value < 10 {
		return digits[value : value+1]
	}
	var buf [3]byte
	i := len(buf)
	for value > 0 {
		i--
		buf[i] = digits[value%10]
		value /= 10
	}
	return string(buf[i:])
}

// 编译期断言：这些类型确实实现了 prometheus.Collector，防止签名漂移。
var _ prometheus.Collector = (prometheus.GaugeFunc)(nil)

// BindDatabase 在数据库与过载闸门就绪后挂上其余指标。
//
// 分成两步是因为 /metrics 端点必须在"还没连库"时也成立——否则本地联调或
// 仅跑 SDK 的部署形态里监控会凭空消失，而那种形态恰恰最容易出问题。
// 重复调用是空操作而非 panic：两种形态共用同一个注册表。
func (r *Registry) BindDatabase(opts Options) {
	if r.bound {
		return
	}
	r.bound = true

	gateSnapshot := func() overload.Stats {
		if opts.Gate == nil {
			return overload.Stats{}
		}
		return opts.Gate.Snapshot()
	}
	r.registry.MustRegister(
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "keykeep_gateway_inflight",
			Help: "当前占用全局并发槽的请求数。",
		}, func() float64 { return float64(gateSnapshot().Inflight) }),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "keykeep_gateway_queue_depth",
			Help: "当前在过载保护队列中等待的请求数。",
		}, func() float64 { return float64(gateSnapshot().Queued) }),
		prometheus.NewCounterFunc(prometheus.CounterOpts{
			Name: "keykeep_gateway_admitted_total",
			Help: "累计获得并发槽的请求数。",
		}, func() float64 { return float64(gateSnapshot().Admitted) }),
		prometheus.NewCounterFunc(prometheus.CounterOpts{
			Name: "keykeep_gateway_queue_full_total",
			Help: "累计因队列满被拒绝的请求数。",
		}, func() float64 { return float64(gateSnapshot().QueueFull) }),
		prometheus.NewCounterFunc(prometheus.CounterOpts{
			Name: "keykeep_gateway_queue_timeout_total",
			Help: "累计排队超时被拒绝的请求数。",
		}, func() float64 { return float64(gateSnapshot().QueueTimeout) }),
	)

	if opts.RateLimitFailClosed != nil {
		r.registry.MustRegister(prometheus.NewCounterFunc(prometheus.CounterOpts{
			Name: "keykeep_gateway_ratelimit_fail_closed_total",
			Help: "累计因限额后端不可用而按 fail-closed 拒绝的请求数。",
		}, func() float64 { return float64(opts.RateLimitFailClosed()) }))
	}
	if opts.Ready != nil {
		r.ready = prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "keykeep_gateway_ready",
			Help: "网关依赖是否就绪（1 = 就绪）。",
		}, []string{"component"})
		r.ready.WithLabelValues("database").Set(boolToFloat(opts.Ready()))
		r.registry.MustRegister(r.ready)
	}
}
