package ledger

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
)

// Price 是每百万 token 的单价，单位「分」。
// ModelCatalog 的 pricing_*_cents 一律按 1M token 计价，避免浮点与精度漂移。
type Price struct {
	PromptPerM     int64
	CompletionPerM int64
	CacheReadPerM  int64
	CacheWritePerM int64
}

// Cost 按整数分计算一次请求的费用，向上取整到分。
// 缓存读写缺价时回退输入价（兼容未维护缓存价的价格版本）。
func (p Price) Cost(input, output, cacheRead, cacheWrite int64) int64 {
	cacheReadPrice := p.CacheReadPerM
	if cacheReadPrice <= 0 {
		cacheReadPrice = p.PromptPerM
	}
	cacheWritePrice := p.CacheWritePerM
	if cacheWritePrice <= 0 {
		cacheWritePrice = p.PromptPerM
	}
	total := priceCents(input, p.PromptPerM) +
		priceCents(output, p.CompletionPerM) +
		priceCents(cacheRead, cacheReadPrice) +
		priceCents(cacheWrite, cacheWritePrice)
	return total
}

// EstimateCents 按预估值算预扣金额；输入 token 未知时用 0 只算输出。
func EstimateCents(input, output, promptPerM, completionPerM int64) int64 {
	return priceCents(input, promptPerM) + priceCents(output, completionPerM)
}

// FloorCost 按保底单价估算一段用量的费用下限（向上取整到分）。
// 未定价模型若按 0 结算，预算对它形同虚设；floorPerM<=0 时返回 0（保持历史行为）。
func FloorCost(input, output, floorPerM int64) int64 {
	if floorPerM <= 0 {
		return 0
	}
	total := input + output
	if total <= 0 {
		return 0
	}
	return (total*floorPerM + 999_999) / 1_000_000
}

// UnpricedFloorFromEnv 读取保底单价配置（分/百万 token）。
// 未设置或非法时返回 0 并记日志；配置后未定价用量按此保底计费，
// 行仍标 unpriced，事后可按真实价格修正。
const UnpricedFloorEnv = "KEYKEEP_UNPRICED_FLOOR_CENTS_PER_MTOK"

func UnpricedFloorFromEnv() int64 {
	raw := strings.TrimSpace(os.Getenv(UnpricedFloorEnv))
	if raw == "" {
		return 0
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v < 0 {
		log.Printf("ledger: invalid %s=%q, unpriced floor disabled", UnpricedFloorEnv, raw)
		return 0
	}
	return v
}

func priceCents(tokens, perMillion int64) int64 {
	if tokens <= 0 || perMillion <= 0 {
		return 0
	}
	return (tokens*perMillion + 999_999) / 1_000_000
}

// Entry 是写入哈希链所需的规范化字段。
type Entry struct {
	RID          string
	TS           int64
	KeyID        string
	EmpID        string
	DeptID       string
	Provider     string
	Alias        string
	ModelReq     string
	ModelServed  string
	AuthID       string
	PriceVersion string
	Unpriced     bool
	InputTokens  int64
	OutputTokens int64
	CacheRead    int64
	CacheWrite   int64
	CostCents    int64
	Status       string
	RequestID    string
	Stream       bool
	Failed       bool
}

// GenesisHash 是链首的 prev_hash。
const GenesisHash = "0000000000000000000000000000000000000000000000000000000000000000"

// ChainHash 计算 row_hash = sha256(canonical(entry) || prev)。
// canonical 用 \x1f 分隔，布尔统一成 0/1；时间是秒级 Unix（控制面用
// JavaScript Date 只有毫秒精度，秒级才能跨语言逐位复算）。
func ChainHash(prev string, e Entry) string {
	var b strings.Builder
	fields := []string{
		prev,
		e.RID,
		strconv.FormatInt(e.TS, 10),
		e.KeyID,
		e.EmpID,
		e.DeptID,
		e.Provider,
		e.Alias,
		e.ModelReq,
		e.ModelServed,
		e.AuthID,
		e.PriceVersion,
		boolField(e.Unpriced),
		strconv.FormatInt(e.InputTokens, 10),
		strconv.FormatInt(e.OutputTokens, 10),
		strconv.FormatInt(e.CacheRead, 10),
		strconv.FormatInt(e.CacheWrite, 10),
		strconv.FormatInt(e.CostCents, 10),
		e.Status,
		e.RequestID,
		boolField(e.Stream),
		boolField(e.Failed),
	}
	for i, f := range fields {
		if i > 0 {
			b.WriteByte(0x1f)
		}
		b.WriteString(f)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

func boolField(v bool) string {
	if v {
		return "1"
	}
	return "0"
}

// StatusFor 把一次调用归类成账本状态词。
func StatusFor(failed bool) string {
	if failed {
		return "failed"
	}
	return "success"
}

// FormatCents 只用于测试与诊断输出。
func FormatCents(c int64) string {
	return fmt.Sprintf("%d", c)
}
