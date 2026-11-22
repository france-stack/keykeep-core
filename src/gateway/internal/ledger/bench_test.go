package ledger

import "testing"

// benchEntry 是一条典型账本行。ChainHash 在每笔记账时都要跑，且它是审计
// 防篡改链（批次 H）的算力成本来源，因此它的每行成本直接决定高并发下
// 账本写入的 CPU 占比。
func benchEntry() Entry {
	return Entry{
		RID:          "rid-00000000-0001",
		TS:           1_700_000_000,
		KeyID:        "key-1",
		EmpID:        "emp-1",
		DeptID:       "dept-1",
		Provider:     "anthropic",
		Alias:        "default",
		ModelReq:     "claude-sonnet-4.5",
		ModelServed:  "claude-sonnet-4.5",
		AuthID:       "auth-1",
		PriceVersion: "2026-11",
		InputTokens:  1840,
		OutputTokens: 512,
		CacheRead:    1024,
		CostCents:    7,
		Status:       "ok",
		RequestID:    "req-1",
		Stream:       true,
	}
}

// BenchmarkChainHash 量单行哈希链计算（canonical 拼接 + sha256 + hex）。
func BenchmarkChainHash(b *testing.B) {
	e := benchEntry()
	prev := GenesisHash
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		prev = ChainHash(prev, e)
	}
	if prev == GenesisHash {
		b.Fatal("链未推进，基准无效")
	}
}

// BenchmarkEstimateCents 量预扣金额计算（纯整数运算，账本口径）。
func BenchmarkEstimateCents(b *testing.B) {
	var last int64
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		last = EstimateCents(int64(i%9000)+100, 1024, 300, 1500)
	}
	if last == 0 {
		b.Fatal("费用恒为 0，基准无效")
	}
}

// BenchmarkFloorCost 量未定价模型的保底预扣计算（批次 D 堵住的那条口径）。
func BenchmarkFloorCost(b *testing.B) {
	var last int64
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		last = FloorCost(int64(i%9000)+100, 1024, 500)
	}
	if last == 0 {
		b.Fatal("费用恒为 0，基准无效")
	}
}
