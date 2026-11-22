package redact

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// pieceReader 每次 Read 只交出一个片段，用来模拟 SSE 的逐块到达。
type pieceReader struct {
	pieces []string
	i      int
}

func (r *pieceReader) Read(p []byte) (int, error) {
	if r.i >= len(r.pieces) {
		return 0, io.EOF
	}
	n := copy(p, r.pieces[r.i])
	r.i++
	return n, nil
}

func (r *pieceReader) Close() error { return nil }

func TestIsTokenPrefix(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"⟦", true},
		{"⟦P", true},
		{"⟦PII-", true},
		{"⟦PII-1", true},
		{"⟦PII-1234", true},
		{"⟦PII-1234⟧", false}, // 完整占位符不该继续压住
		{"⟦PII-1234⟧ ", false},
		{"⟦PII-12345", false}, // 五位编号永远凑不成合法占位符
		{"⟦PII-12a", false},
		{"⟦PIQ", false},
		{"hello", false},
		// ⟦ 的首字节（E2）与中间字节（9F）都算未完成前缀
		{string([]byte{0xE2}), true},
		{string([]byte{0xE2, 0x9F}), true},
	}
	for _, c := range cases {
		if got := isTokenPrefix(c.in); got != c.want {
			t.Errorf("isTokenPrefix(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestTokenHoldbackKeepsCompleteTokenOutOfHold(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"prefix⟦PII-1234⟧suffix", 0},            // 完整占位符不压，就地还原
		{"prefix⟦PII-1234", 11},                  // 只差 ⟧，必须等下一块
		{"⟦PII-1234" + string([]byte{0xE2}), 12}, // ⟧ 的首字节也被切断
		{"x⟦PII-12", 9},                          // 数字区中段
		{"totally plain text", 0},                // 无关文本不压
	}
	for _, c := range cases {
		if got := tokenHoldback([]byte(c.in)); got != c.want {
			t.Errorf("tokenHoldback(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestStreamRestorerRestoresTokenSplitAcrossPieces(t *testing.T) {
	m := NewMapper()
	secret := "4111111111111111"
	token := m.Placeholder(secret)
	stream := "data: {\"c\":\"card " + token + " ok\"}\n\n"
	// 一个字节一块：占位符必然被切断，连 ⟦/⟧ 也会被按字节切断。
	pieces := make([]string, 0, len(stream))
	for i := 0; i < len(stream); i++ {
		pieces = append(pieces, stream[i:i+1])
	}
	r := newStreamRestorer(&pieceReader{pieces: pieces}, m)

	var got []byte
	buf := make([]byte, 7) // 奇数缓冲，进一步打乱字节边界
	for {
		n, err := r.Read(buf)
		got = append(got, buf[:n]...)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read: %v", err)
		}
	}
	want := strings.ReplaceAll(stream, token, secret)
	if string(got) != want {
		t.Errorf("流式还原结果不符\n got=%q\nwant=%q", got, want)
	}
	if ContainsPlaceholder(string(got)) {
		t.Error("输出仍含占位符")
	}
}

func TestStreamRestorerFlushesTrailingPartialTokenAtEOF(t *testing.T) {
	m := NewMapper()
	m.Placeholder("13800001111")
	trailing := "tail ⟦PII-12" // 流在半个占位符处结束
	r := newStreamRestorer(&pieceReader{pieces: []string{trailing}}, m)
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("readall: %v", err)
	}
	if string(got) != trailing {
		t.Errorf("未完成尾部应原样放行: got %q want %q", got, trailing)
	}
}

func TestStreamRestorerPropagatesReadError(t *testing.T) {
	m := NewMapper()
	m.Placeholder("13800001111")
	r := newStreamRestorer(errReader{}, m)
	if _, err := io.ReadAll(r); err == nil {
		t.Fatal("读错误应向上传播")
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (errReader) Close() error             { return nil }

func TestTransformResponseRestoresEventStream(t *testing.T) {
	m := NewMapper()
	token := m.Placeholder("alice@example.com")
	events := "data: {\"u\":\"" + token + "\"}\n\ndata: [DONE]\n\n"
	resp := &http.Response{
		StatusCode:    http.StatusOK,
		ContentLength: -1,
		Header:        http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:          &pieceReader{pieces: []string{"data: {\"u\":\"", token[:6], token[6:] + "\"}\n\n", "data: [DONE]\n\n"}},
	}
	if err := transformResponse(resp, NewEngine(DefaultRules()), m); err != nil {
		t.Fatalf("transform: %v", err)
	}
	if _, ok := resp.Body.(*streamRestorer); !ok {
		t.Fatalf("SSE 响应应挂流式还原器，实得 %T", resp.Body)
	}
	if resp.Header.Get("Content-Length") != "" || resp.ContentLength != -1 {
		t.Errorf("改写长度后必须取消 Content-Length 声明: %q %d", resp.Header.Get("Content-Length"), resp.ContentLength)
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("readall: %v", err)
	}
	want := strings.ReplaceAll(events, token, "alice@example.com")
	if string(got) != want {
		t.Errorf("SSE 还原不符\n got=%q\nwant=%q", got, want)
	}
}

func TestTransformResponseLeavesSSEAloneWithoutMappings(t *testing.T) {
	// 零映射（绝大多数请求）不得包任何壳，也不得动 Content-Length。
	body := &pieceReader{pieces: []string{"data: a\n\n"}}
	resp := &http.Response{
		StatusCode:    http.StatusOK,
		ContentLength: -1,
		Header:        http.Header{"Content-Type": []string{"text/event-stream; charset=utf-8"}},
		Body:          body,
	}
	if err := transformResponse(resp, NewEngine(DefaultRules()), NewMapper()); err != nil {
		t.Fatalf("transform: %v", err)
	}
	if resp.Body != io.ReadCloser(body) {
		t.Errorf("零映射应原样透传，实得 %T", resp.Body)
	}
}

func TestTransformResponseDoesNotWrapKnownLengthBinary(t *testing.T) {
	m := NewMapper()
	m.Placeholder("13800001111")
	body := io.NopCloser(strings.NewReader("PNGBYTES"))
	resp := &http.Response{
		StatusCode:    http.StatusOK,
		ContentLength: int64(len("PNGBYTES")),
		Header:        http.Header{"Content-Type": []string{"image/png"}, "Content-Length": []string{"8"}},
		Body:          body,
	}
	if err := transformResponse(resp, NewEngine(DefaultRules()), m); err != nil {
		t.Fatalf("transform: %v", err)
	}
	if _, ok := resp.Body.(*streamRestorer); ok {
		t.Fatal("定长二进制响应不该被改成 chunked 流")
	}
	if resp.Header.Get("Content-Length") != "8" {
		t.Errorf("Content-Length 应保持 8, got %q", resp.Header.Get("Content-Length"))
	}
}
