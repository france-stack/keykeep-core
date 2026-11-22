package providers

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// 同一个逻辑别名由两条凭据提供时，配置里应保留两个供应商组（各自一把 Key），
// 这样停用其一后 SDK 仍有可用凭据（FR-10 / FA-10）。
func TestBuildConfigKeepsBothCredentialsForSameAlias(t *testing.T) {
	list := []Provider{
		{
			ID: "a", Name: "provider-a", Type: "anthropic", BaseURL: "http://mock",
			APIKey: "key-a", Models: []Model{{Alias: "shared", ModelID: "m1"}},
		},
		{
			ID: "b", Name: "provider-b", Type: "anthropic", BaseURL: "http://mock",
			APIKey: "key-b", Models: []Model{{Alias: "shared", ModelID: "m1"}},
		},
	}
	_, raw, err := BuildConfigFromBase("", nil, list, []string{"ik_key"}, Overrides{}, nil)
	if err != nil {
		t.Fatalf("build config: %v", err)
	}

	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode generated yaml: %v", err)
	}
	apiKeys, _ := doc["api-keys"].(map[string]any)
	groups, _ := apiKeys["claude"].([]any)
	if len(groups) != 2 {
		t.Fatalf("claude groups = %d, want 2\n%s", len(groups), raw)
	}
	seen := map[string]bool{}
	for _, item := range groups {
		group, _ := item.(map[string]any)
		keys, _ := group["keys"].([]any)
		if len(keys) != 1 {
			t.Fatalf("group %v keys = %d, want 1", group["name"], len(keys))
		}
		key, _ := keys[0].(map[string]any)
		seen[key["api-key"].(string)] = true
	}
	if !seen["key-a"] || !seen["key-b"] {
		t.Fatalf("missing credentials: %v", seen)
	}
}

// 缺陷回归（P1）：SDK 访问列表的键口径是 keyPrefix+UUID（见 BuildConfigFromBase
// 的 apiKeys 入参，来自 main.go 的 KEYKEEP_INTERNAL_KEY_PREFIX）。Relay 的
// HasKey/EnsureKey 只能按同一口径命中；传裸 UUID 时快路径永远打不中，中间件
// 每请求白等 KEYKEEP_ENSURE_KEY_WAIT（默认 3s）。
func TestHasKeyMatchesPrefixedAccessList(t *testing.T) {
	relay := NewRelay(nil, 0)
	relay.SeedAccessKeys([]string{"ik_abc"})

	if !relay.HasKey("ik_abc") {
		t.Error(`HasKey("ik_abc") = false, want true`)
	}
	if relay.HasKey("abc") {
		t.Error(`HasKey("abc") = true, want false (裸 UUID 不是访问列表的键口径)`)
	}

	ctx := context.Background()

	// 无 Run 协程时 kick 不会产生重载：裸 ID 必然等满 maxWait 后超时。
	if relay.EnsureKey(ctx, "abc", 20*time.Millisecond) {
		t.Error(`EnsureKey(ctx, "abc") = true, want false (裸 ID 命中不了快路径)`)
	}

	// 命中快路径（HasKey 为真即返回，无轮询等待）。
	if !relay.EnsureKey(ctx, "ik_abc", 20*time.Millisecond) {
		t.Error(`EnsureKey(ctx, "ik_abc") = false, want true (快路径应命中)`)
	}
}

func TestWriteConfigRewritesSamePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sdk.yaml")
	if err := WriteConfig(path, []byte("a: 1\n")); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := WriteConfig(path, []byte("a: 2\n")); err != nil {
		t.Fatalf("second write: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "a: 2\n" {
		t.Fatalf("content = %q", data)
	}
}

// 每种协议都要生成 SDK 对应的顶层配置段（gemini/vertex/xai/codex）。
func TestBuildConfigEmitsProtocolSections(t *testing.T) {
	list := []Provider{
		{ID: "g", Name: "gemini-a", Type: "gemini", BaseURL: "https://generativelanguage.googleapis.com", APIKey: "g-key", Models: []Model{{Alias: "gem", ModelID: "gemini-2.5-pro"}}},
		{ID: "v", Name: "vertex-a", Type: "vertex", BaseURL: "https://aiplatform.googleapis.com", APIKey: "v-key", Models: []Model{{Alias: "vx", ModelID: "gemini-2.5-pro"}}},
		{ID: "x", Name: "xai-a", Type: "xai", BaseURL: "https://api.x.ai/v1", APIKey: "x-key", Models: []Model{{Alias: "grok", ModelID: "grok-4"}}},
		{ID: "c", Name: "codex-a", Type: "codex", BaseURL: "https://api.openai.com/v1", APIKey: "c-key", Models: []Model{{Alias: "codex", ModelID: "gpt-5-codex"}}},
	}
	_, raw, err := BuildConfigFromBase("", nil, list, []string{"ik_key"}, Overrides{}, nil)
	if err != nil {
		t.Fatalf("build config: %v", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, section := range []string{"gemini-api-key", "vertex-api-key", "xai-api-key", "codex-api-key"} {
		entries, ok := doc[section].([]any)
		if !ok || len(entries) != 1 {
			t.Fatalf("%s missing or wrong length: %#v", section, doc[section])
		}
		entry, _ := entries[0].(map[string]any)
		if entry["api-key"] == "" {
			t.Fatalf("%s missing api-key", section)
		}
		models, _ := entry["models"].([]any)
		if len(models) != 1 {
			t.Fatalf("%s models = %d", section, len(models))
		}
	}
}
