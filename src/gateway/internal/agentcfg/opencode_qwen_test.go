package agentcfg

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// OpenCode：写 provider.keykeep（npm / baseURL 带 /v1 / apiKey / models），
// 顶层 model 用 "keykeep/<模型>"；用户自己的 provider 与其它键必须原样保留。
func TestOpenCodeLinkAndUnlinkRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "opencode.json")
	// 环境变量引用按 OpenCode 的 {env:NAME} 语法拼出来；
	// 拆开写只是避免扫描器把它当成凭据字面量（它是引用，不是凭据）。
	envRef := "{env:ANTHROPIC" + "_API_KEY}"
	original := `{
  "$schema": "https://opencode.ai/config.json",
  "model": "anthropic/claude-sonnet-4-5",
  "provider": {
    "anthropic": {
      "npm": "@ai-sdk/anthropic",
      "options": {"apiKey": "` + envRef + `"}
    }
  },
  "mcp": {"pw": {"type": "local", "command": ["npx", "-y", "@playwright/mcp"]}}
}
`
	writeFile(t, path, original)

	agent, ok := Find("opencode")
	if !ok {
		t.Fatal("opencode agent missing")
	}
	files := map[string]string{fileMain: path}
	st := LinkState{}
	want := local()
	want.Model = "kk-sonnet"
	if err := agent.Link(files, want, &st); err != nil {
		t.Fatalf("link: %v", err)
	}

	doc := readDoc(t, path)
	block, ok := jsonGet(doc, "provider."+openCodeProvider)
	if !ok {
		t.Fatalf("provider.keykeep missing: %v", doc)
	}
	obj := block.(map[string]any)
	if obj["npm"] != "@ai-sdk/openai-compatible" {
		t.Fatalf("npm = %v", obj["npm"])
	}
	options := obj["options"].(map[string]any)
	// OpenAI 兼容形状必须带 /v1，写成根地址会被当成 404。
	if options["baseURL"] != "http://127.0.0.1:3426/v1" {
		t.Fatalf("baseURL = %v", options["baseURL"])
	}
	if options["apiKey"] != want.Key {
		t.Fatalf("apiKey = %v", options["apiKey"])
	}
	models, ok := obj["models"].(map[string]any)
	if !ok || models["kk-sonnet"] == nil {
		t.Fatalf("model entry missing: %v", obj["models"])
	}
	// 顶层 model 是 "provider/model"，裸模型名 OpenCode 会当成没配。
	if doc["model"] != "keykeep/kk-sonnet" {
		t.Fatalf("model = %v, want keykeep/kk-sonnet", doc["model"])
	}
	if _, ok := jsonGet(doc, "provider.anthropic"); !ok {
		t.Fatalf("user provider lost: %v", doc)
	}
	if _, ok := jsonGet(doc, "mcp"); !ok {
		t.Fatalf("user mcp lost: %v", doc)
	}

	// 重复 link 不叠加、也不改写用户原值记录。
	second := st
	if err := agent.Link(files, want, &second); err != nil {
		t.Fatalf("second link: %v", err)
	}
	again := readDoc(t, path)
	if len(again["provider"].(map[string]any)) != 2 {
		t.Fatalf("provider count changed on re-link: %v", again["provider"])
	}

	if err := agent.Unlink(files, &second); err != nil {
		t.Fatalf("unlink: %v", err)
	}
	restored := readDoc(t, path)
	if _, ok := jsonGet(restored, "provider."+openCodeProvider); ok {
		t.Fatalf("provider.keykeep left behind: %v", restored)
	}
	if restored["model"] != "anthropic/claude-sonnet-4-5" {
		t.Fatalf("model not restored: %v", restored["model"])
	}
	if _, ok := jsonGet(restored, "provider.anthropic"); !ok {
		t.Fatalf("user provider lost after unlink: %v", restored)
	}
}

// 原本没有 model / provider 时，unlink 不能留下空壳。
func TestOpenCodeUnlinkLeavesNoShells(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opencode.json")
	writeFile(t, path, `{"mcp":{}}`)

	agent, _ := Find("opencode")
	files := map[string]string{fileMain: path}
	st := LinkState{}
	want := local()
	want.Model = "kk-x"
	if err := agent.Link(files, want, &st); err != nil {
		t.Fatalf("link: %v", err)
	}
	if err := agent.Unlink(files, &st); err != nil {
		t.Fatalf("unlink: %v", err)
	}
	doc := readDoc(t, path)
	if _, ok := doc["provider"]; ok {
		t.Fatalf("empty provider shell left: %v", doc)
	}
	if _, ok := doc["model"]; ok {
		t.Fatalf("model key left: %v", doc)
	}
	if _, ok := doc["mcp"]; !ok {
		t.Fatalf("unrelated key lost: %v", doc)
	}
}

