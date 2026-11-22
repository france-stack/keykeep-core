package agentcfg

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func local() Local {
	return Local{
		AnthropicBase: "http://127.0.0.1:3426",
		OpenAIBase:    "http://127.0.0.1:3426/v1",
		Key:           "keykeep-local",
		Model:         "",
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func claudeFiles(t *testing.T) map[string]string {
	t.Helper()
	return map[string]string{fileMain: filepath.Join(t.TempDir(), "settings.json")}
}

// Claude Code：只动 env 里两个键与顶层 model；用户其它设置必须原样保留。
// 关键点：不写 env.ANTHROPIC_MODEL（会压过 /model 里的选择），已有则清掉。
func TestClaudeLinkAndUnlinkRoundTrip(t *testing.T) {
	files := claudeFiles(t)
	path := files[fileMain]
	original := `{
  "model": "claude-opus-4-5",
  "env": {
    "FOO": "bar",
    "ANTHROPIC_BASE_URL": "https://api.anthropic.com",
    "ANTHROPIC_MODEL": "claude-sonnet-4-5"
  }
}
`
	writeFile(t, path, original)

	agent, ok := Find("claude")
	if !ok {
		t.Fatal("claude agent missing")
	}
	st := LinkState{}
	want := local()
	want.Model = "kk-sonnet"
	if err := agent.Link(files, want, &st); err != nil {
		t.Fatalf("link: %v", err)
	}

	doc := readDoc(t, path)
	env := doc["env"].(map[string]any)
	if env["ANTHROPIC_BASE_URL"] != "http://127.0.0.1:3426" {
		t.Fatalf("base url = %v", env["ANTHROPIC_BASE_URL"])
	}
	if env["ANTHROPIC_AUTH_TOKEN"] != "keykeep-local" {
		t.Fatalf("token = %v", env["ANTHROPIC_AUTH_TOKEN"])
	}
	if _, exists := env["ANTHROPIC_MODEL"]; exists {
		t.Fatalf("ANTHROPIC_MODEL must be removed (it outranks /model), got %v", env["ANTHROPIC_MODEL"])
	}
	if env["FOO"] != "bar" {
		t.Fatalf("unrelated env key lost: %v", env)
	}
	if doc["model"] != "kk-sonnet" {
		t.Fatalf("top-level model = %v, want kk-sonnet", doc["model"])
	}
	if _, err := os.Stat(path + ".keykeep.bak"); err != nil {
		t.Fatalf("backup missing: %v", err)
	}

	if err := agent.Unlink(files, &st); err != nil {
		t.Fatalf("unlink: %v", err)
	}
	restored := readDoc(t, path)
	env = restored["env"].(map[string]any)
	if env["ANTHROPIC_BASE_URL"] != "https://api.anthropic.com" {
		t.Fatalf("base url not restored: %v", env["ANTHROPIC_BASE_URL"])
	}
	if _, exists := env["ANTHROPIC_AUTH_TOKEN"]; exists {
		t.Fatalf("token should be removed, got %v", env["ANTHROPIC_AUTH_TOKEN"])
	}
	if env["ANTHROPIC_MODEL"] != "claude-sonnet-4-5" {
		t.Fatalf("ANTHROPIC_MODEL not restored: %v", env["ANTHROPIC_MODEL"])
	}
	if restored["model"] != "claude-opus-4-5" {
		t.Fatalf("model not restored: %v", restored["model"])
	}
	if env["FOO"] != "bar" {
		t.Fatalf("unrelated env key lost after unlink: %v", env)
	}
}

// 没有 model 时不能凭空造一个；原本没有 model 的，unlink 后也不能留下。
func TestClaudeLeavesModelAloneWhenUnset(t *testing.T) {
	files := claudeFiles(t)
	path := files[fileMain]
	writeFile(t, path, `{"env":{"FOO":"bar"}}`)

	agent, _ := Find("claude")
	st := LinkState{}
	if err := agent.Link(files, local(), &st); err != nil {
		t.Fatalf("link: %v", err)
	}
	if _, exists := readDoc(t, path)["model"]; exists {
		t.Fatal("model key invented without --model")
	}
	if err := agent.Unlink(files, &st); err != nil {
		t.Fatalf("unlink: %v", err)
	}
	doc := readDoc(t, path)
	if _, exists := doc["model"]; exists {
		t.Fatalf("model key left behind: %v", doc)
	}
	if doc["env"].(map[string]any)["FOO"] != "bar" {
		t.Fatalf("unrelated key lost: %v", doc)
	}
}

// 注释/尾逗号等非法 JSON 必须报错而不是覆盖写。
func TestClaudeRejectsInvalidJSON(t *testing.T) {
	files := claudeFiles(t)
	path := files[fileMain]
	writeFile(t, path, `{ "env": { "A": "b" } `)
	agent, _ := Find("claude")
	st := LinkState{}
	err := agent.Link(files, local(), &st)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), `"A": "b"`) {
		t.Fatalf("file was modified: %s", raw)
	}
}

