package shell

import (
	"html"
	"strings"
)

// MessagePage 是导航被拦下时显示的合成页面。
//
// 为什么不用空白页：用户点了页脚的外链，窗口如果只是「没反应」，会被当成
// 软件坏了；一句「已交给浏览器打开」+ 一个回控制台的链接，把状态说清楚。
type MessagePage struct {
	Title   string
	Heading string
	Detail  string
	BackURL string
}

// HTML 产出完整页面（自带样式，不引任何外部资源）。
//
// 不引外部资源是硬要求：这个页面在窗口里显示，一旦引用 CDN，
// 「离线可用」的承诺就断在这里。
func (p MessagePage) HTML() string {
	var b strings.Builder
	b.WriteString("<!DOCTYPE html><html lang=\"zh-CN\"><head><meta charset=\"utf-8\">")
	b.WriteString("<title>")
	b.WriteString(html.EscapeString(p.Title))
	b.WriteString("</title><style>")
	b.WriteString("html,body{height:100%;margin:0;background:#0f172a;color:#e2e8f0;")
	b.WriteString("font-family:\"Microsoft YaHei\",\"PingFang SC\",system-ui,sans-serif}")
	b.WriteString(".wrap{height:100%;display:flex;align-items:center;justify-content:center}")
	b.WriteString(".card{max-width:560px;padding:32px 36px;background:#1e293b;border-radius:14px;")
	b.WriteString("box-shadow:0 10px 40px rgba(0,0,0,.35)}")
	b.WriteString("h1{font-size:19px;margin:0 0 12px}")
	b.WriteString("p{margin:0 0 18px;color:#94a3b8;font-size:13px;line-height:1.7}")
	b.WriteString("code{background:#0f172a;padding:2px 6px;border-radius:5px;color:#cbd5e1;word-break:break-all}")
	b.WriteString("a{display:inline-block;padding:9px 18px;background:#2563eb;color:#fff;")
	b.WriteString("border-radius:8px;text-decoration:none;font-size:13px}")
	b.WriteString("a:hover{background:#1d4ed8}</style></head><body><div class=\"wrap\"><div class=\"card\">")
	b.WriteString("<h1>")
	b.WriteString(html.EscapeString(p.Heading))
	b.WriteString("</h1><p><code>")
	b.WriteString(html.EscapeString(p.Detail))
	b.WriteString("</code></p>")
	if p.BackURL != "" {
		b.WriteString("<a href=\"")
		b.WriteString(html.EscapeString(p.BackURL))
		b.WriteString("\">返回控制台</a>")
	}
	b.WriteString("</div></div></body></html>")
	return b.String()
}
