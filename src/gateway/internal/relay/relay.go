// Package relay 实现 KeyKeep 的员工端本地网关（client 模式）。
//
// 员工机器上只跑一个监听回环地址的转发进程：Claude Code / Codex / Gemini CLI
// 等 Agent 连 http://127.0.0.1:3426，本进程把请求原样转到老板的 KeyKeep 网关，
// 并用员工自己的虚拟 Key（kk_…）替换客户端带来的鉴权头。这样：
//   - Agent 配置里只出现本机地址，换服务器、换 Key 都不用改 Agent；
//   - 厂商凭据只在老板机器上，员工机器没有任何上游 Key；
//   - 员工 Key 只存在本机配置（0600）与进程内存。
//
// 形态参考 magpie 的本地网关（MIT，仅参考设计，未复制其代码）。
package relay

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"
)

// PlaceholderKey 是写进 Agent 配置的假 Key：本地连接器会用真实虚拟 Key 覆盖它，
// 所以 Agent 配置里永远看不到、也存不住真实 Key。
const PlaceholderKey = "keykeep-local"

// Config 是连接器的运行参数。
type Config struct {
	Upstream *url.URL // 老板的 KeyKeep 网关根地址（不带 /v1）
	APIKey   string   // 员工的虚拟 Key（kk_…）
	Version  string
	Listen   string // 本机监听地址，用于展示
	ClientID string // 机器名，进 X-KeyKeep-Client 便于服务端排障
}

type handler struct {
	cfg    Config
	proxy  *httputil.ReverseProxy
	client *http.Client

	mu        sync.Mutex
	reachable bool
	checkedAt time.Time
}

// NewHandler 返回本地连接器的 HTTP 入口。
func NewHandler(cfg Config) http.Handler {
	h := &handler{cfg: cfg, client: &http.Client{Timeout: 1500 * time.Millisecond}}

	proxy := httputil.NewSingleHostReverseProxy(cfg.Upstream)
	// SSE 必须逐块下发：缓冲会让 Claude Code 看起来「卡住」直到整段结束。
	proxy.FlushInterval = -1

	director := proxy.Director
	proxy.Director = func(r *http.Request) {
		director(r)
		// 客户端带来的任何凭据都不该到达服务端或上游：统一换成员工虚拟 Key。
		r.Header.Del("x-api-key")
		r.Header.Del("X-Api-Key")
		r.Header.Del("x-goog-api-key")
		r.Header.Del("Proxy-Authorization")
		r.Header.Set("Authorization", "Bearer "+cfg.APIKey)
		if cfg.ClientID != "" {
			r.Header.Set("X-KeyKeep-Client", cfg.ClientID)
		}
		// 每个入站请求补一个请求 ID：网关侧幂等声明、审计与账本都能串起同一次调用，
		// 也挡住传输层透明重试。客户端自带 X-Request-Id 时沿用不覆盖。
		if strings.TrimSpace(r.Header.Get("X-Request-Id")) == "" {
			r.Header.Set("X-Request-Id", newRequestID())
		}
		// Gemini CLI 会把 Key 放查询串（?key=…）；不能让它跟着请求进上游。
		if q := r.URL.Query(); q.Has("key") {
			q.Del("key")
			r.URL.RawQuery = q.Encode()
		}
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": map[string]any{
			"code":    "upstream_unreachable",
			"message": "无法连接公司网关：检查网络，或运行 kkconnect status 查看本机连接器状态。",
		}})
	}
	h.proxy = proxy
	return h
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 连接器内嵌员工虚拟 Key，必须只服务本机：
	// 1) Host 必须是回环——挡住 DNS rebinding（浏览器把 evil.com 解析到 127.0.0.1）；
	// 2) 浏览器跨站请求必带 Origin/Referer——非回环来源直接拒绝，
	//    堵住"恶意网页发简单请求烧员工配额"的路；Agent/curl 不带这些头，不受影响。
	if !IsLoopbackHost(r.Host) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": map[string]any{
			"code":    "forbidden_host",
			"message": "本连接器只接受本机地址访问。",
		}})
		return
	}
	for _, header := range []string{"Origin", "Referer"} {
		if value := r.Header.Get(header); value != "" {
			if u, err := url.Parse(value); err != nil || !IsLoopbackHost(u.Host) {
				writeJSON(w, http.StatusForbidden, map[string]any{"error": map[string]any{
					"code":    "forbidden_origin",
					"message": "本连接器不接受跨站请求。",
				}})
				return
			}
		}
	}
	switch r.URL.Path {
	case "/healthz":
		h.handleHealth(w, r)
	case "/keykeep/status":
		h.handleStatus(w, r)
	case "/favicon.ico":
		w.WriteHeader(http.StatusNoContent)
	case "/":
		if strings.Contains(r.Header.Get("Accept"), "text/html") {
			h.handlePage(w)
			return
		}
		h.handleStatus(w, r)
	default:
		h.proxy.ServeHTTP(w, r)
	}
}