// Codex：顶层 model_provider / model 写在所有表头之前，表块带标记，重复 link 不叠加。
func TestCodexLinkIsIdempotent(t *testing.T) {
	files := map[string]string{fileMain: filepath.Join(t.TempDir(), "config.toml")}
	path := files[fileMain]
	original := `model = "gpt-5"
approval_policy = "on-request"

[projects."/work"]
trust_level = "trusted"
`
	writeFile(t, path, original)

	agent, _ := Find("codex")
	st := LinkState{}
	if err := agent.Link(files, local(), &st); err != nil {
		t.Fatalf("link: %v", err)
	}
	first, _ := os.ReadFile(path)
	if strings.Count(string(first), "[model_providers.keykeep]") != 1 {
		t.Fatalf("provider table count = %d", strings.Count(string(first), "[model_providers.keykeep]"))
	}
	if !strings.Contains(string(first), `model_provider = "keykeep"`) {
		t.Fatalf("missing model_provider: %s", first)
	}
	// 顶层键必须在 [projects…] 之前。
	idxTop := strings.Index(string(first), `model_provider = "keykeep"`)
	idxTable := strings.Index(string(first), "[projects.")
	if idxTop < 0 || idxTable < 0 || idxTop > idxTable {
		t.Fatalf("top-level key not before tables:\n%s", first)
	}

	// CLI 第二次 link 会从状态文件读回同一份快照（不新建），这里照此模拟。
	second := st
	if err := agent.Link(files, local(), &second); err != nil {
		t.Fatalf("second link: %v", err)
	}
	after, _ := os.ReadFile(path)
	if strings.Count(string(after), "[model_providers.keykeep]") != 1 {
		t.Fatalf("duplicate block after second link:\n%s", after)
	}
	if !strings.Contains(string(after), "[projects.") {
		t.Fatalf("user table lost:\n%s", after)
	}

	if err := agent.Unlink(files, &second); err != nil {
		t.Fatalf("unlink: %v", err)
	}
	restored, _ := os.ReadFile(path)
	if strings.Contains(string(restored), "keykeep") {
		t.Fatalf("keykeep block left behind:\n%s", restored)
	}
	if !strings.Contains(string(restored), `approval_policy = "on-request"`) {
		t.Fatalf("user keys lost:\n%s", restored)
	}
	if !strings.Contains(string(restored), `model = "gpt-5"`) {
		t.Fatalf("original model line lost:\n%s", restored)
	}
}

func TestCodexRefusesExistingUserTable(t *testing.T) {
	files := map[string]string{fileMain: filepath.Join(t.TempDir(), "config.toml")}
	writeFile(t, files[fileMain], "[model_providers.keykeep]\nname = \"mine\"\n")
	agent, _ := Find("codex")
	st := LinkState{}
	if err := agent.Link(files, local(), &st); err == nil {
		t.Fatal("expected refusal")
	}
}

