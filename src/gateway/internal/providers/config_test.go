package providers

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"os"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/keykeep/gateway/internal/breaker"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// 显式标注非真实凭据，避免 secret-scan 误报；输入与断言共用同一常量防止漂移。
const testUpstreamKey = "upstream-key-fixture-not-real"

func TestBuildConfigAnthropic(t *testing.T) {
	cfg, err := BuildConfig(&config.Config{AuthDir: "./auths"}, []Provider{{
		ID: "p1", Name: "mock-claude", Type: "anthropic",
		BaseURL: "http://127.0.0.1:19000", APIKey: testUpstreamKey,
		Models: []Model{{Alias: "mock-claude", ModelID: "claude-3-5-sonnet-20241022"}},
	}}, []string{"ik_p1"})
	if err != nil {
		t.Fatal(err)
	}

	if len(cfg.ClaudeKey) != 1 || len(cfg.OpenAICompatibility) != 0 {
		t.Fatalf("claude=%d openai=%d", len(cfg.ClaudeKey), len(cfg.OpenAICompatibility))
	}
	ck := cfg.ClaudeKey[0]
	if ck.APIKey != testUpstreamKey || ck.BaseURL != "http://127.0.0.1:19000" {
		t.Fatalf("unexpected claude key: %#v", ck)
	}
	if len(ck.Models) != 1 || ck.Models[0].Alias != "mock-claude" {
		t.Fatalf("unexpected models: %#v", ck.Models)
	}
	if len(cfg.APIKeys) != 1 || cfg.APIKeys[0] != "ik_p1" {
		t.Fatalf("access keys: %#v", cfg.APIKeys)
	}
}

