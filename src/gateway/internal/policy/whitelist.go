// Package policy 实施虚拟 Key 的模型白名单（FR-17 / FA-17）。
//
// 白名单为空表示不限制；非空时请求体中的 model 必须命中某条规则。
// 规则支持精确名与通配（* 任意长度、? 单字符），匹配前不区分路径分隔符——
// 模型名里常带 "/"（如 vendor/model），不能用 path.Match 的语义。
package policy

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/keykeep/gateway/internal/httpmw"
	"github.com/keykeep/gateway/internal/reqbody"
	"github.com/keykeep/gateway/internal/virtualkey"
)

// Auditor 是审计旁路；nil 表示不写审计。契约定义收敛到 internal/httpmw（C17）。
type Auditor = httpmw.Auditor

func writeError(w http.ResponseWriter, status int, body map[string]any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": body})
}

// Whitelist 在虚拟 Key 校验之后执行；白名单为空时直接放行。
func Whitelist(auditor Auditor, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		info, ok := virtualkey.FromContext(r.Context())
		if !ok || len(info.AllowedModels) == 0 {
			next.ServeHTTP(w, r)
			return
		}

		hint := reqbody.BodyHint(r)
		if hint.TooLarge {
			writeError(w, http.StatusRequestEntityTooLarge, map[string]any{"code": "request_too_large"})
			return
		}
		model := hint.Model
		if model == "" {
			// Gemini 协议把模型放在 URL 路径（/v1beta/models/{model}:generateContent），
			// body 里没有 model 字段；不看路径等于白名单对整个 Gemini 协议失效。
			model = geminiPathModel(r.URL.Path)
		}
		if model == "" {
			if hint.Size > 0 && hint.Err != nil {
				// 带 body 的请求解析不出模型名时按 fail-close 拒绝：
				// 放行等于允许构造畸形 body 绕过白名单。
				writeError(w, http.StatusBadRequest, map[string]any{"code": "invalid_request_body"})
				return
			}
			// 没有 model 字段的端点（如 GET /v1/models）不适用白名单。
			next.ServeHTTP(w, r)
			return
		}
		if MatchesAny(info.AllowedModels, model) {
			next.ServeHTTP(w, r)
			return
		}

		if auditor != nil {
			auditor.Record(r.Context(), "key.model_denied", info.EmpID, map[string]any{
				"keyId":   info.ID,
				"model":   model,
				"allowed": info.AllowedModels,
				"path":    r.URL.Path,
			}, r.Header.Get("X-Request-Id"))
		}
		writeError(w, http.StatusForbidden, map[string]any{
			"code":    "model_not_allowed",
			"model":   model,
			"allowed": info.AllowedModels,
		})
	})
}

// geminiPathModel 从 Gemini 风格 URL 路径提取模型名：
// /v1beta/models/gemini-2.0-flash:generateContent、/v1/models/gemini-2.0-flash 等。
// 模型段以 ":"（动作）或 "/"（子路径）结束；解析不出返回空串。
func geminiPathModel(path string) string {
	const marker = "/models/"
	i := strings.Index(path, marker)
	if i < 0 {
		return ""
	}
	rest := path[i+len(marker):]
	if j := strings.IndexByte(rest, ':'); j >= 0 {
		rest = rest[:j]
	}
	if j := strings.IndexByte(rest, '/'); j >= 0 {
		rest = rest[:j]
	}
	if rest == "" {
		return ""
	}
	if model, err := url.PathUnescape(rest); err == nil && model != "" {
		return model
	}
	return rest
}

// MatchesAny 判断模型是否命中任一白名单规则；供自助接口过滤模型列表复用。
func MatchesAny(patterns []string, model string) bool {
	for _, pattern := range patterns {
		if match(pattern, model) {
			return true
		}
	}
	return false
}

// match 是支持 * 与 ? 的通配匹配，逐字符回溯，不分配正则。
func match(pattern, name string) bool {
	p, n := 0, 0
	star, mark := -1, 0
	for n < len(name) {
		switch {
		case p < len(pattern) && (pattern[p] == '?' || pattern[p] == name[n]):
			p++
			n++
		case p < len(pattern) && pattern[p] == '*':
			star = p
			mark = n
			p++
		case star >= 0:
			p = star + 1
			mark++
			n = mark
		default:
			return false
		}
	}
	for p < len(pattern) && pattern[p] == '*' {
		p++
	}
	return p == len(pattern)
}
