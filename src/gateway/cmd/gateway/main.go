// KeyKeep 数据面网关：在进程内嵌入 CLIProxyAPI SDK（路线 A），
// 外层只做监听分离与健康检查，转发逻辑由 SDK 负责。
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"

	"github.com/keykeep/gateway/internal/accesslog"
	"github.com/keykeep/gateway/internal/audit"
	"github.com/keykeep/gateway/internal/breaker"
	"github.com/keykeep/gateway/internal/budget"
	"github.com/keykeep/gateway/internal/dbpool"
	"github.com/keykeep/gateway/internal/idempotency"
	"github.com/keykeep/gateway/internal/ledger"
	"github.com/keykeep/gateway/internal/metrics"
	"github.com/keykeep/gateway/internal/overload"
	"github.com/keykeep/gateway/internal/policy"
	"github.com/keykeep/gateway/internal/providers"
	"github.com/keykeep/gateway/internal/ratelimit"
	"github.com/keykeep/gateway/internal/redact"
	"github.com/keykeep/gateway/internal/reqbody"
	"github.com/keykeep/gateway/internal/selfservice"
	"github.com/keykeep/gateway/internal/server"
	"github.com/keykeep/gateway/internal/version"
	"github.com/keykeep/gateway/internal/virtualkey"
)

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// parseDuration 解析时长配置；非法值回退默认并记一条日志。
func parseDuration(raw string, fallback time.Duration) time.Duration {
	value, err := time.ParseDuration(raw)
	if err != nil || value < 0 {
		log.Printf("invalid duration %q, using %s", raw, fallback)
		return fallback
	}
	return value
}

// envInt 读非负整数环境变量；缺失或非法时返回 fallback。
func envInt(key string, fallback int) int {
	v, err := strconv.Atoi(os.Getenv(key))
	if err != nil || v < 0 {
		return fallback
	}
	return v
}

func splitAddr(addr string) (string, int, error) {
	host, portText, err := net.SplitHostPort(addr)
	if err != nil {
		return "", 0, err
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return "", 0, fmt.Errorf("invalid port %q: %w", portText, err)
	}
	return host, port, nil
}