// Anthropic 兼容中转只认 x-api-key 时，控制面的鉴权头必须落到 SDK 的 headers 段。
func TestBuildConfigAnthropicCustomAuthHeader(t *testing.T) {
	cfg, err := BuildConfig(&config.Config{AuthDir: "./auths"}, []Provider{{
		ID: "p3", Name: "opendesign", Type: "anthropic",
		BaseURL: "https://amr-link.open-design.ai", APIKey: testUpstreamKey,
		AuthHeaderName: "x-api-key", AuthScheme: "",
		Models: []Model{{Alias: "deepseek-v4.1-flash", ModelID: "deepseek-v4.1-flash"}},
	}}, []string{"ik_p1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.ClaudeKey) != 1 {
		t.Fatalf("claude=%d", len(cfg.ClaudeKey))
	}
	if got := cfg.ClaudeKey[0].Headers["x-api-key"]; got != testUpstreamKey {
		t.Fatalf("x-api-key header = %q, want %q", got, testUpstreamKey)
	}
}

// 默认 Authorization: Bearer 交给 SDK，不重复写入 headers。
func TestBuildConfigAnthropicDefaultAuthHeader(t *testing.T) {
	cfg, err := BuildConfig(&config.Config{AuthDir: "./auths"}, []Provider{{
		ID: "p4", Name: "claude-proxy", Type: "anthropic",
		BaseURL: "https://example.com", APIKey: testUpstreamKey,
		AuthHeaderName: "Authorization", AuthScheme: "Bearer",
		Models: []Model{{Alias: "claude-sonnet", ModelID: "claude-sonnet-4-5"}},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.ClaudeKey[0].Headers["Authorization"]; got != "" {
		t.Fatalf("default auth should not be overridden, got %q", got)
	}
}

func TestBuildConfigOpenAI(t *testing.T) {
	cfg, err := BuildConfig(&config.Config{AuthDir: "./auths"}, []Provider{{
		ID: "p2", Name: "mock-openai", Type: "openai",
		BaseURL: "http://127.0.0.1:19001", APIKey: "up",
		Models: []Model{{Alias: "mock-openai", ModelID: "gpt-4o-mini"}},
	}}, []string{"ik_p1"})
	if err != nil {
		t.Fatal(err)
	}

	if len(cfg.OpenAICompatibility) != 1 || len(cfg.ClaudeKey) != 0 {
		t.Fatalf("openai=%d claude=%d", len(cfg.OpenAICompatibility), len(cfg.ClaudeKey))
	}
	oc := cfg.OpenAICompatibility[0]
	if oc.Name != "mock-openai" || oc.BaseURL != "http://127.0.0.1:19001" {
		t.Fatalf("unexpected openai config: %#v", oc)
	}
	if len(oc.APIKeyEntries) != 1 || oc.APIKeyEntries[0].APIKey != "up" {
		t.Fatalf("unexpected key entries: %#v", oc.APIKeyEntries)
	}
	if len(oc.Models) != 1 || oc.Models[0].Alias != "mock-openai" {
		t.Fatalf("unexpected models: %#v", oc.Models)
	}
}

func TestDecrypt(t *testing.T) {
	kek := bytes.Repeat([]byte{7}, 32)
	block, err := aes.NewCipher(kek)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	nonce := bytes.Repeat([]byte{1}, 12)
	ct := gcm.Seal(nil, nonce, []byte("secret"), nil)
	got, err := decrypt(ct, nonce, kek)
	if err != nil || got != "secret" {
		t.Fatalf("got=%q err=%v", got, err)
	}
	if _, err := decrypt(ct, nonce, bytes.Repeat([]byte{8}, 32)); err == nil {
		t.Fatal("wrong key decrypted")
	}
}

func TestLoadKEK(t *testing.T) {
	raw := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=" // 32 zero bytes base64
	if _, err := LoadKEK(raw); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadKEK("short"); err == nil {
		t.Fatal("short KEK accepted")
	}
}

func TestBuildConfigPreservesBaseSettings(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/base.yaml"
	base := `config-version: 8
request-retry: 3
max-retry-credentials: 2
disable-cooling: true
streaming:
  keepalive-seconds: 0
`
	if err := os.WriteFile(path, []byte(base), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, raw, err := BuildConfigFromBase(path, &config.Config{AuthDir: "./auths"}, nil, []string{"ik_x"}, Overrides{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RequestRetry != 3 || cfg.MaxRetryCredentials != 2 || !cfg.DisableCooling {
		t.Fatalf("base settings dropped: retry=%d creds=%d cooling=%v",
			cfg.RequestRetry, cfg.MaxRetryCredentials, cfg.DisableCooling)
	}
	if len(cfg.APIKeys) != 1 || cfg.APIKeys[0] != "ik_x" {
		t.Fatalf("access keys = %v", cfg.APIKeys)
	}
	if !bytes.Contains(raw, []byte("routing")) && !bytes.Contains(raw, []byte("request-retry")) {
		t.Fatalf("generated yaml lost settings: %s", raw)
	}
}

func TestBuildConfigOverridesRetry(t *testing.T) {
	retry, creds := 0, 1
	cooling := true
	cfg, _, err := BuildConfigFromBase("", &config.Config{AuthDir: "./auths"}, nil, []string{"ik_x"},
		Overrides{RequestRetry: &retry, MaxRetryCredentials: &creds, DisableCooling: &cooling}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RequestRetry != 0 || cfg.MaxRetryCredentials != 1 || !cfg.DisableCooling {
		t.Fatalf("overrides not applied: retry=%d creds=%d cooling=%v",
			cfg.RequestRetry, cfg.MaxRetryCredentials, cfg.DisableCooling)
	}
}

// 空访问列表绝不能落进 SDK：内嵌 SDK 对零 access provider 是 fail-open
// （v8.0.47 sdk/access/manager.go Authenticate 零 provider 直接放行）。空列表
// 会让「键已签发、重载未发生」窗口里的请求绕过 SDK 层鉴权，且账本拿不到内部键
// 而丢账（2026-11-08 E2E 复跑实测丢 5 笔）。此时必须保留底稿 access 或写入
// 哨兵键（fail-closed），直到下一次重载把真实内部键写进去。
func TestBuildConfigFailsClosedOnEmptyAccessKeys(t *testing.T) {
	list := []Provider{
		{ID: "a", Name: "p", Type: "anthropic", BaseURL: "http://mock", APIKey: "key-a"},
	}
	for _, accessKeys := range [][]string{nil, {}} {
		_, raw, err := BuildConfigFromBase("", nil, list, accessKeys, Overrides{}, nil)
		if err != nil {
			t.Fatalf("build config with accessKeys=%v: %v", accessKeys, err)
		}
		var doc map[string]any
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("decode generated yaml: %v", err)
		}
		access, _ := doc["access"].(map[string]any)
		if access == nil {
			t.Fatalf("access section must exist (fail-closed), got none\n%s", raw)
		}
		keys, _ := access["api-keys"].([]any)
		if len(keys) == 0 {
			t.Fatalf("access.api-keys must not be empty (SDK fail-open)\n%s", raw)
		}
	}
}

// FR-26/27：熔断打开的凭据在生成配置时被整组剔除（SDK 看不到就不会选它），
// 其他凭据与同名别名不受影响。skip 的键必须与 breaker.CredentialID 同口径。
func TestBuildConfigSkipsOpenCredential(t *testing.T) {
	list := []Provider{
		{ID: "p1", Name: "primary", Type: "anthropic", BaseURL: "https://a.example",
			APIKey: "key-primary", Models: []Model{{Alias: "m", ModelID: "upstream-m"}}},
		{ID: "p2", Name: "backup", Type: "anthropic", BaseURL: "https://b.example",
			APIKey: "key-backup", Models: []Model{{Alias: "m", ModelID: "upstream-m"}}},
	}
	openID := breaker.CredentialID("key-primary", "https://a.example")
	_, raw, err := BuildConfigFromBase("", nil, list, []string{"ik_x"}, Overrides{},
		func(id string) bool { return id == openID })
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("key-primary")) {
		t.Fatal("open credential's key must not appear in generated config")
	}
	if !bytes.Contains(raw, []byte("key-backup")) {
		t.Fatal("backup credential must stay in generated config")
	}
	if bytes.Contains(raw, []byte("primary")) {
		t.Fatal("open credential's group must be removed entirely")
	}
}
