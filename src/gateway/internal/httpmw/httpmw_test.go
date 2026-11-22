package httpmw

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// flushOnlyWriter 实现 Flusher 但不实现 Unwrap：Recorder 必须把 Flush 透下去，
// 否则 SSE 会被攒成一块（这是网关最贵的一种"看起来正常"）。
type flushOnlyWriter struct {
	*httptest.ResponseRecorder
	flushes int
}

func (w *flushOnlyWriter) Flush() { w.flushes++ }

func TestRecorderKeepsFirstStatusCode(t *testing.T) {
	rec := httptest.NewRecorder()
	r := NewRecorder(rec)
	r.WriteHeader(http.StatusTooManyRequests)
	r.WriteHeader(http.StatusOK)
	if got := r.Status(); got != http.StatusTooManyRequests {
		t.Fatalf("首个状态码应胜出，得到 %d", got)
	}
	// Recorder 是纯观察者：线上传什么状态由内层 ResponseWriter 决定，
	// 它不能替中间件"改写"客户端看到的结果。
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("内层状态码 %d，期望 429", rec.Code)
	}
}

func TestRecorderCountsBytesAcrossWrites(t *testing.T) {
	rec := NewRecorder(httptest.NewRecorder())
	for _, s := range []string{"ab", "cde", ""} {
		if _, err := rec.Write([]byte(s)); err != nil {
			t.Fatal(err)
		}
	}
	if rec.Bytes != 5 {
		t.Fatalf("写出字节数 %d，期望 5", rec.Bytes)
	}
	if rec.Status() != http.StatusOK {
		t.Fatalf("隐式状态码应为 200，得到 %d", rec.Status())
	}
}

func TestRecorderStatusDefaultsToOKWithoutResponse(t *testing.T) {
	if NewRecorder(httptest.NewRecorder()).Status() != http.StatusOK {
		t.Fatal("未写响应时应按 net/http 约定返回 200")
	}
}

func TestRecorderForwardsFlush(t *testing.T) {
	inner := &flushOnlyWriter{ResponseRecorder: httptest.NewRecorder()}
	r := NewRecorder(inner)
	r.Flush()
	if inner.flushes != 1 {
		t.Fatalf("Flush 透传次数 %d，期望 1", inner.flushes)
	}
	// 没有 Flusher 的 ResponseWriter 不能让 Recorder  panic。
	NewRecorder(noFlushWriter{}).Flush()
}

type noFlushWriter struct{}

func (noFlushWriter) Header() http.Header       { return http.Header{} }
func (noFlushWriter) Write([]byte) (int, error) { return 0, nil }
func (noFlushWriter) WriteHeader(int)           {}

func TestRecorderUnwrapReachesInner(t *testing.T) {
	rec := httptest.NewRecorder()
	r := NewRecorder(rec)
	u, ok := r.Unwrap().(*httptest.ResponseRecorder)
	if !ok || u != rec {
		t.Fatal("Unwrap 必须返回被包装的原始 ResponseWriter")
	}
	// ReverseProxy 靠 Unwrap 找到 Flusher；包装链断了就退化成缓冲拷贝。
	rr := httptest.NewRecorder()
	fr := &flushOnlyWriter{ResponseRecorder: rr}
	if _, ok := http.ResponseWriter(NewRecorder(fr)).(interface{ Flush() }); !ok {
		t.Fatal("Recorder 自身必须是 Flusher")
	}
}

func TestRecorderWriteErrorStillReturnsInnerCount(t *testing.T) {
	rec := NewRecorder(errWriter{})
	n, err := rec.Write([]byte("hello"))
	if err == nil {
		t.Fatal("内层写失败必须上报")
	}
	// 内层确实吃下了 3 字节才报错：计数跟着真实写入走，不能凭空归零，
	// 否则访问日志的 bytes= 会和客户端实际收到的对不上。
	if n != 3 || rec.Bytes != 3 {
		t.Fatalf("部分写入应计入：n=%d bytes=%d", n, rec.Bytes)
	}
}

type errWriter struct{}

func (errWriter) Header() http.Header { return http.Header{} }
func (errWriter) Write([]byte) (int, error) {
	return 3, http.ErrNotSupported
}
func (errWriter) WriteHeader(int) {}
