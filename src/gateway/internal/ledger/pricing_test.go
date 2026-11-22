package ledger

import (
	"strings"
	"testing"
)

func TestPriceCost(t *testing.T) {
	p := Price{PromptPerM: 300, CompletionPerM: 1500, CacheReadPerM: 30}
	cases := []struct {
		name                       string
		input, output, read, write int64
		want                       int64
	}{
		{"zero", 0, 0, 0, 0, 0},
		{"one million input", 1_000_000, 0, 0, 0, 300},
		{"rounds up to one cent", 1, 0, 0, 0, 1},
		{"mixed", 1_000_000, 1_000_000, 0, 0, 1800},
		{"cache read cheaper", 0, 0, 1_000_000, 0, 30},
		{"cache write uses prompt price", 0, 0, 0, 1_000_000, 300},
	}
	for _, c := range cases {
		if got := p.Cost(c.input, c.output, c.read, c.write); got != c.want {
			t.Errorf("%s: got %d want %d", c.name, got, c.want)
		}
	}
}

func TestPriceCostFallsBackForMissingCachePrice(t *testing.T) {
	p := Price{PromptPerM: 100}
	if got := p.Cost(0, 0, 1_000_000, 0); got != 100 {
		t.Fatalf("cache read fallback: got %d", got)
	}
}

func TestChainHashIsDeterministicAndOrderSensitive(t *testing.T) {
	e := Entry{RID: "rid-1", TS: 1, KeyID: "k", EmpID: "e", ModelServed: "m", CostCents: 7}
	a := ChainHash(GenesisHash, e)
	if a != ChainHash(GenesisHash, e) {
		t.Fatal("hash not deterministic")
	}
	if len(a) != 64 || strings.Trim(a, "0123456789abcdef") != "" {
		t.Fatalf("hash shape: %q", a)
	}
	if a == ChainHash("f00", e) {
		t.Fatal("prev hash not bound")
	}
	e2 := e
	e2.CostCents = 8
	if a == ChainHash(GenesisHash, e2) {
		t.Fatal("cost not bound")
	}
}

func TestChainHashGolden(t *testing.T) {
	e := Entry{RID: "rid-1", TS: 1700000000, KeyID: "k1", EmpID: "e1",
		DeptID: "00000000-0000-0000-0000-000000000000", Provider: "anthropic",
		Alias: "a", ModelReq: "a", ModelServed: "m", AuthID: "auth",
		PriceVersion: "v1", InputTokens: 10, OutputTokens: 20, CostCents: 5,
		Status: "success", RequestID: "trace", Stream: true}
	// 与控制面 lib/ledger-hash.test.ts 共用同一向量。
	want := "b06f1a53b0c86a722fea701451a4b4264d382463c010e8cfda34ef596deaf0c1"
	if got := ChainHash(GenesisHash, e); got != want {
		t.Fatalf("golden chain hash = %s", got)
	}
}

func TestStatusFor(t *testing.T) {
	if StatusFor(true) != "failed" || StatusFor(false) != "success" {
		t.Fatal("status mapping")
	}
}

func TestRIDOrNewShapes(t *testing.T) {
	if ridOrNew("given") != "given" {
		t.Fatal("existing rid must be kept")
	}
	got := ridOrNew("")
	if len(got) != 36 || strings.Count(got, "-") != 4 {
		t.Fatalf("generated rid shape: %q", got)
	}
}
