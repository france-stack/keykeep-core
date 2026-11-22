package audit

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// 审计事件防篡改链的 Go 侧实现。
//
// 判定规则只有一份，它在控制面（src/control/lib/audit-chain.ts），这里逐字
// 复刻出来是为了让数据面拒绝事件也进同一条链。两处实现必须产出**完全相同**
// 的摘要，否则控制面的全量走链会把网关写入的行全部报成 hash_mismatch——
// 链宁可没有，也不能有一条"看起来有、实际上互相指控"的链。
//
// 跨语言对账由 chain_test.go 的固定向量盯住：向量由 TypeScript 侧生成，Go 侧
// 只会因为真实的行为漂移而失败。改这里的任何一行，先问控制面同不同意。

// GenesisHash 是链的创世前驱（64 个 0），与 ledger-hash.ts 的 GENESIS_HASH 同值。
const GenesisHash = "0000000000000000000000000000000000000000000000000000000000000000"

// ChainLockKey 与账本 7411001、幂等守卫 7411002 同族；三者共用一把锁会互相阻塞。
const ChainLockKey int64 = 7411003

// 链算法标记。a1 = 裸 sha256，a1k = HMAC-sha256。
const (
	AlgoSHA256 = "a1"
	AlgoHMAC   = "a1k"
)

// ChainRow 是参与摘要的那一行的字段，与 TS 的 AuditChainRow 一一对应。
//
// At 必须是**库里那一行的时间**（毫秒精度）：摘要输入取的是 row.at.getTime()，
// 而 timestamptz(6) 有微秒。TS 侧 Date 只有毫秒，所以两边都按毫秒截断——
// 用完整微秒会让网关写入的行永远复算不出来。
type ChainRow struct {
	Seq       int64
	At        time.Time
	ActorID   string
	Action    string
	Detail    map[string]any
	RequestID string
}

// AlgoFor 与 TS 的 hashAlgoFor 一致：配了链密钥用 HMAC，否则裸 sha256。
func AlgoFor(key string) string {
	if key != "" {
		return AlgoHMAC
	}
	return AlgoSHA256
}

// ChainHash 计算一行的摘要；key 只在 algo == AlgoHMAC 时使用。
func ChainHash(prevHash string, row ChainRow, algo, key string) (string, error) {
	payload, err := ChainInput(prevHash, row)
	if err != nil {
		return "", err
	}
	if algo == AlgoHMAC {
		if key == "" {
			return "", errors.New("链算法 a1k 需要链密钥")
		}
		mac := hmac.New(sha256.New, []byte(key))
		mac.Write([]byte(payload))
		return hex.EncodeToString(mac.Sum(nil)), nil
	}
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:]), nil
}

// ChainInput 拼摘要输入：字段顺序固定，\x1f 分隔，空串代表 null。
func ChainInput(prevHash string, row ChainRow) (string, error) {
	detail, err := CanonicalJSON(row.Detail)
	if err != nil {
		return "", err
	}
	fields := []string{
		prevHash,
		strconv.FormatInt(row.Seq, 10),
		// TS 用 Date.getTime()（毫秒）。timestamptz 的微秒必须截掉，
		// 否则同一行在两种语言里算出两个摘要。
		strconv.FormatInt(row.At.UnixMilli(), 10),
		row.ActorID,
		row.Action,
		detail,
		row.RequestID,
	}
	return strings.Join(fields, "\x1f"), nil
}

// CanonicalJSON 复刻 TS 的 canonicalJson：对象键按码元序排序、undefined 丢弃、
// 数字按 JS 的 String() 规则渲染。
//
// 键排序用字节序而不是「按 UTF-16 码元序」——两者对 BMP 内的码点一致，只在
// 代理对（emoji 等，U+10000 以上）与 U+E000..U+FFFF 之间交界处不同。网关写入的
// detail 键全是 ASCII 标识符，这个差异不可达；控制面另行做过同样的判断。
func CanonicalJSON(value map[string]any) (string, error) {
	var b strings.Builder
	if err := writeCanonical(&b, value); err != nil {
		return "", err
	}
	return b.String(), nil
}

