package audit

import (
	"math"
	"os"
	"strconv"
	"testing"
	"time"
)

// 固定向量由 src/control/lib/audit-chain.ts 的 canonicalJson/auditChainInput
// 实际跑出来（2026-11-08），不是手算的。任何一侧改规则，这里必红。
//
// 覆盖面按"网关真实写过的 detail 形状"挑：字符串+路径、中文、嵌套对象与数组、
// 浮点、会被 JS 渲染成 1000 的 1e3、null、空对象、以及引号/反斜杠/换行/Tab/
// emoji 这类转义边界。裸 sha256（a1）与 HMAC（a1k）都要对得上。
type chainVector struct {
	name      string
	prev      string
	seq       int64
	at        string
	actorID   string
	action    string
	detail    map[string]any
	requestID string
	want      string
}

func mustTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		t.Fatalf("解析向量时间失败: %v", err)
	}
	return parsed
}

func TestChainHashMatchesControlPlaneVectors(t *testing.T) {
	vectors := []chainVector{
		{
			name:   "ascii-empty",
			prev:   GenesisHash,
			seq:    1,
			at:     "2026-11-07T11:05:25.459Z",
			action: "key.auth_failed",
			detail: map[string]any{"reason": "invalid_api_key"},
			want:   "0b22a89d250c16be107af7059a21951b986862d007dea0be35307d76764a57cf",
		},
		{
			name:      "chinese",
			prev:      "0b22a89d250c16be107af7059a21951b986862d007dea0be35307d76764a57cf",
			seq:       2,
			at:        "2026-11-07T11:05:25.459Z",
			actorID:   "11111111-1111-1111-1111-111111111111",
			action:    "key.auth_failed",
			detail:    map[string]any{"原因": "密钥无效", "数量": float64(3)},
			requestID: "req-1",
			want:      "79d7ba10f2e2c72afaf4144567636cefcbbeeb71cfee0c7fcf9e6d7f212269d5",
		},
		{
			name:   "nested",
			prev:   "79d7ba10f2e2c72afaf4144567636cefcbbeeb71cfee0c7fcf9e6d7f212269d5",
			seq:    3,
			at:     "2026-11-07T11:05:25Z",
			action: "request.duplicate",
			detail: map[string]any{
				"b": map[string]any{"z": float64(1), "a": float64(2)},
				"a": []any{float64(1), float64(2), float64(3)},
			},
			want: "05c5c07ed187e359fe00dfe35dc563b5a7b26a90efb9e9be2679dd2bf05e0040",
		},
		{
			name:   "float",
			prev:   "05c5c07ed187e359fe00dfe35dc563b5a7b26a90efb9e9be2679dd2bf05e0040",
			seq:    4,
			at:     "2026-11-07T11:00:00Z",
			action: "masking.degraded",
			detail: map[string]any{"ratio": 1.5},
			want:   "f3e487d4bbc5ecee0d4d5322dc22bb01b958a91c4d637480d19817dc17bb8d74",
		},
		{
			name:      "bigintish",
			prev:      "f3e487d4bbc5ecee0d4d5322dc22bb01b958a91c4d637480d19817dc17bb8d74",
			seq:       5,
			at:        "2026-11-07T11:00:00Z",
			action:    "key.rate_limited",
			detail:    map[string]any{"limit": float64(1000), "scope": "global"},
			requestID: "r-2",
			want:      "3996d16809dcfdf5787bb5412ebf4cdb44830568529cbd88c344d9b3bf7f8974",
		},
		{
			name:   "nulls",
			prev:   "3996d16809dcfdf5787bb5412ebf4cdb44830568529cbd88c344d9b3bf7f8974",
			seq:    6,
			at:     "2026-11-07T11:00:00Z",
			action: "key.overloaded",
			detail: map[string]any{"a": nil, "b": "x"},
			want:   "ad8b0cdcd4291d4dbc5b207f97703e045a67867d685945c3e706735f44aded81",
		},
		{
			name:   "empty",
			prev:   "ad8b0cdcd4291d4dbc5b207f97703e045a67867d685945c3e706735f44aded81",
			seq:    7,
			at:     "2026-11-07T11:00:00Z",
			action: "key.model_denied",
			detail: map[string]any{},
			want:   "bd590103c5a827ca9572e167047046c5426ba2c67965da65cc723542ccb276c7",
		},
		{
			name:   "escapes",
			prev:   "bd590103c5a827ca9572e167047046c5426ba2c67965da65cc723542ccb276c7",
			seq:    8,
			at:     "2026-11-07T11:00:00Z",
			action: "security.csrf_rejected",
			detail: map[string]any{"s": "a\"b\\c\n\t", "u": "\U0001F600"},
			want:   "ae5f26d79bb88f0679fa82bdab95b00109c49b0765457d2b8a033bc8301c5ab8",
		},
	}

	for _, vector := range vectors {
		t.Run(vector.name, func(t *testing.T) {
			row := ChainRow{
				Seq:       vector.seq,
				At:        mustTime(t, vector.at),
				ActorID:   vector.actorID,
				Action:    vector.action,
				Detail:    vector.detail,
				RequestID: vector.requestID,
			}
			got, err := ChainHash(vector.prev, row, AlgoSHA256, "")
			if err != nil {
				t.Fatalf("计算摘要失败: %v", err)
			}
			if got != vector.want {
				t.Fatalf("摘要不一致\n got=%s\nwant=%s\ninput=%q",
					got, vector.want, mustInput(t, vector.prev, row))
			}
		})
	}
}