// IsLoopbackHost 判断 host:port 的主机部分是否为回环地址。
// 空主机（":3426"）按非回环处理：Go 会把它绑到所有网卡。
func IsLoopbackHost(hostPort string) bool {
	host := hostPort
	if h, _, err := net.SplitHostPort(hostPort); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	host = strings.ToLower(host)
	return host == "127.0.0.1" || host == "localhost" || host == "::1"
}

// newRequestID 生成本次请求的追踪 ID（32 位十六进制，随机 16 字节）。
func newRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// 随机源不可用时退化为时间戳，保证请求 ID 仍存在。
		return fmt.Sprintf("req-%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// ---------------------------------------------------------------- status

type statusPayload struct {
	Status            string `json:"status"`
	Mode              string `json:"mode"`
	Version           string `json:"version"`
	Listen            string `json:"listen"`
	Upstream          string `json:"upstream"`
	UpstreamReachable bool   `json:"upstreamReachable"`
	KeyMasked         string `json:"keyMasked"`
	Client            string `json:"client,omitempty"`
	BaseURLAnthropic  string `json:"baseUrlAnthropic"`
	BaseURLOpenAI     string `json:"baseUrlOpenAI"`
}

func (h *handler) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":            "ok",
		"mode":              "client",
		"version":           h.cfg.Version,
		"upstream":          strings.TrimRight(h.cfg.Upstream.String(), "/"),
		"upstreamReachable": h.upstreamReachable(r.Context()),
	})
}

func (h *handler) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	anthropic, openai := h.baseURLs()
	writeJSON(w, http.StatusOK, statusPayload{
		Status:            "ok",
		Mode:              "client",
		Version:           h.cfg.Version,
		Listen:            h.cfg.Listen,
		Upstream:          strings.TrimRight(h.cfg.Upstream.String(), "/"),
		UpstreamReachable: h.upstreamReachable(r.Context()),
		KeyMasked:         maskKey(h.cfg.APIKey),
		Client:            h.cfg.ClientID,
		BaseURLAnthropic:  anthropic,
		BaseURLOpenAI:     openai,
	})
}

// upstreamReachable 探测服务端 /healthz，结果缓存 5 秒，避免状态页每刷一次就打一次上游。
func (h *handler) upstreamReachable(ctx context.Context) bool {
	h.mu.Lock()
	if !h.checkedAt.IsZero() && time.Since(h.checkedAt) < 5*time.Second {
		value := h.reachable
		h.mu.Unlock()
		return value
	}
	h.mu.Unlock()

	checkCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	reachable := false
	req, err := http.NewRequestWithContext(checkCtx, http.MethodGet,
		strings.TrimRight(h.cfg.Upstream.String(), "/")+"/healthz", nil)
	if err == nil {
		req.Header.Set("Authorization", "Bearer "+h.cfg.APIKey)
		if resp, err := h.client.Do(req); err == nil {
			reachable = resp.StatusCode >= 200 && resp.StatusCode < 300
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			_ = resp.Body.Close()
		}
	}

	h.mu.Lock()
	h.reachable = reachable
	h.checkedAt = time.Now()
	h.mu.Unlock()
	return reachable
}

// baseURLs 按监听地址算出员工该填进 Agent 的两个根地址。
func (h *handler) baseURLs() (string, string) {
	host := h.cfg.Listen
	switch {
	case strings.HasPrefix(host, ":"):
		host = "127.0.0.1" + host
	case strings.HasPrefix(host, "0.0.0.0:"):
		host = "127.0.0.1:" + strings.TrimPrefix(host, "0.0.0.0:")
	case host == "":
		host = "127.0.0.1:3426"
	}
	base := "http://" + host
	return base, base + "/v1"
}

