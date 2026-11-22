package ratelimit

import "context"

// Counter 是限流计数后端：内存实现用于单实例/测试，PG 实现用于多实例一致计数。
// 所有方法都必须并发安全；Admit 返回的 Reservation 在请求结束时调用 Release。
type Counter interface {
	Admit(ctx context.Context, keyID string, cfg Config, estIn, estOut int64) (*Reservation, *Denial, error)
	AddActual(ctx context.Context, keyID string, inputTokens, outputTokens int64) error
	Snapshot(ctx context.Context, keyID string) (rpm, inTokens, outTokens int64, err error)
	Close()
}

// Reservation 表示一次准入占用；Release 可重复调用，第二次起为空操作。
type Reservation struct {
	release func()
}

func newReservation(release func()) *Reservation {
	return &Reservation{release: release}
}

func (r *Reservation) Release() {
	if r == nil || r.release == nil {
		return
	}
	release := r.release
	r.release = nil
	release()
}
