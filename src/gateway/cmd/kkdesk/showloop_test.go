package main

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeSignal 复刻 Win32 manual-reset 事件的语义：
// 未置位时 Wait 阻塞，置位后保持置位直到 Reset。
type fakeSignal struct {
	mu     sync.Mutex
	cond   *sync.Cond
	signal bool
	waits  atomic.Int32
}

func newFakeSignal() *fakeSignal {
	f := &fakeSignal{}
	f.cond = sync.NewCond(&f.mu)
	return f
}

func (f *fakeSignal) set() {
	f.mu.Lock()
	f.signal = true
	f.cond.Broadcast()
	f.mu.Unlock()
}

func (f *fakeSignal) Wait() {
	f.mu.Lock()
	f.waits.Add(1)
	for !f.signal {
		f.cond.Wait()
	}
	f.mu.Unlock()
}

func (f *fakeSignal) Reset() {
	f.mu.Lock()
	f.signal = false
	f.mu.Unlock()
}

// 没有请求时必须待住。这是卡死那个缺陷的直接回归：
// 循环漏掉 Wait 时会立刻开始刷 show，3 分钟内把 UI 线程烧到无响应。
func TestWaitShowRequestsBlocksWithoutSignal(t *testing.T) {
	sig := newFakeSignal()
	var shows atomic.Int32

	go waitShowRequests(sig, func() error {
		shows.Add(1)
		return nil
	})

	time.Sleep(200 * time.Millisecond)
	if got := shows.Load(); got != 0 {
		t.Fatalf("未置位时不应调用 show，实际 %d 次（忙等回归）", got)
	}
	if got := sig.waits.Load(); got != 1 {
		t.Fatalf("应当只进入 Wait 一次，实际 %d 次", got)
	}
}

// 一次置位只触发一次 show：Reset 必须在动作之前完成，
// 否则 manual-reset 信号保持置位，Wait 立即返回造成无限聚焦。
func TestWaitShowRequestsShowsOncePerSignal(t *testing.T) {
	sig := newFakeSignal()
	shown := make(chan struct{}, 8)

	go waitShowRequests(sig, func() error {
		shown <- struct{}{}
		return nil
	})

	sig.set()
	select {
	case <-shown:
	case <-time.After(2 * time.Second):
		t.Fatal("置位后应当调用 show")
	}

	// 给循环足够时间去「多跑几轮」——如果 Reset 漏了，这里会连续收到。
	select {
	case <-shown:
		t.Fatal("一次置位触发了多次 show（Reset 顺序回归）")
	case <-time.After(300 * time.Millisecond):
	}
}

// 连续两次请求应当各触发一次。
func TestWaitShowRequestsHandlesRepeatedSignals(t *testing.T) {
	sig := newFakeSignal()
	shown := make(chan struct{}, 8)

	go waitShowRequests(sig, func() error {
		shown <- struct{}{}
		return nil
	})

	for i := 0; i < 3; i++ {
		sig.set()
		select {
		case <-shown:
		case <-time.After(2 * time.Second):
			t.Fatalf("第 %d 次置位没有触发 show", i+1)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