func maskKey(key string) string {
	if len(key) <= 10 {
		return "kk_…"
	}
	return key[:7] + "…" + key[len(key)-6:]
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// ---------------------------------------------------------------- dashboard

func (h *handler) handlePage(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy",
		"default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'")
	// 占位 Key 在渲染时注入：HTML 模板里不写死，凭据形状的字符串只存在一处定义。
	_, _ = io.WriteString(w, strings.ReplaceAll(dashboardHTML, "%%KEY%%", PlaceholderKey))
}

// dashboardHTML 是员工本机能看到的唯一页面：连接状态与三个可直接抄的地址。
// 所有动态值都走 /keykeep/status，页面本身不含任何密钥。
const dashboardHTML = `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>KeyKeep 连接器</title>
<style>
 :root{color-scheme:dark}
 body{font:14px/1.6 -apple-system,"Segoe UI","Microsoft YaHei",sans-serif;margin:0;background:#0f1115;color:#e6e8ee}
 main{max-width:760px;margin:0 auto;padding:32px 20px}
 h1{font-size:20px;margin:0 0 4px}
 .sub{color:#9aa3b2;margin:0 0 24px}
 .card{background:#171a21;border:1px solid #262b36;border-radius:10px;padding:16px 18px;margin:0 0 14px}
 .row{display:flex;justify-content:space-between;gap:12px;padding:6px 0;border-bottom:1px solid #222733}
 .row:last-child{border:0}
 .k{color:#9aa3b2}
 code{background:#0d1016;border:1px solid #262b36;border-radius:6px;padding:2px 6px;font-family:ui-monospace,Consolas,monospace}
 .ok{color:#41c98a}.bad{color:#ff6b6b}
 pre{background:#0d1016;border:1px solid #262b36;border-radius:8px;padding:12px;overflow:auto;margin:0}
</style>
</head>
<body><main>
<h1>KeyKeep 连接器</h1>
<p class="sub">本机网关正在运行。Agent 只需要连这里，不用知道公司服务器地址。</p>
<div class="card" id="state">正在读取状态…</div>
<div class="card">
  <div class="row"><span class="k">Anthropic Base URL</span><code id="a">…</code></div>
  <div class="row"><span class="k">OpenAI Base URL</span><code id="o">…</code></div>
  <div class="row"><span class="k">API Key</span><code>keykeep-local</code></div>
</div>
<div class="card">
  <div class="k" style="margin-bottom:8px">Claude Code / Codex 由 kkconnect link 自动写配置；手动配置照抄：</div>
  <pre id="env">…</pre>
</div>
</main>
<script>
var $=function(id){return document.getElementById(id)};
function esc(v){return String(v).replace(/[&<>"']/g,function(c){return{'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]})}
async function tick(){
  try{
    var r=await fetch('/keykeep/status',{cache:'no-store'});
    var s=await r.json();
    $('a').textContent=s.baseUrlAnthropic;
    $('o').textContent=s.baseUrlOpenAI;
    $('env').textContent='export ANTHROPIC_BASE_URL="'+s.baseUrlAnthropic+'"\nexport ANTHROPIC_AUTH_TOKEN="%%KEY%%"\nexport OPENAI_BASE_URL="'+s.baseUrlOpenAI+'"\nexport OPENAI_API_KEY="%%KEY%%"';
    $('state').innerHTML=
      '<div class="row"><span class="k">上游服务器</span><code>'+esc(s.upstream)+'</code></div>'+
      '<div class="row"><span class="k">连接状态</span><span class="'+(s.upstreamReachable?'ok':'bad')+'">'+(s.upstreamReachable?'正常':'连不上，检查网络或找老板确认密钥')+'</span></div>'+
      '<div class="row"><span class="k">我的密钥</span><code>'+esc(s.keyMasked)+'</code></div>';
  }catch(e){ $('state').textContent='连接器未响应'; }
}
tick(); setInterval(tick,5000);
</script>
</body></html>`
