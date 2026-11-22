package shell

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// Decision 是一次导航判定的结果。
type Decision string

const (
	// Allow：在窗口内加载。只有控制台同源地址配得上这个结果。
	Allow Decision = "allow"
	// OpenExternal：拦下来交给系统默认浏览器。外链与 file:// 走这条。
	OpenExternal Decision = "openExternal"
	// Block：直接拒绝，不交给任何浏览器。可执行的伪协议（javascript:、data:）
	// 走这条——把它们交给 ShellExecuteW 等于把「打开一个可执行内容」当成
	// 「打开一个网址」处理。
	Block Decision = "block"
)

// Policy 是窗口的导航白名单。
//
// 做成值类型 + 纯函数：判定逻辑不碰 Win32、不碰网络，任何平台都能跑单测。
// 安全边界靠人工点一遍是验证不了的——外链被拦在窗口外这件事，必须有断言。
type Policy struct {
	scheme string
	host   string
	port   string
}

// NewPolicy 用控制台地址构造白名单。
//
// 传进来的必须是回环地址（settings.ControlURL() 永远返回 127.0.0.1）。
// 局域网模式下控制台虽然也监听 0.0.0.0，但本机窗口仍然走回环——
// 让窗口去连自己广播出去的地址，会在换网时变成一个连不上的白屏。
func NewPolicy(controlURL string) (Policy, error) {
	parsed, err := url.Parse(strings.TrimSpace(controlURL))
	if err != nil {
		return Policy{}, fmt.Errorf("解析控制台地址失败: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return Policy{}, fmt.Errorf("控制台地址不完整：%q", controlURL)
	}
	port := parsed.Port()
	if port == "" {
		switch parsed.Scheme {
		case "http":
			port = "80"
		case "https":
			port = "443"
		}
	}
	return Policy{
		scheme: strings.ToLower(parsed.Scheme),
		host:   strings.ToLower(parsed.Hostname()),
		port:   port,
	}, nil
}

// Origin 返回 "scheme://host:port"，用于日志与错误信息。
func (p Policy) Origin() string {
	return p.scheme + "://" + net.JoinHostPort(p.host, p.port)
}

// Classify 判定一个 URI 该怎么处理。
//
// 规则（与 ADR-013 §2.4 一致）：
//   - 与控制台同 scheme/host/port → Allow；
//   - http/https 的其它来源、file: → OpenExternal（交系统默认浏览器）；
//   - about:blank → Allow（WebView2 的空白初始文档，不是可加载内容）；
//   - 其余（javascript:、data:、vbscript:、blob:、空串、解析失败）→ Block。
func (p Policy) Classify(raw string) Decision {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return Block
	}
	// about:blank 是 WebView2 建立文档时的初始地址，没有可执行内容。
	// 把它判成 Block 会在资源过滤层给出一个 403 响应体，反而打断初始化。
	if strings.EqualFold(trimmed, "about:blank") {
		return Allow
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return Block
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme == "" {
		// 没有 scheme 的输入无法判断指向哪里，按拒绝处理。
		return Block
	}
	switch scheme {
	case "http", "https":
		if p.sameOrigin(parsed) {
			return Allow
		}
		return OpenExternal
	case "file":
		// 本地文件不是控制台的一部分；交给系统打开（与旧行为一致）。
		return OpenExternal
	default:
		return Block
	}
}

func (p Policy) sameOrigin(parsed *url.URL) bool {
	if strings.ToLower(parsed.Scheme) != p.scheme {
		return false
	}
	if strings.ToLower(parsed.Hostname()) != p.host {
		return false
	}
	port := parsed.Port()
	if port == "" {
		switch strings.ToLower(parsed.Scheme) {
		case "http":
			port = "80"
		case "https":
			port = "443"
		}
	}
	return port == p.port
}

// NormalizeControlURL 把控制台地址归一成 "http://127.0.0.1:<port>"。
//
// 单独做一层是因为 Navigate 的入参和 Policy 的构造必须用同一个字符串：
// 两处各自拼地址，一旦一边多了个尾斜杠，同源判定就会在某个分支上悄悄失配。
func NormalizeControlURL(controlURL string, controlPort int) string {
	trimmed := strings.TrimRight(strings.TrimSpace(controlURL), "/")
	if trimmed != "" {
		return trimmed
	}
	return "http://127.0.0.1:" + strconv.Itoa(controlPort)
}