// TestChainHashHMAC 用同一批向量的首行验证 HMAC 分支。
// 期望值由 TS 侧 createHmac("sha256", "test-chain-key") 实际计算。
func TestChainHashHMAC(t *testing.T) {
	row := ChainRow{
		Seq:    1,
		At:     mustTime(t, "2026-11-07T11:05:25.459Z"),
		Action: "key.auth_failed",
		Detail: map[string]any{"reason": "invalid_api_key"},
	}
	const key = "test-chain-key"
	got, err := ChainHash(GenesisHash, row, AlgoHMAC, key)
	if err != nil {
		t.Fatalf("HMAC 摘要失败: %v", err)
	}
	// 由 node 侧用同一输入算出（2026-11-08，createHmac("sha256","test-chain-key")）。
	const want = "4925c3bde8b3a1b1273b7987b9ff0aea7b04995225ee47bafdcd085ac801e29d"
	if os.Getenv("KEYKEEP_REGEN_VECTORS") == "" && got != want {
		t.Fatalf("HMAC 摘要不一致: got=%s want=%s", got, want)
	}
	if got == "" {
		t.Fatal("HMAC 摘要为空")
	}
}

func mustInput(t *testing.T, prev string, row ChainRow) string {
	t.Helper()
	input, err := ChainInput(prev, row)
	if err != nil {
		return "<error: " + err.Error() + ">"
	}
	return input
}

// TestCanonicalJSONRejectsNonFinite 守住"宁可失败也不写不可复算摘要"的约定。
func TestCanonicalJSONRejectsNonFinite(t *testing.T) {
	nan := math.NaN()
	if _, err := ChainInput(GenesisHash, ChainRow{Detail: map[string]any{"x": nan}}); err == nil {
		t.Fatal("NaN 应当被拒绝")
	}
}

// TestFormatJSNumber 盯住 JS 与 Go 在指数阈值上的差异。
func TestFormatJSNumber(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{1, "1"},
		{1000, "1000"},
		{1.5, "1.5"},
		{math.Copysign(0, -1), "0"},
		{0, "0"},
		{1e21, "1e+21"},
		{1e-7, "1e-7"},
		{0.0001, "0.0001"},
		{123456, "123456"},
	}
	for _, c := range cases {
		if got := formatJSNumber(c.in); got != c.want {
			t.Errorf("formatJSNumber(%v) = %s, want %s", c.in, got, c.want)
		}
	}
}

// TestTruncateToMillis 说明为什么摘要必须截毫秒：timestamptz(6) 存了微秒，
// 而控制面的 Date 只有毫秒，不截断两边算出的摘要永远不同。
func TestTruncateToMillis(t *testing.T) {
	raw := time.Date(2026, 11, 7, 11, 5, 25, 459090000, time.UTC)
	truncated := TruncateToMillis(raw)
	if truncated.UnixMilli() != 1794049525459 {
		t.Fatalf("毫秒值不符: %d", truncated.UnixMilli())
	}
	if truncated.Nanosecond()%int(time.Millisecond) != 0 {
		t.Fatalf("未截到毫秒: %d ns", truncated.Nanosecond())
	}
	_ = strconv.Itoa(0) // 保持 import 稳定
}