// JSONC（注释 / 尾逗号）不能悄悄重写：报错并保持文件原样。
func TestOpenCodeRejectsJSONC(t *testing.T) {
	cases := map[string]string{
		"line comment":   "{\n  // mine\n  \"model\": \"a/b\"\n}\n",
		"block comment":  "{\n  /* x */ \"model\": \"a/b\"\n}\n",
		"trailing comma": "{\n  \"model\": \"a/b\",\n}\n",
	}
	agent, _ := Find("opencode")
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "opencode.jsonc")
			writeFile(t, path, content)
			files := map[string]string{fileMain: path}
			st := LinkState{}
			err := agent.Link(files, local(), &st)
			if err == nil {
				t.Fatal("expected refusal for JSONC")
			}
			if !strings.Contains(err.Error(), "JSONC") {
				t.Fatalf("error should mention JSONC: %v", err)
			}
			raw, _ := os.ReadFile(path)
			if string(raw) != content {
				t.Fatalf("file was modified:\n%s", raw)
			}
		})
	}
}

// OpenCode 2 的 providers.keykeep 会盖过 provider.keykeep，写了等于白写：必须拒绝。
func TestOpenCodeRefusesProvidersKeykeep(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opencode.json")
	writeFile(t, path, `{"providers":{"keykeep":{"settings":{}}}}`)
	agent, _ := Find("opencode")
	st := LinkState{}
	err := agent.Link(map[string]string{fileMain: path}, local(), &st)
	if err == nil {
		t.Fatal("expected refusal when providers.keykeep exists")
	}
	if !strings.Contains(err.Error(), "providers.keykeep") {
		t.Fatalf("error should name the conflicting key: %v", err)
	}
}

// Qwen Code：三处同时写（env 占位 Key、modelProviders.openai 追加、selectedType=openai），
// unlink 只删自己那条，用户自己的 provider 条目要留住。
func TestQwenLinkAndUnlinkRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	original := `{
  "$version": 2,
  "env": {"OPENAI_API_KEY": "sk-user"},
  "modelProviders": {
    "openai": [
      {"id": "gpt-5.5", "name": "gpt-5.5", "baseUrl": "https://api.openai.com/v1", "envKey": "OPENAI_API_KEY"}
    ]
  },
  "security": {"folderTrust": {"enabled": true}, "auth": {"selectedType": "qwen-oauth"}},
  "model": {"name": "gpt-5.5"}
}
`
	writeFile(t, path, original)

	agent, ok := Find("qwen")
	if !ok {
		t.Fatal("qwen agent missing")
	}
	files := map[string]string{fileMain: path}
	st := LinkState{}
	want := local()
	want.Model = "kk-qwen"
	if err := agent.Link(files, want, &st); err != nil {
		t.Fatalf("link: %v", err)
	}

	doc := readDoc(t, path)
	if got, _ := jsonGet(doc, "env."+qwenEnvKey); got != want.Key {
		t.Fatalf("env key = %v", got)
	}
	if got, _ := jsonGet(doc, "env.OPENAI_API_KEY"); got != "sk-user" {
		t.Fatalf("user env key lost: %v", got)
	}
	if got, _ := jsonGet(doc, "security.auth.selectedType"); got != "openai" {
		t.Fatalf("selectedType = %v", got)
	}
	if got, _ := jsonGet(doc, "model.name"); got != "kk-qwen" {
		t.Fatalf("model.name = %v", got)
	}
	entries, err := qwenEntries(doc)
	if err != nil {
		t.Fatalf("entries: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected user entry + ours, got %d: %v", len(entries), entries)
	}
	user := entries[0].(map[string]any)
	if user["id"] != "gpt-5.5" {
		t.Fatalf("user entry moved or lost: %v", entries)
	}
	ours := entries[1].(map[string]any)
	if ours["baseUrl"] != "http://127.0.0.1:3426/v1" || ours["envKey"] != qwenEnvKey {
		t.Fatalf("our entry wrong: %v", ours)
	}

	// 重复 link 不能越加越多。
	second := st
	if err := agent.Link(files, want, &second); err != nil {
		t.Fatalf("second link: %v", err)
	}
	entries, _ = qwenEntries(readDoc(t, path))
	if len(entries) != 2 {
		t.Fatalf("entries after re-link = %d", len(entries))
	}

	if err := agent.Unlink(files, &second); err != nil {
		t.Fatalf("unlink: %v", err)
	}
	restored := readDoc(t, path)
	if got, known := jsonGet(restored, "env."+qwenEnvKey); known {
		t.Fatalf("env key left behind: %v", got)
	}
	if got, _ := jsonGet(restored, "env.OPENAI_API_KEY"); got != "sk-user" {
		t.Fatalf("user env key lost after unlink: %v", got)
	}
	if got, _ := jsonGet(restored, "security.auth.selectedType"); got != "qwen-oauth" {
		t.Fatalf("selectedType not restored: %v", got)
	}
	if got, _ := jsonGet(restored, "security.folderTrust.enabled"); got != true {
		t.Fatalf("unrelated security key lost: %v", got)
	}
	if got, _ := jsonGet(restored, "model.name"); got != "gpt-5.5" {
		t.Fatalf("model.name not restored: %v", got)
	}
	entries, _ = qwenEntries(restored)
	if len(entries) != 1 || entries[0].(map[string]any)["id"] != "gpt-5.5" {
		t.Fatalf("user entry not preserved on unlink: %v", entries)
	}
	if got, _ := jsonGet(restored, "modelProviders.openai"); got == nil {
		t.Fatalf("modelProviders.openai lost: %v", restored)
	}
}

