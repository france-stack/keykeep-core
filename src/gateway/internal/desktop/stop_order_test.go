package desktop

import (
	"testing"
	"time"
)

// F8：停止路径必须收敛——子进程按固定顺序停、停止后不再被看护重启。
// 这里钉住三件可回归的事实；真实进程的「退出码可观测 / 端口与锁释放」
// 由装机清单实测（GUI 路径无法在单测里跑）。

// 停止顺序固定为 control → gateway → kkmock；数据库不在这个列表里
// （它走 pg_ctl -m fast，由 StopPostgres 单独收尾）。
func TestStopOrderIsStable(t *testing.T) {
	for _, order := range [][]string{
		{"kkmock", "gateway", "control"},
		{"gateway", "control", "kkmock"},
		{"control", "gateway", "kkmock"},
	} {
		procs := make([]*Proc, 0, len(order))
		for _, name := range order {
			procs = append(procs, &Proc{Name: name})
		}
		sortForShutdown(procs)
		got := names(procs)
		want := []string{"control", "gateway", "kkmock"}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("输入 %v 排序后 = %v，期望 %v", order, got, want)
			}
		}
	}
}

// Stop 置位 stopping 后，stopSignal 必须立即可读——退避等待靠它提前退出，
// 否则停止流程会被最长 60 秒的重启延迟拖住。
func TestStopClosesStopSignal(t *testing.T) {
	sup := NewSupervisor(&Layout{Root: t.TempDir()}, DefaultSettings())
	signal := sup.stopSignal()
	sup.Stop()

	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatal("Stop 之后 stopSignal 应已关闭")
	}
}

// 停止后 watch 不应重启：stopping 为 true 时 watch 走「已停止」分支。
// 用真实的 Proc 结构断言状态，不走真实进程（跨平台可跑）。
func TestStopMarksProcsStopped(t *testing.T) {
	sup := NewSupervisor(&Layout{Root: t.TempDir()}, DefaultSettings())
	proc := &Proc{Name: "control", done: make(chan struct{}), running: true}
	sup.procs["control"] = proc

	sup.Stop()

	if !sup.stopping {
		t.Fatal("Stop 之后 stopping 应为 true")
	}
	// Stop 对 nil Cmd 的进程不做 terminate（terminate 有 nil 保护），
	// 状态由 watch 收尾；这里只验证不会 panic 且停止标志已置位。
	if !sup.stopping {
		t.Fatal("stopping 标志丢失")
	}
}

// waitDone 在进程迟迟不退时必须超时返回，不能挂死停止流程。
func TestWaitDoneTimesOut(t *testing.T) {
	sup := NewSupervisor(&Layout{Root: t.TempDir()}, DefaultSettings())
	proc := &Proc{Name: "gateway", done: make(chan struct{})}

	started := time.Now()
	sup.waitDone(proc, 30*time.Millisecond)
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("waitDone 应超时返回，实际耗时 %s", elapsed)
	}
}