func writeCanonical(b *strings.Builder, value any) error {
	switch v := value.(type) {
	case nil:
		b.WriteString("null")
		return nil
	case bool:
		if v {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
		return nil
	case string:
		return writeJSString(b, v)
	case float64:
		return writeJSNumber(b, v)
	case float32:
		return writeJSNumber(b, float64(v))
	case int:
		b.WriteString(strconv.Itoa(v))
		return nil
	case int32:
		b.WriteString(strconv.FormatInt(int64(v), 10))
		return nil
	case int64:
		return writeJSNumber(b, float64(v))
	case uint64:
		return writeJSNumber(b, float64(v))
	case json.Number:
		f, err := v.Float64()
		if err != nil {
			return err
		}
		return writeJSNumber(b, f)
	case []string:
		b.WriteByte('[')
		for i, item := range v {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := writeCanonical(b, item); err != nil {
				return err
			}
		}
		b.WriteByte(']')
		return nil
	case []any:
		b.WriteByte('[')
		for i, item := range v {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := writeCanonical(b, item); err != nil {
				return err
			}
		}
		b.WriteByte(']')
		return nil
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key, item := range v {
			if item == nil && isUndefinedPlaceholder(v, key) {
				continue
			}
			keys = append(keys, key)
		}
		sort.Strings(keys)
		b.WriteByte('{')
		for i, key := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := writeJSString(b, key); err != nil {
				return err
			}
			b.WriteByte(':')
			if err := writeCanonical(b, v[key]); err != nil {
				return err
			}
		}
		b.WriteByte('}')
		return nil
	default:
		// 走到这里说明有人往 detail 里塞了 Date/struct/chan 之类 TS 也拒绝的值。
		// 宁可让这一条审计失败并留下日志，也不要写一个别人复算不出来的摘要。
		return errors.New("审计 detail 只接受纯 JSON")
	}
}

// isUndefinedPlaceholder 永远返回 false：Go 的 map 里没有"键存在但值是 undefined"
// 这一状态，nil 就是 JSON null，与 TS 的 null 同义。函数保留只为标明这个差异。
func isUndefinedPlaceholder(map[string]any, string) bool { return false }

// writeJSString 复刻 JSON.stringify 的字符串转义。
//
// 与 encoding/json 的差别有三处，都会改变摘要：encoding/json 默认把 < & >
// 转成 < 等（SetEscapeHTML），还会转义 U+2028/U+2029；JSON.stringify 三者
// 都原样输出。所以这里不能用 json.Marshal。
func writeJSString(b *strings.Builder, s string) error {
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				b.WriteString(`\u`)
				b.WriteString(hex4(uint16(r)))
				continue
			}
			// 非 ASCII、U+2028/2029、emoji 全部原样输出，与 JSON.stringify 一致。
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return nil
}

func hex4(v uint16) string {
	const digits = "0123456789abcdef"
	return string([]byte{
		digits[(v>>12)&0xF],
		digits[(v>>8)&0xF],
		digits[(v>>4)&0xF],
		digits[v&0xF],
	})
}

// writeJSNumber 复刻 Number.prototype.toString() 对 JSON 可出现数字的渲染。
//
// JSON.stringify 对数字走的是 JS 的 ToString：整数不带小数点，1e21 起改用指数
// 记法，1e-7 以下改用指数记法。Go 的 strconv.FormatFloat(f, 'g', -1, 64) 在
// 阈值上与 JS 不同（Go 从 1e21 起也是指数，但 1e-7 附近的分界与尾数写法不一致），
// 因此这里显式实现同一套规则。
func writeJSNumber(b *strings.Builder, f float64) error {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return errors.New("审计 detail 含非有限数字")
	}
	if f == 0 {
		b.WriteByte('0') // -0 也走这里，与 TS 的 Object.is(value, -0) ? "0" 一致
		return nil
	}
	b.WriteString(formatJSNumber(f))
	return nil
}

// formatJSNumber 按 ECMAScript Number::toString(10) 的规则渲染。
func formatJSNumber(f float64) string {
	abs := math.Abs(f)
	// 整数且在安全范围内：直接打整数，避免 1e3 被写成 1000.0 之类。
	if f == math.Trunc(f) && abs < 1e21 {
		// FormatFloat 对 -0 会输出 "-0"，而 JS 的 String(-0) 是 "0"。
		// 上面 writeJSNumber 已把 +0/-0 短路掉了，这里再兜一次是因为
		// formatJSNumber 也会被测试直接调用。
		if f == 0 {
			return "0"
		}
		return strconv.FormatFloat(f, 'f', 0, 64)
	}
	if abs >= 1e21 || abs < 1e-6 {
		// 指数形式：JS 写成 1e+21 / 1.5e-7（指数不带前导零）。
		s := strconv.FormatFloat(f, 'e', -1, 64)
		return normalizeExponent(s)
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// normalizeExponent 把 Go 的 1e+21 形态对齐 JS：Go 已输出 e+21，主要差异是
// Go 在 -1 精度下可能给出 1e-07（两位指数），JS 给 1e-7。
func normalizeExponent(s string) string {
	i := strings.IndexAny(s, "eE")
	if i < 0 {
		return s
	}
	mantissa := s[:i]
	exp := s[i+1:]
	sign := ""
	if strings.HasPrefix(exp, "+") || strings.HasPrefix(exp, "-") {
		sign = exp[:1]
		exp = exp[1:]
	}
	exp = strings.TrimLeft(exp, "0")
	if exp == "" {
		exp = "0"
	}
	return mantissa + "e" + sign + exp
}

// TruncateToMillis 把时间截到毫秒，与服务端 Date 的精度对齐。
func TruncateToMillis(t time.Time) time.Time { return t.Truncate(time.Millisecond) }

// unmarshalJSON 是 writeChain 与测试共用的 detail 解码入口。
func unmarshalJSON(raw []byte, out *map[string]any) error {
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
}
