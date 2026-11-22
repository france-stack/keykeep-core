package ratelimit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/keykeep/gateway/internal/reqbody"
)

func benchBody() string {
	return `{"model":"claude-sonnet-4.5","max_tokens":1024,"messages":[` +
		strings.Repeat(`{"role":"user","content":"`+strings.Repeat("你好这是一段中文上下文", 12)+`"},`, 8) +
		`{"role":"assistant","content":"ok"}]}`
}

// BenchmarkEstimate 量限流估值对请求体的成本：读体 + 一次全量 JSON 解析。
// 热路径四次全量解析中的第二次（批次 M 收敛为一次共享解析）。
func BenchmarkEstimate(b *testing.B) {
	body := benchBody()
	var sinkIn, sinkOut int64
	handler := reqbody.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sinkIn, sinkOut = estimate(r)
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		handler.ServeHTTP(rec, req)
	}
	_ = sinkIn
	_ = sinkOut
}

// BenchmarkEstimateFreshRequest 同上，但每次重建请求对象，量出真实单请求口径
// （含 httptest 构造成本，用于对齐"每请求"直觉）。
func BenchmarkEstimateFreshRequest(b *testing.B) {
	body := benchBody()
	handler := reqbody.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		estimate(r)
		w.WriteHeader(http.StatusOK)
	}))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
		handler.ServeHTTP(httptest.NewRecorder(), req)
	}
}

// BenchmarkLimiterAdmit 量内存计数器的单 Key 放行成本（含 60 桶窗口汇总）。
// 限额取极大值，保证测的是"放行"而不是"拒绝"分支。
func BenchmarkLimiterAdmit(b *testing.B) {
	l := newTestLimiter(time.Unix(1_700_000_000, 0))
	cfg := Config{RPM: 1 << 40, TPM: 1 << 40, ITPM: 1 << 40, OTPM: 1 << 40}
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		res, denial, err := l.Admit(ctx, "key-1", cfg, 800, 200)
		if err != nil || denial != nil {
			b.Fatalf("admit failed: err=%v denial=%+v", err, denial)
		}
		res.Release()
	}
}

// BenchmarkLimiterAdmitParallel 量多 Key 并发下的放行成本：分片锁的收益/残留
// 竞争只有并发基准才量得出来（单线程基准里 32 把锁和 1 把锁没区别）。
func BenchmarkLimiterAdmitParallel(b *testing.B) {
	l := newTestLimiter(time.Unix(1_700_000_000, 0))
	cfg := Config{RPM: 1 << 40, TPM: 1 << 40, ITPM: 1 << 40, OTPM: 1 << 40}
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			key := "key-" + string(rune('a'+i%26))
			i++
			res, denial, err := l.Admit(ctx, key, cfg, 800, 200)
			if err != nil || denial != nil {
				b.Fatalf("admit failed: err=%v denial=%+v", err, denial)
			}
			res.Release()
		}
	})
}

// BenchmarkLimiterSnapshot 量排障用窗口汇总成本：批次 B 把它降级为 debug-only
// 之后，它不应再出现在放行路径上，这里给出它本身的量级以便对比。
func BenchmarkLimiterSnapshot(b *testing.B) {
	l := newTestLimiter(time.Unix(1_700_000_000, 0))
	ctx := context.Background()
	if err := l.AddActual(ctx, "key-1", 800, 200); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, _, err := l.Snapshot(ctx, "key-1"); err != nil {
			b.Fatal(err)
		}
	}
}
