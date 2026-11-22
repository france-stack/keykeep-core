// 进程内 TTL 缓存，给账本热路径的 identity/price 查询减负。
//
// 条目数以 Key 数与模型数为上界（产品规模内千级），TTL 到期即失效——
// 撤销 Key、改价至多延迟一个 TTL 生效，对账本语义可接受（回调本身就是异步结算）。
package ledger

import (
	"sync"
	"time"
)

type ttlEntry[T any] struct {
	value     T
	expiresAt time.Time
}

type ttlCache[T any] struct {
	mu  sync.RWMutex
	ttl time.Duration
	m   map[string]ttlEntry[T]
	now func() time.Time
}

func newTTLCache[T any](ttl time.Duration) *ttlCache[T] {
	return &ttlCache[T]{ttl: ttl, m: make(map[string]ttlEntry[T]), now: time.Now}
}

// get 返回未过期值；不存在或已过期均为 false（过期条目懒淘汰）。
func (c *ttlCache[T]) get(key string) (T, bool) {
	if c == nil {
		var zero T
		return zero, false
	}
	c.mu.RLock()
	entry, ok := c.m[key]
	c.mu.RUnlock()
	if !ok || c.now().After(entry.expiresAt) {
		var zero T
		return zero, false
	}
	return entry.value, true
}

// put 写入值；nil 接收方（直接构造 Plugin 的测试）为无操作。
func (c *ttlCache[T]) put(key string, value T) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[key] = ttlEntry[T]{value: value, expiresAt: c.now().Add(c.ttl)}
}

type identityResult struct {
	empID  string
	deptID string
	exists bool
}

type priceResult struct {
	price   Price
	version string
	found   bool
}
