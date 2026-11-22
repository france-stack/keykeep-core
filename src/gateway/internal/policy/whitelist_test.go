package policy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/keykeep/gateway/internal/reqbody"
	"github.com/keykeep/gateway/internal/virtualkey"
)

type fakeStore struct{ info *virtualkey.KeyInfo }

func (s fakeStore) Lookup(ctx context.Context, keyHash []byte) (*virtualkey.KeyInfo, error) {
	return s.info, nil
}

type recorded struct {
	action  string
	actorID string
	detail  map[string]any
}

type fakeAuditor struct{ events []recorded }

func (a *fakeAuditor) Record(ctx context.Context, action, actorID string, detail map[string]any, requestID string) {
	a.events = append(a.events, recorded{action: action, actorID: actorID, detail: detail})
}

func keyInfo(models ...string) *virtualkey.KeyInfo {
	return &virtualkey.KeyInfo{ID: "key-1", EmpID: "emp-1", AllowedModels: models}
}

func newChain(info *virtualkey.KeyInfo, auditor Auditor) http.Handler {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	})
	return virtualkey.Middleware(fakeStore{info: info}, "ik_", nil, Whitelist(auditor, next))
}

func doRequest(t *testing.T, handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer kk_"+strings.Repeat("a", 43))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestMatch(t *testing.T) {
	cases := []struct {
		pattern string
		name    string
		want    bool
	}{
		{"claude-sonnet-4.5", "claude-sonnet-4.5", true},
		{"claude-sonnet-4.5", "claude-sonnet-4", false},
		{"claude-*", "claude-sonnet-4.5", true},
		{"claude-*", "gpt-5.1", false},
		{"*/qwen3", "alibaba/qwen3", true},
		{"vendor/model?", "vendor/model1", true},
		{"vendor/model?", "vendor/model12", false},
		{"*", "anything/at-all", true},
		{"a*b*c", "axxbyyc", true},
		{"a*b*c", "axxbyy", false},
	}
	for _, tc := range cases {
		if got := match(tc.pattern, tc.name); got != tc.want {
			t.Fatalf("match(%q, %q) = %v, want %v", tc.pattern, tc.name, got, tc.want)
		}
	}
}

func TestWhitelistAllowsMatchingModel(t *testing.T) {
	handler := newChain(keyInfo("claude-*"), nil)
	rec := doRequest(t, handler, http.MethodPost, "/v1/messages", `{"model":"claude-sonnet-4.5"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Body.String() != `{"model":"claude-sonnet-4.5"}` {
		t.Fatalf("body not restored: %q", rec.Body.String())
	}
}

func TestWhitelistDeniesOtherModel(t *testing.T) {
	auditor := &fakeAuditor{}
	handler := newChain(keyInfo("claude-*"), auditor)
	rec := doRequest(t, handler, http.MethodPost, "/v1/messages", `{"model":"gpt-5.1"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	var body struct {
		Error struct {
			Code    string   `json:"code"`
			Model   string   `json:"model"`
			Allowed []string `json:"allowed"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Error.Code != "model_not_allowed" || body.Error.Model != "gpt-5.1" {
		t.Fatalf("unexpected error body: %s", rec.Body.String())
	}
	if len(auditor.events) != 1 || auditor.events[0].action != "key.model_denied" {
		t.Fatalf("audit events = %+v", auditor.events)
	}
	if auditor.events[0].detail["model"] != "gpt-5.1" {
		t.Fatalf("audit detail = %+v", auditor.events[0].detail)
	}
}

func TestWhitelistEmptyMeansUnrestricted(t *testing.T) {
	handler := newChain(keyInfo(), nil)
	rec := doRequest(t, handler, http.MethodPost, "/v1/messages", `{"model":"anything"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestWhitelistSkipsRequestsWithoutModel(t *testing.T) {
	handler := newChain(keyInfo("claude-*"), nil)
	rec := doRequest(t, handler, http.MethodGet, "/v1/models", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestWhitelistRejectsOversizedBody(t *testing.T) {
	handler := newChain(keyInfo("claude-*"), nil)
	huge := `{"model":"claude-sonnet-4.5","pad":"` + strings.Repeat("x", reqbody.MaxBytes) + `"}`
	rec := doRequest(t, handler, http.MethodPost, "/v1/messages", huge)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
}

func TestGeminiPathModel(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"/v1beta/models/gemini-2.0-flash:generateContent", "gemini-2.0-flash"},
		{"/v1beta/models/gemini-2.0-flash:streamGenerateContent", "gemini-2.0-flash"},
		{"/v1beta/models/gemini-2.0-flash", "gemini-2.0-flash"},
		{"/v1beta/models/gemini-2.0-flash/countTokens", "gemini-2.0-flash"},
		{"/v1beta/models", ""},
		{"/v1/messages", ""},
		{"/v1beta/models/:generateContent", ""},
	}
	for _, tc := range cases {
		if got := geminiPathModel(tc.path); got != tc.want {
			t.Fatalf("geminiPathModel(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

func TestWhitelistChecksGeminiPathModel(t *testing.T) {
	// Gemini 协议的模型在 URL 路径里，body 没有 model 字段：
	// 白名单必须看路径，否则整个协议可绕过。
	handler := newChain(keyInfo("claude-*"), nil)
	rec := doRequest(t, handler, http.MethodPost,
		"/v1beta/models/gemini-2.0-flash:generateContent", `{"contents":[]}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestWhitelistAllowsGeminiPathModel(t *testing.T) {
	handler := newChain(keyInfo("gemini-*"), nil)
	rec := doRequest(t, handler, http.MethodPost,
		"/v1beta/models/gemini-2.0-flash:generateContent", `{"contents":[]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestWhitelistRejectsInvalidJSONBody(t *testing.T) {
	// 带 body 但解析不出模型名时 fail-close，堵住畸形 body 绕过白名单。
	handler := newChain(keyInfo("claude-*"), nil)
	rec := doRequest(t, handler, http.MethodPost, "/v1/messages", `{"model": broken`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}
