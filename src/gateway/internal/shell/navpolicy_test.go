package shell

import "testing"

func mustPolicy(t *testing.T, controlURL string) Policy {
	t.Helper()
	policy, err := NewPolicy(controlURL)
	if err != nil {
		t.Fatalf("NewPolicy(%q) 失败: %v", controlURL, err)
	}
	return policy
}

func TestNewPolicyRejectsIncompleteURL(t *testing.T) {
	cases := []string{"", "   ", "127.0.0.1:8443", "/login"}
	for _, input := range cases {
		if _, err := NewPolicy(input); err == nil {
			t.Fatalf("NewPolicy(%q) 应当返回错误，实际没有", input)
		}
	}
}

func TestNewPolicyInfersDefaultPorts(t *testing.T) {
	cases := map[string]string{
		"http://127.0.0.1":       "http://127.0.0.1:80",
		"https://127.0.0.1":      "https://127.0.0.1:443",
		"http://127.0.0.1:8443":  "http://127.0.0.1:8443",
		"HTTP://127.0.0.1:8443/": "http://127.0.0.1:8443",
	}
	for input, want := range cases {
		if got := mustPolicy(t, input).Origin(); got != want {
			t.Fatalf("Origin(%q) = %q，期望 %q", input, got, want)
		}
	}
}

// allow 分支：只有控制台同源地址能在窗口内加载。
func TestClassifyAllowsOnlyControlOrigin(t *testing.T) {
	policy := mustPolicy(t, "http://127.0.0.1:8443")
	allowed := []string{
		"http://127.0.0.1:8443",
		"http://127.0.0.1:8443/",
		"http://127.0.0.1:8443/login",
		"http://127.0.0.1:8443/admin/keys?tab=1#top",
		"http://127.0.0.1:8443/api/healthz",
		// about:blank 是 WebView2 建立文档时的初始地址，没有可执行内容。
		"about:blank",
		"ABOUT:BLANK",
	}
	for _, uri := range allowed {
		if got := policy.Classify(uri); got != Allow {
			t.Fatalf("Classify(%q) = %q，期望 allow", uri, got)
		}
	}
}

// openExternal 分支：外部 http/https 与本地文件交给系统默认浏览器。
func TestClassifySendsExternalToBrowser(t *testing.T) {
	policy := mustPolicy(t, "http://127.0.0.1:8443")
	external := []string{
		"https://example.com/docs",
		"http://example.com",
		// 同主机不同端口不是同源。
		"http://127.0.0.1:8080/",
		"http://127.0.0.1:443/",
		// 同端口不同 scheme 不是同源。
		"https://127.0.0.1:8443/login",
		// 局域网地址不是窗口该连的地方。
		"http://192.168.1.10:8443/login",
		"file:///C:/Windows/win.ini",
	}
	for _, uri := range external {
		if got := policy.Classify(uri); got != OpenExternal {
			t.Fatalf("Classify(%q) = %q，期望 openExternal", uri, got)
		}
	}
}

// block 分支：可执行的伪协议一律拒绝，绝不交给 ShellExecuteW。
func TestClassifyBlocksExecutableSchemes(t *testing.T) {
	policy := mustPolicy(t, "http://127.0.0.1:8443")
	blocked := []string{
		"javascript:alert(1)",
		"JAVASCRIPT:alert(1)",
		"data:text/html,<script>alert(1)</script>",
		"vbscript:msgbox(1)",
		"blob:http://127.0.0.1:8443/1234",
		"",
		"   ",
		"/login",
		"localhost:8443",
		"127.0.0.1:8443/login",
	}
	for _, uri := range blocked {
		if got := policy.Classify(uri); got != Block {
			t.Fatalf("Classify(%q) = %q，期望 block", uri, got)
		}
	}
}

// 三段判定的分配是安全边界的核心：任何输入都必须落在这三个结果之一，
// 不允许出现「既不放行也不处置」的第四种状态。
func TestClassifyAlwaysReturnsKnownDecision(t *testing.T) {
	policy := mustPolicy(t, "https://127.0.0.1:9443")
	inputs := []string{
		"https://127.0.0.1:9443/me",
		"https://127.0.0.1:9443.evil.com/",
		"https://evil.com#@127.0.0.1:9443",
		"https://userinfo@evil.com/",
		"//evil.com/path",
		"http://127.0.0.1:9443%2f..%2fadmin",
		"\x00",
		"https://127.0.0.1:9443\x00.evil.com",
	}
	for _, uri := range inputs {
		switch got := policy.Classify(uri); got {
		case Allow, OpenExternal, Block:
		default:
			t.Fatalf("Classify(%q) 返回未知结果 %q", uri, got)
		}
	}
}

// 主机名大小写与尾点不影响同源判定，但伪装域名必须落到 openExternal。
func TestClassifyHostCaseAndSpoofing(t *testing.T) {
	policy := mustPolicy(t, "http://127.0.0.1:8443")
	if got := policy.Classify("http://LOCALHOST:8443/login"); got != OpenExternal {
		t.Fatalf("localhost 与 127.0.0.1 不是同一 host，实际 %q", got)
	}
	if got := policy.Classify("http://127.0.0.1.evil.com:8443/"); got != OpenExternal {
		t.Fatalf("伪装域名必须转交浏览器，实际 %q", got)
	}
}

func TestNormalizeControlURL(t *testing.T) {
	cases := []struct {
		input string
		port  int
		want  string
	}{
		{"http://127.0.0.1:8443/", 8443, "http://127.0.0.1:8443"},
		{"  http://127.0.0.1:9000  ", 8443, "http://127.0.0.1:9000"},
		{"", 8443, "http://127.0.0.1:8443"},
	}
	for _, c := range cases {
		if got := NormalizeControlURL(c.input, c.port); got != c.want {
			t.Fatalf("NormalizeControlURL(%q, %d) = %q，期望 %q", c.input, c.port, got, c.want)
		}
	}
}