// Gemini CLI：两个文件都要写——settings.json 切认证方式，.env 给地址与 Key；
// unlink 两边都还原。
func TestGeminiWritesBothFilesAndRestores(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		fileMain: filepath.Join(dir, "settings.json"),
		fileEnv:  filepath.Join(dir, ".env"),
	}
	writeFile(t, files[fileMain], `{
  "theme": "dark",
  "security": {"auth": {"selectedType": "oauth-personal"}},
  "model": {"name": "gemini-2.5-pro"}
}
`)
	writeFile(t, files[fileEnv], "GEMINI_API_KEY=old-key\nOTHER=keep\n")

	agent, _ := Find("gemini")
	st := LinkState{}
	want := local()
	want.Model = "kk-gemini"
	if err := agent.Link(files, want, &st); err != nil {
		t.Fatalf("link: %v", err)
	}

	settings := readDoc(t, files[fileMain])
	if got, _ := jsonGet(settings, "security.auth.selectedType"); got != "gemini-api-key" {
		t.Fatalf("selectedType = %v", got)
	}
	if got, _ := jsonGet(settings, "model.name"); got != "kk-gemini" {
		t.Fatalf("model.name = %v", got)
	}
	if settings["theme"] != "dark" {
		t.Fatalf("unrelated key lost: %v", settings)
	}

	raw, _ := os.ReadFile(files[fileEnv])
	if !strings.Contains(string(raw), "GEMINI_API_KEY=keykeep-local") {
		t.Fatalf("key not written: %s", raw)
	}
	if !strings.Contains(string(raw), "GOOGLE_GEMINI_BASE_URL=http://127.0.0.1:3426") {
		t.Fatalf("base url not written: %s", raw)
	}
	if !strings.Contains(string(raw), "OTHER=keep") {
		t.Fatalf("unrelated line lost: %s", raw)
	}

	if err := agent.Unlink(files, &st); err != nil {
		t.Fatalf("unlink: %v", err)
	}
	settings = readDoc(t, files[fileMain])
	if got, _ := jsonGet(settings, "security.auth.selectedType"); got != "oauth-personal" {
		t.Fatalf("selectedType not restored: %v", got)
	}
	if got, _ := jsonGet(settings, "model.name"); got != "gemini-2.5-pro" {
		t.Fatalf("model.name not restored: %v", got)
	}
	if _, exists := jsonGet(settings, "security.auth"); !exists {
		t.Fatalf("user's own auth block must survive: %v", settings)
	}
	restored, _ := os.ReadFile(files[fileEnv])
	if !strings.Contains(string(restored), "GEMINI_API_KEY=old-key") {
		t.Fatalf("key not restored: %s", restored)
	}
	if strings.Contains(string(restored), "GOOGLE_GEMINI_BASE_URL") {
		t.Fatalf("base url not removed: %s", restored)
	}
}

// settings.json 原本不存在时，link 建出来、unlink 要删掉，不能留一个空壳。
func TestGeminiCreatesAndRemovesSettings(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		fileMain: filepath.Join(dir, "settings.json"),
		fileEnv:  filepath.Join(dir, ".env"),
	}
	agent, _ := Find("gemini")
	st := LinkState{}
	if err := agent.Link(files, local(), &st); err != nil {
		t.Fatalf("link: %v", err)
	}
	if _, err := os.Stat(files[fileMain]); err != nil {
		t.Fatalf("settings.json not created: %v", err)
	}
	if err := agent.Unlink(files, &st); err != nil {
		t.Fatalf("unlink: %v", err)
	}
	doc := readDoc(t, files[fileMain])
	if _, exists := jsonGet(doc, "security.auth.selectedType"); exists {
		t.Fatalf("selectedType left behind: %v", doc)
	}
	// 建出来的父对象也要回收，不能留下 "security": {"auth": {}} 这种空壳。
	if _, exists := jsonGet(doc, "security"); exists {
		t.Fatalf("empty security shell left behind: %v", doc)
	}
}

func TestFindIsCaseInsensitive(t *testing.T) {
	if _, ok := Find("CLAUDE"); !ok {
		t.Fatal("Find should be case-insensitive")
	}
	if _, ok := Find("nope"); ok {
		t.Fatal("unknown agent should not resolve")
	}
}

// CLAUDE_CONFIG_DIR / GEMINI_CLI_HOME 必须被尊重（Claude Code 与 Gemini CLI 自己的约定）。
func TestFilesHonorConfigDirOverrides(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(t.TempDir(), "claude-cfg"))
	t.Setenv("CODEX_HOME", filepath.Join(t.TempDir(), "codex-home"))
	t.Setenv("GEMINI_CLI_HOME", filepath.Join(t.TempDir(), "gemini-home"))

	cases := map[string]string{
		"claude": "claude-cfg",
		"codex":  "codex-home",
		"gemini": "gemini-home",
	}
	for id, want := range cases {
		agent, ok := Find(id)
		if !ok {
			t.Fatalf("%s missing", id)
		}
		files, err := agent.Files()
		if err != nil {
			t.Fatalf("%s files: %v", id, err)
		}
		for _, path := range files {
			if !strings.Contains(filepath.ToSlash(path), want) {
				t.Fatalf("%s path %q does not honor override %q", id, path, want)
			}
		}
	}
}

func readDoc(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, raw)
	}
	return doc
}
