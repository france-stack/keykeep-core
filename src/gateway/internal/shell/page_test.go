package shell

import (
	"strings"
	"testing"
)

func TestMessagePageEscapesContent(t *testing.T) {
	// 被拦下的地址是外部输入（用户点了什么链接，页面就有什么 URL），
	// 合成页面里必须转义，否则白名单页面自己成了一个反射型注入点。
	page := MessagePage{
		Title:   `<script>alert("t")</script>`,
		Heading: `<img src=x onerror=alert(1)>`,
		Detail:  `https://example.com/?q="><script>alert(1)</script>`,
		BackURL: `http://127.0.0.1:8443/?a="><script>`,
	}
	html := page.HTML()
	for _, raw := range []string{"<script>alert", "<img src=x", `q="><script>`} {
		if strings.Contains(html, raw) {
			t.Fatalf("合成页面未转义 %q：\n%s", raw, html)
		}
	}
	if !strings.Contains(html, "&lt;script&gt;") {
		t.Fatalf("应当包含转义后的内容:\n%s", html)
	}
}

func TestMessagePageHasNoExternalResources(t *testing.T) {
	html := MessagePage{Title: "t", Heading: "h", Detail: "d", BackURL: "http://127.0.0.1:8443"}.HTML()
	for _, needle := range []string{"http://cdn", "https://cdn", "<link", "<script src"} {
		if strings.Contains(html, needle) {
			t.Fatalf("合成页面不该引用外部资源（发现 %q）", needle)
		}
	}
}

func TestMessagePageBackLinkOnlyWhenURLProvided(t *testing.T) {
	withBack := MessagePage{Title: "t", Heading: "h", Detail: "d", BackURL: "http://127.0.0.1:8443"}.HTML()
	if !strings.Contains(withBack, "返回控制台") {
		t.Fatal("提供 BackURL 时应显示返回链接")
	}
	withoutBack := MessagePage{Title: "t", Heading: "h", Detail: "d"}.HTML()
	if strings.Contains(withoutBack, "返回控制台") {
		t.Fatal("没有 BackURL 时不该出现返回链接")
	}
}

func TestMessagePageIsCompleteDocument(t *testing.T) {
	html := MessagePage{Title: "t", Heading: "h", Detail: "d"}.HTML()
	for _, needle := range []string{"<!DOCTYPE html>", "<html", "</html>", `charset="utf-8"`} {
		if !strings.Contains(html, needle) {
			t.Fatalf("合成页面缺少 %q", needle)
		}
	}
}
