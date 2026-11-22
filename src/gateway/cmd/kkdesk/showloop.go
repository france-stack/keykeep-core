package main

import "log"

// showSignal 是「请求显示控制台」的最小子集。
//
// 抽成接口是为了给 waitShowRequests 做单测：这条循环曾经因为漏掉 Wait 而
// 变成忙等——每次 PostMessage 到 UI 线程，实测 3 分钟内烧掉 186 秒 CPU，
// 窗口彻底无响应。它必须在任何平台上都能被断言「没有请求时不动作」。
type showSignal interface {
	// Wait 阻塞直到有人置位。
	Wait()
	// Reset 把信号复位成未置位。
	Reset()
}

// waitShowRequests 阻塞等待「显示控制台」请求，每来一次调用一次 show。
//
// 顺序是 Wait → Reset → show，三段都不能换：
//   - 先 Wait：没有请求时循环必须停在这里。漏掉它就是从启动起无限 Focus，
//     每次 Focus 都往 UI 线程投一条消息，窗口的消息队列被灌满后整窗卡死。
//   - 再 Reset：信号是 manual reset，置位后保持；不先复位的话 Wait 立刻返回，
//     一次点击会触发无限次聚焦。
//   - 最后 show：复位完再动作，动作期间来的新请求会让下一轮 Wait 立即返回，
//     不会漏掉第二次双击。
func waitShowRequests(sig showSignal, show func() error) {
	for {
		sig.Wait()
		sig.Reset()
		if err := show(); err != nil {
			log.Printf("显示控制台失败: %v", err)
		}
	}
}
