package redact

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/keykeep/gateway/internal/httpmw"
	"github.com/keykeep/gateway/internal/reqbody"
	"github.com/keykeep/gateway/internal/server"
	"github.com/keykeep/gateway/internal/virtualkey"
)

// Mode 是三种脱敏预设。
type Mode string

const (
	ModeRulesOnly   Mode = "rules-only"
	ModeBlock       Mode = "block"
	ModePassThrough Mode = "pass-through"
)

// NormalizeMode 把环境变量值收敛到三种预设；未知值按 ADR-008 默认 rules-only。
func NormalizeMode(raw string) Mode {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "block", "strict":
		return ModeBlock
	case "pass-through", "passthrough", "off", "none":
		return ModePassThrough
	default:
		return ModeRulesOnly
	}
}

// Auditor 是审计旁路；nil 表示不写审计。契约定义收敛到 internal/httpmw（C17）。
type Auditor = httpmw.Auditor

// Config 是中间件配置。
type Config struct {
	Mode     Mode
	Holder   *Holder
	Detector Detector
	Auditor  Auditor
}

// Middleware 在幂等之后、白名单/限流/预算之前打码请求体，并登记响应还原钩子。
func Middleware(cfg Config, next http.Handler) http.Handler {
	mode := cfg.Mode
	if mode == "" {
		mode = ModeRulesOnly
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body == nil || !hasBody(r.Method) {
			next.ServeHTTP(w, r)
			return
		}
		info, _ := virtualkey.FromContext(r.Context())
		keyID, empID := "", ""
		if info != nil {
			keyID, empID = info.ID, info.EmpID
		}

		if mode == ModePassThrough {
			w.Header().Set("X-Masking", "off")
			audit(cfg.Auditor, r, "masking.bypass", empID, map[string]any{
				"keyId": keyID, "path": r.URL.Path, "mode": string(mode),
			})
			next.ServeHTTP(w, r)
			return
		}

		body, tooLarge := reqbody.Read(r)
		if tooLarge || len(body) == 0 {
			// 超大请求由白名单按既有逻辑返回 413；空 body 无需脱敏。
			next.ServeHTTP(w, r)
			return
		}

		if mode == ModeBlock && cfg.Detector == nil {
			audit(cfg.Auditor, r, "masking.blocked", empID, map[string]any{
				"keyId": keyID, "path": r.URL.Path, "reason": "detector_unavailable",
			})
			writeError(w, http.StatusServiceUnavailable, "masking_unavailable")
			return
		}

		engine := (*Engine)(nil)
		if cfg.Holder != nil {
			engine = cfg.Holder.Get()
		}
		if engine == nil {
			engine = NewEngine(DefaultRules())
		}

		mapper := NewMapper()
		masked := body
		if LooksJSON(body) {
			out, err := engine.MaskJSON(r.Context(), body, mapper, cfg.Detector)
			if err != nil {
				if mode == ModeBlock {
					audit(cfg.Auditor, r, "masking.blocked", empID, map[string]any{
						"keyId": keyID, "path": r.URL.Path, "reason": "detector_error",
					})
					writeError(w, http.StatusServiceUnavailable, "masking_unavailable")
					return
				}
				// rules-only：JSON 解析失败时对原文做不可逆打码，避免秘密原样外发。
				masked = []byte(engine.Sanitize(string(body)))
				audit(cfg.Auditor, r, "masking.degraded", empID, map[string]any{
					"keyId": keyID, "path": r.URL.Path, "reason": "invalid_json",
				})
			} else {
				masked = out
			}
		} else {
			masked = []byte(engine.Sanitize(string(body)))
		}

		if !bytes.Equal(masked, body) {
			r.Body = io.NopCloser(bytes.NewReader(masked))
			r.ContentLength = int64(len(masked))
		}
		w.Header().Set("X-Masking", string(mode))

		if mapper.Limited() {
			audit(cfg.Auditor, r, "masking.degraded", empID, map[string]any{
				"keyId": keyID, "path": r.URL.Path, "reason": "mapping_limit",
			})
		}

		ctx := WithMapper(r.Context(), mapper)
		ctx = server.WithResponseTransformer(ctx, func(resp *http.Response) error {
			return transformResponse(resp, engine, mapper)
		})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// readCloser 把已读前缀与原始剩余拼回完整 body 时保留原始 Closer。
type readCloser struct {
	io.Reader
	io.Closer
}

// transformResponse 处理响应回落：
//   - 可缓冲的（错误体、非流式 JSON）读满后一次性改写并修正 Content-Length；
//   - 不可缓冲的（SSE、分块文本）挂流式还原器，逐块吐占位符真值（审计 B4）；
//   - 压缩体与非文本定长二进制原样透传。
func transformResponse(resp *http.Response, engine *Engine, mapper *Mapper) error {
	if resp == nil || resp.Body == nil {
		return nil
	}
	// 压缩体不能改写：还原后的长度与压缩帧不再匹配。
	if resp.Header.Get("Content-Encoding") != "" {
		return nil
	}
	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	isError := resp.StatusCode >= 400
	// 事件流一律不缓冲：上游可以在错误头之后继续持有连接，读满等于挂住整条流。
	eventStream := strings.Contains(contentType, "text/event-stream")
	bufferable := !eventStream &&
		((isError && (strings.Contains(contentType, "json") || strings.Contains(contentType, "text"))) ||
			(!isError && strings.Contains(contentType, "application/json")))
	if !bufferable {
		// B4：SSE 曾经直接 return nil，占位符永远留在流里——客户端不但读到
		// ⟦PII-0001⟧，还会在下一轮对话里把它原样发回来，污染上下文。
		// 零映射时保持逐块透传，不给普通请求加任何开销。
		if mapper == nil || mapper.Count() == 0 || !restoreSafe(resp, eventStream) {
			return nil
		}
		resp.Body = newStreamRestorer(resp.Body, mapper)
		resp.ContentLength = -1
		resp.Header.Del("Content-Length")
		return nil
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, reqbody.MaxBytes+1))
	// 已读前缀留在 data，未读剩余仍在 resp.Body：无论超限还是读失败，
	// 都必须把两段拼回完整 body 原样透传；只挂前缀等于把响应静默截断。
	// 两个分支都不主动 Close——Closer 仍是原始 body，由 HTTP 栈在读完时关闭。
	if len(data) > reqbody.MaxBytes || err != nil {
		resp.Body = readCloser{Reader: io.MultiReader(bytes.NewReader(data), resp.Body), Closer: resp.Body}
		return nil
	}
	_ = resp.Body.Close()

	out := data
	if isError {
		out = []byte(engine.Sanitize(string(data)))
	} else if mapper != nil {
		out = []byte(mapper.Restore(string(data)))
	}
	resp.Body = io.NopCloser(bytes.NewReader(out))
	resp.ContentLength = int64(len(out))
	resp.Header.Set("Content-Length", strconv.Itoa(len(out)))
	return nil
}

// restoreSafe 判断改写响应体是否会破坏长度语义：事件流与分块体本来就没有
// Content-Length；带定长 Content-Length 的非文本体（图片/附件）不该被改成 chunked。
func restoreSafe(resp *http.Response, eventStream bool) bool {
	if eventStream {
		return true
	}
	if resp.ContentLength >= 0 {
		return false
	}
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	return strings.Contains(ct, "text") || strings.Contains(ct, "json") ||
		strings.Contains(ct, "xml") || strings.Contains(ct, "javascript") || ct == ""
}

func audit(auditor Auditor, r *http.Request, action, actorID string, detail map[string]any) {
	if auditor == nil {
		return
	}
	auditor.Record(r.Context(), action, actorID, detail, r.Header.Get("X-Request-Id"))
}

func writeError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": code}})
}

func hasBody(method string) bool {
	return method == http.MethodPost || method == http.MethodPut || method == http.MethodPatch
}

// LogSanitize 供其他包在输出日志前打码；没有引擎时返回原文。
func LogSanitize(engine *Engine, text string) string {
	if engine == nil {
		return text
	}
	return engine.Sanitize(text)
}
