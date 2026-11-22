package audit

import (
	"context"
	"os"
	"testing"
	"time"
)

// 这条测试只在显式给了 KEYKEEP_LIVE_DATABASE_URL 时才跑：它要真的写库，
// 而普通 `go test ./...` 不允许有副作用。
//
// 它守的是一个跨语言约定里最容易悄悄坏掉的部分：Go 写进去的行，控制面
// （src/control/lib/audit.ts 的 verifyStoredAuditChain）必须能复算出来。
// 单元测试只能对上 8 条固定向量；这里对的是"真实 JSONB 归一化之后"的那一行。
//
//	go test ./internal/audit -run Live -v
func TestLiveAuditChainWrite(t *testing.T) {
	url := os.Getenv("KEYKEEP_LIVE_DATABASE_URL")
	if url == "" {
		t.Skip("未设置 KEYKEEP_LIVE_DATABASE_URL，跳过真实写库测试")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	writer, err := NewWriter(ctx, url)
	if err != nil {
		t.Fatalf("建连失败: %v", err)
	}
	defer writer.Close()

	detail := map[string]any{
		"reason": "invalid_api_key",
		"path":   "/v1/chat/completions",
		"scope":  "global",
		"limit":  1000,
		"nested": map[string]any{"b": 2, "a": "中文键值"},
	}
	writer.Record(ctx, "key.auth_failed", "", detail, "live-test-1")

	// 用同一个 Writer 的连接把刚写的那行读回来，独立复算一遍摘要。
	var (
		seq      int64
		at       time.Time
		action   string
		prevHash *string
		rowHash  *string
		algo     *string
		rawDet   []byte
		actor    *string
		rid      *string
	)
	err = writer.pool.QueryRow(ctx,
		`SELECT seq, at, action, "prevHash", "rowHash", "hashAlgo", detail, "actorId", "requestId"
		   FROM "AuditEvent" WHERE "requestId" = 'live-test-1'
		  ORDER BY seq DESC LIMIT 1`).
		Scan(&seq, &at, &action, &prevHash, &rowHash, &algo, &rawDet, &actor, &rid)
	if err != nil {
		t.Fatalf("回读失败: %v", err)
	}
	if rowHash == nil || *rowHash == "" {
		t.Fatal("网关写入的审计行没有 rowHash——链又断了")
	}
	if prevHash == nil || *prevHash == "" {
		t.Fatal("网关写入的审计行没有 prevHash")
	}

	row := ChainRow{
		Seq:       seq,
		At:        TruncateToMillis(at),
		Action:    action,
		Detail:    map[string]any{},
		ActorID:   deref(actor),
		RequestID: deref(rid),
	}
	if err := unmarshalJSON(rawDet, &row.Detail); err != nil {
		t.Fatalf("detail 解析失败: %v", err)
	}
	want, err := ChainHash(*prevHash, row, deref(algo), os.Getenv("KEYKEEP_AUDIT_CHAIN_KEY"))
	if err != nil {
		t.Fatalf("复算失败: %v", err)
	}
	if want != *rowHash {
		t.Fatalf("复算不一致: got=%s stored=%s", want, *rowHash)
	}
	if prev := *prevHash; prev != GenesisHash {
		// 前驱必须真的指向上一行，否则说明有别的写入路径绕过了链锁。
		var linked bool
		if err := writer.pool.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM "AuditEvent" WHERE "rowHash" = $1)`, prev).Scan(&linked); err != nil {
			t.Fatalf("核对前驱失败: %v", err)
		}
		if !linked {
			t.Fatalf("prevHash %s 在表里找不到对应行——链分叉了", prev)
		}
	}
	t.Logf("seq=%d algo=%s rowHash=%s", seq, deref(algo), *rowHash)
}
