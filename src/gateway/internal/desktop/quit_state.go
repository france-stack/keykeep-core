// quitState 协调「请求退出」与「窗口就绪」两件事的先后顺序。
//
// 停止请求可能早于托盘窗口创建（首装初始化要 20–60 秒，这期间用户点托盘
// 菜单的「退出」或 `kkapp stop` 都会先到）。窗口还没建好时无处投递退出消息，
// 早先的实现直接 return，于是 GetMessage 循环永远不结束、进程常驻（F10）。
//
// 这里把「请求」与「投递」分开：请求只记状态，返回「现在能否立刻投递」；
// 窗口就绪时再问一次是否有欠投递。take() 保证一次请求只触发一次投递。
//
// 不依赖 Win32，测试在任何平台都能跑。
package desktop

import "sync"

type quitState struct {
	mu          sync.Mutex
	requested   bool
	delivered   bool
	windowReady bool
}

// request 记录一次退出请求，返回「现在是否应该立刻投递退出消息」。
func (q *quitState) request() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.requested = true
	return q.windowReady && !q.delivered
}

// ready 在窗口与消息循环线程号都就绪后调用，返回「是否有欠投递的退出请求」。
func (q *quitState) ready() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.windowReady = true
	return q.requested && !q.delivered
}

// take 标记本次投递已完成。只有真正投出去后才调用；投递失败时调用 reset
// 把状态退回「欠投递」，让上层（进程兜底路径）仍能观察到未完成的退出。
func (q *quitState) take() {
	q.mu.Lock()
	q.delivered = true
	q.mu.Unlock()
}

// reset 在投递失败时撤销 take，保持「请求还在、尚未投递」。
func (q *quitState) reset() {
	q.mu.Lock()
	q.delivered = false
	q.mu.Unlock()
}

// pending 报告「有请求且尚未投递」。进程兜底路径据此决定是否还要等。
func (q *quitState) pending() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.requested && !q.delivered
}
