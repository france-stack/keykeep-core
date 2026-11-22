// Package shell 是桌面版控制台的呈现层（ADR-013）：
// 把控制台从「ShellExecuteW 交给默认浏览器」换成 kkdesk 内的 WebView2 原生窗口。
//
// 三条硬边界，改动前先读：
//
//  1. 线程。WebView2 的 COM 对象要求 STA 线程 + Win32 消息泵。窗口的创建、
//     导航与销毁全部发生在 runtime.LockOSThread() 的专用 goroutine 里；
//     其它 goroutine 只通过 channel 投递命令，绝不直接调 Win32。
//  2. 导航。窗口只允许加载控制台同源地址；其余一律拦下来交给系统默认浏览器。
//     判定逻辑在 navpolicy.go 里做成纯函数，单测覆盖 allow/openExternal/block
//     三个分支——安全边界不能只靠人工点一遍验证。
//  3. 回退。WebView2 运行时缺失或窗口创建失败时回退浏览器，绝不静默降级成
//     「打不开」。kill-switch（settings.json uiShell / KEYKEEP_SHELL_MODE）
//     让用户在个别机器上自行切回浏览器模式，不需要等新版本。
//
// 本包不依赖 desktop 包：浏览器打开与日志都通过注入的函数传入，
// 于是 Presenter 的决策逻辑可以在任何平台上跑单测。
package shell