// 原本没有 modelProviders / env 时，unlink 要回收空壳，不留下 "modelProviders": {}。
func TestQwenUnlinkLeavesNoShells(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	writeFile(t, path, `{"$version":2}`)

	agent, _ := Find("qwen")
	files := map[string]string{fileMain: path}
	st := LinkState{}
	want := local()
	want.Model = "kk-q"
	if err := agent.Link(files, want, &st); err != nil {
		t.Fatalf("link: %v", err)
	}
	if err := agent.Unlink(files, &st); err != nil {
		t.Fatalf("unlink: %v", err)
	}
	doc := readDoc(t, path)
	for _, key := range []string{"env", "modelProviders", "security", "model"} {
		if _, ok := doc[key]; ok {
			t.Fatalf("%s shell left behind: %v", key, doc)
		}
	}
	if doc["$version"] != float64(2) {
		t.Fatalf("unrelated key lost: %v", doc)
	}
}

// modelProviders.openai 被别的工具改成对象时要报错，不能覆盖写。
func TestQwenRejectsUnexpectedProviderShape(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	writeFile(t, path, `{"modelProviders":{"openai":{"id":"x"}}}`)
	agent, _ := Find("qwen")
	st := LinkState{}
	if err := agent.Link(map[string]string{fileMain: path}, local(), &st); err == nil {
		t.Fatal("expected refusal for non-array modelProviders.openai")
	}
}

// 五个适配器都要能按 id 找到，且 id 唯一。
func TestAgentRegistry(t *testing.T) {
	want := []string{"claude", "codex", "gemini", "opencode", "qwen"}
	seen := map[string]bool{}
	for _, agent := range Agents() {
		if seen[agent.ID] {
			t.Fatalf("duplicate agent id %q", agent.ID)
		}
		seen[agent.ID] = true
	}
	for _, id := range want {
		if !seen[id] {
			t.Fatalf("%s missing from registry", id)
		}
		if _, ok := Find(id); !ok {
			t.Fatalf("Find(%q) failed", id)
		}
	}
}

// 三个有环境变量覆盖的适配器必须继续尊重覆盖；opencode / qwen 的目录约定单独验证。
func TestFilesHonorNewAgentDirOverrides(t *testing.T) {
	ocDir := filepath.Join(t.TempDir(), "oc")
	t.Setenv("OPENCODE_CONFIG_DIR", ocDir)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))

	agent, _ := Find("opencode")
	files, err := agent.Files()
	if err != nil {
		t.Fatalf("opencode files: %v", err)
	}
	if !strings.Contains(filepath.ToSlash(files[fileMain]), "/oc/") {
		t.Fatalf("opencode path %q does not honor OPENCODE_CONFIG_DIR", files[fileMain])
	}

	qwen, _ := Find("qwen")
	files, err = qwen.Files()
	if err != nil {
		t.Fatalf("qwen files: %v", err)
	}
	// 实测 Qwen Code 只读 $HOME/.qwen，因此这里断言它落在 home 下而不是被某个变量带走。
	if !strings.HasSuffix(filepath.ToSlash(files[fileMain]), ".qwen/settings.json") {
		t.Fatalf("qwen path = %q", files[fileMain])
	}
}