// runHealthcheck 探一次本机 /healthz，返回进程退出码（0 健康，1 不健康）。
//
// 探的地址取自 KEYKEEP_GATEWAY_LISTEN：监听在 0.0.0.0 时改探 127.0.0.1，
// 容器里不能往 "0.0.0.0" 发请求。
func runHealthcheck() int {
	listen := env("KEYKEEP_GATEWAY_LISTEN", ":8080")
	host, port, err := splitAddr(listen)
	if err != nil {
		log.Printf("healthcheck: 解析监听地址失败: %v", err)
		return 1
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + net.JoinHostPort(host, strconv.Itoa(port)) + "/healthz")
	if err != nil {
		log.Printf("healthcheck: %v", err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Printf("healthcheck: status %d", resp.StatusCode)
		return 1
	}
	return 0
}

func main() {
	// --healthcheck 供容器健康检查使用：镜像是 distroless，没有 wget/curl，
	// 与其为一条探针换回带 shell 的底座（等于把攻击面加倍），不如让程序自己探。
	if len(os.Args) > 1 && os.Args[1] == "--healthcheck" {
		os.Exit(runHealthcheck())
	}
	cfgPath := env("KEYKEEP_SDK_CONFIG", "config.yaml")
	baseCfg, err := config.LoadConfig(cfgPath)
	if err != nil {
		log.Fatalf("load sdk config %s: %v", cfgPath, err)
	}
	cfg := baseCfg
	sdkConfigPath := cfgPath
	dbURL := os.Getenv("KEYKEEP_DATABASE_URL")
	keyPrefix := env("KEYKEEP_INTERNAL_KEY_PREFIX", "ik_")
	// 上游熔断三态（FR-26/27）：观测 SDK 执行结果，打开时把凭据剔除出下发配置。
	// KEYKEEP_BREAKER=off 可关闭（默认开；无数据库形态也保留，只是没有可观测的凭据）。
	var breakerMachine *breaker.Machine
	if env("KEYKEEP_BREAKER", "on") != "off" {
		breakerMachine = breaker.New(breaker.Options{
			FailureThreshold: envInt("KEYKEEP_BREAKER_FAILURES", 3),
			BaseCooldown:     time.Duration(envInt("KEYKEEP_BREAKER_BASE_COOLDOWN_SECONDS", 5)) * time.Second,
			MaxCooldown:      time.Duration(envInt("KEYKEEP_BREAKER_MAX_COOLDOWN_SECONDS", 300)) * time.Second,
		})
	}
	skipOpen := func(machine *breaker.Machine) func(string) bool {
		if machine == nil {
			return nil
		}
		return machine.Open
	}
	if dbURL != "" {
		kek := os.Getenv("KEYKEEP_KEK")
		if kek == "" {
			log.Fatalf("KEYKEEP_KEK is required when KEYKEEP_DATABASE_URL is set")
		}
		loadCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		var raw []byte
		cfg, raw, err = providers.Load(loadCtx, dbURL, kek, baseCfg, cfgPath, keyPrefix, providers.OverridesFromEnv(), skipOpen(breakerMachine))
		cancel()
		if err != nil {
			log.Fatalf("load providers from postgres: %v", err)
		}
		// 生成的配置含解密后的真实上游凭据，写进私有临时目录（0700 目录 + 0600 文件）：
		// SDK 的文件 watcher 监听它而不是基础配置，避免有人编辑 config.yaml 时
		// 覆盖运行时 access keys。日志里不出现路径——这个文件是凭据，路径进日志
		// 就等于把「去哪里找明文密钥」写进了别人也能看的日志文件/工单截图。
		sweepStaleSDKConfigs()
		path, cleanup, err := writeSDKConfig(raw)
		if err != nil {
			log.Fatalf("write generated sdk config: %v", err)
		}
		defer cleanup()
		sdkConfigPath = path
		log.Printf("provider config loaded from postgres (generated private sdk config)")
	}

	// SDK 只监听回环内部端口；对外端口由本进程的反向代理持有。
	sdkAddr := env("KEYKEEP_SDK_ADDR", "127.0.0.1:18080")
	host, port, err := splitAddr(sdkAddr)
	if err != nil {
		log.Fatalf("invalid KEYKEEP_SDK_ADDR: %v", err)
	}
	cfg.Host = host
	cfg.Port = port

	builder := cliproxy.NewBuilder().WithConfig(cfg).WithConfigPath(sdkConfigPath)
	if policy := breaker.NewPolicy(breakerMachine); policy != nil {
		builder = builder.WithResultPolicy(policy)
	}
	svc, err := builder.Build()
	if err != nil {
		log.Fatalf("build embedded sdk: %v", err)
	}

	// 用量账本：SDK 每次上游调用后回调，这里落哈希链账本并结算预算。
	var usagePlugin *ledger.Plugin
	if dbURL != "" {
		setupCtx, setupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		usagePlugin, err = ledger.NewPlugin(setupCtx, dbURL, keyPrefix)
		setupCancel()
		if err != nil {
			log.Fatalf("init usage ledger: %v", err)
		}
		defer usagePlugin.Close()
		// 未定价模型的保底单价：账本结算与预算预扣共用同一口径。
		unpricedFloor := ledger.UnpricedFloorFromEnv()
		usagePlugin.SetUnpricedFloorPerM(unpricedFloor)
		svc.RegisterUsagePlugin(usagePlugin)
		log.Printf("usage ledger enabled (hash chain + daily aggregate, unpriced floor=%d)", unpricedFloor)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 账本 WAL 重放：启动即补偿上次退出前的积压，之后每 30 秒一轮。
	if usagePlugin != nil {
		usagePlugin.StartReplayer(ctx)
	}

	go func() {
		if err := svc.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("embedded sdk stopped: %v", err)
		}
	}()

	if !server.WaitPort(sdkAddr, 15*time.Second) {
		log.Fatalf("embedded sdk did not open %s", sdkAddr)
	}

	// 控制面改库后（停用凭据、改映射、签发/撤销 Key）由 watcher 重写配置并触发热重载。
	// Relay 还允许虚拟键中间件对「刚签发、尚未进 SDK 访问列表」的键主动触发重载并等待，
	// 否则这几笔调用的账本归属会静默丢失（E2E 复跑实测）。
	var virtualKeyEnsure func(ctx context.Context, keyID string) bool
	if dbURL != "" {
		interval := parseDuration(env("KEYKEEP_PROVIDER_WATCH_INTERVAL", "2s"), 2*time.Second)
		ensureWait := parseDuration(env("KEYKEEP_ENSURE_KEY_WAIT", "3s"), 3*time.Second)
		relay := providers.NewRelay(func(ctx context.Context) ([]string, error) {
			cfg, raw, err := providers.Load(ctx, dbURL, os.Getenv("KEYKEEP_KEK"), baseCfg, cfgPath, keyPrefix, providers.OverridesFromEnv(), skipOpen(breakerMachine))
			if err != nil {
				return nil, err
			}
			if err := providers.WriteConfig(sdkConfigPath, raw); err != nil {
				return nil, err
			}
			return cfg.APIKeys, nil
		}, interval)
		// 启动 Load 的结果就是初始访问列表，直接播种，省掉首个请求的重载。
		relay.SeedAccessKeys(cfg.APIKeys)
		go relay.Run(ctx)
		virtualKeyEnsure = func(ctx context.Context, keyID string) bool {
			return relay.EnsureKey(ctx, keyID, ensureWait)
		}
		log.Printf("provider watch enabled (interval=%s, ensure-key wait=%s)", interval, ensureWait)
	}

	target, err := url.Parse("http://" + sdkAddr)
	if err != nil {
		log.Fatalf("parse sdk url: %v", err)
	}
	handler := accesslog.Middleware(server.NewHandler(target, version.String()))
	// 指标在有数据库与无数据库两种形态下都要在：前者能导出全部计数，
	// 后者至少能看出「这个进程没连数据库」。放在分支外，别让它随配置消失。
	var (
		metricsRegistry *metrics.Registry
		metricsHandler  http.Handler
	)
	if env("KEYKEEP_METRICS", "on") != "off" {
		metricsRegistry = metrics.New()
		metricsHandler = metricsRegistry.Handler()
		accesslog.SetMetrics(metricsRegistry)
	}
	var overloadGate *overload.Gate
	if dbURL != "" {
		store, err := virtualkey.NewPostgresStore(context.Background(), dbURL)
		if err != nil {
			log.Fatalf("init virtual key store: %v", err)
		}
		defer store.Close()
		// 顺序：虚拟 Key 校验 → 模型白名单 → RPM/TPM 限流 → 两级预算（员工/全局）。
		auditor, err := audit.NewWriter(context.Background(), dbURL)
		if err != nil {
			log.Fatalf("init audit writer: %v", err)
		}
		defer auditor.Close()
		gate, err := budget.NewGate(context.Background(), dbURL)
		if err != nil {
			log.Fatalf("init budget gate: %v", err)
		}
		defer gate.Close()
		gate.UnpricedFloorPerM = ledger.UnpricedFloorFromEnv()
		limitStore, err := ratelimit.NewStore(context.Background(), dbURL)
		if err != nil {
			log.Fatalf("init rate limit store: %v", err)
		}
		defer limitStore.Close()

		// 计数后端：默认 PG（跨实例一致）；KEYKEEP_RATE_STORE=memory 回退单进程。
		var counter ratelimit.Counter = ratelimit.NewLimiter()
		if env("KEYKEEP_RATE_STORE", "postgres") != "memory" {
			pgCounter, err := ratelimit.NewPGCounters(context.Background(), dbURL)
			if err != nil {
				log.Fatalf("init shared rate counters: %v", err)
			}
			defer pgCounter.Close()
			counter = pgCounter
		} else {
			log.Printf("rate counters: memory (single instance only)")
		}
		if usagePlugin != nil {
			usagePlugin.SetUsageSink(counter)
		}

		overloadCfg := overload.FromEnv()
		overloadGate = overload.New(overloadCfg)
		defer overloadGate.Close()
		keyed := overload.NewKeyed()

		// M6 脱敏：内置规则 + DB 规则；默认 rules-only（ADR-008）。
		maskHolder := redact.NewHolder(redact.NewEngine(redact.DefaultRules()))
		maskLoader, err := redact.NewLoader(ctx, dbURL)
		if err != nil {
			log.Fatalf("init redact rules: %v", err)
		}
		defer maskLoader.Close()
		if rules, err := maskLoader.Load(ctx); err == nil {
			maskHolder.Set(redact.NewEngine(redact.Merge(redact.DefaultRules(), rules)))
		} else {
			log.Printf("redact: initial rule load failed, using built-ins: %v", err)
		}
		maskInterval := parseDuration(env("KEYKEEP_MASK_WATCH_INTERVAL", "2s"), 2*time.Second)
		go redact.Watch(ctx, maskLoader, maskHolder, maskInterval)
		maskCfg := redact.Config{
			Mode:    redact.NormalizeMode(env("KEYKEEP_MASK_MODE", "rules-only")),
			Holder:  maskHolder,
			Auditor: auditor,
		}
		if detectorURL := os.Getenv("KEYKEEP_MASK_MODEL_URL"); detectorURL != "" {
			maskCfg.Detector = redact.NewHTTPDetector(detectorURL)
		}
		log.Printf("redaction enabled (mode=%s rules=%d detector=%t)",
			maskCfg.Mode, maskHolder.Get().RuleCount(), maskCfg.Detector != nil)

		// 转发链顺序：幂等 → 脱敏 → 白名单 → 限流 → 并发/排队 → 预算 → 访问列表就绪 → SDK。
		claims, err := idempotency.NewStore(context.Background(), dbURL)
		if err != nil {
			if errors.Is(err, idempotency.ErrAlreadyRunning) {
				log.Fatalf("网关未启动：%v。这台机器上已经有一个网关在跑（托盘里可能已显示运行中），本次不重复启动，也不会影响正在运行的实例。", err)
			}
			log.Fatalf("init request claims: %v", err)
		}
		defer claims.Close()
		forward := server.NewProxyHandler(target)
		// 访问列表就绪门在预算之后、SDK 之前：此时 claim 已建（幂等最外层），
		// 客户端断连的 outcome_unknown 语义不受就绪等待影响；预算已预扣，
		// 超时放行后的失败由「多退少补」结算兜底。
		forward = virtualkey.EnsureAccess(keyPrefix, virtualKeyEnsure, forward)
		forward = budget.Middleware(gate, forward)
		forward = overload.Middleware(overloadGate, keyed, limitStore, auditor, forward)
		forward = ratelimit.Middleware(counter, limitStore, auditor, forward)
		forward = policy.Whitelist(auditor, forward)
		// 脱敏在幂等之后（哈希用原始字节）、白名单之前（下游看到打码后的体）。
		forward = redact.Middleware(maskCfg, forward)
		// 幂等最外层：重复请求在限流/预算/上游之前被拒绝。
		forward = idempotency.Middleware(claims, auditor, forward)
		// 请求体只读一次：幂等/白名单/限流/预算共享同一份原始字节。
		forward = reqbody.Middleware(forward)

		// 自助查询在网关本地处理，不占限流/并发/预算额度。
		selfPool, err := dbpool.Open(context.Background(), dbURL)
		if err != nil {
			log.Fatalf("init self-service pool: %v", err)
		}
		defer selfPool.Close()
		self := selfservice.New(selfPool, counter, limitStore)

		// 指标助手：把依赖数据库的计数接进已建好的注册表。
		// 关闭方式：KEYKEEP_METRICS=off —— 只在回环/内网暴露的前提下才建议开启。
		if metricsRegistry != nil {
			metricsRegistry.BindDatabase(metrics.Options{
				Gate:                overloadGate,
				RateLimitFailClosed: ratelimit.FailClosedCount,
				Ready:               func() bool { return store.Ping(context.Background()) == nil },
			})
			ratelimit.SetMetrics(metricsRegistry)
			auditor.SetMetrics(metricsRegistry)
		}

		handler = server.NewRootHandlerWithMetrics(version.String(), self, selfservice.Handles, forward, metricsHandler)
		// 访问日志在虚拟 Key 之内，能带上 Key/员工 id；/healthz 与 /metrics 不记录。
		handler = virtualkey.Middleware(store, keyPrefix, auditor, accesslog.Middleware(handler))
		log.Printf("virtual key, whitelist, rate limit, overload, budget and self-service enabled (postgres; concurrency=%d queue=%d timeout=%s)",
			overloadCfg.MaxConcurrency, overloadCfg.QueueSize, overloadCfg.QueueTimeout)
	}
	listen := env("KEYKEEP_GATEWAY_LISTEN", ":8080")
	// 未连数据库时（本地联调 / 只跑 SDK）也要挂上 /metrics，否则同一份部署
	// 换个配置就让监控凭空消失。
	if metricsHandler != nil && dbURL == "" {
		handler = server.NewRootHandlerWithMetrics(version.String(), handler, nil, handler, metricsHandler)
	}
	httpServer := server.NewHTTPServer(listen, handler)

	go func() {
		log.Printf("keykeep gateway listening on %s (sdk %s)", listen, sdkAddr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("gateway stopped: %v", err)
			stop()
		}
	}()

	<-ctx.Done()
	// 先唤醒排队中的请求（503），再关闭 HTTP 服务。
	overloadGate.Close()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("gateway shutdown: %v", err)
	}
}
