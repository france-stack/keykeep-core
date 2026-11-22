// Package agentcfg 把本机 Agent 的配置指向 KeyKeep 连接器，并在解除时原样还原。
//
// 设计约束（参照 magpie 的 agent 配置写入，MIT，仅参考设计未复制代码）：
//   - 只改必要的键，用户其余配置不动；
//   - 写入前记录每个被改键的原值（含「原本不存在」），解除时逐键还原；
//   - 首次修改前留一份 .keykeep.bak，便于人工比对；
//   - 解析失败一律报错退出，绝不在读不懂的配置上覆盖写。
//
// 两条从 magpie 源码核对的硬事实（2026-11-06）：
//   - Claude Code 的主模型写在 settings.json 顶层的 model；写 env.ANTHROPIC_MODEL
//     会压过 /model 里的选择，必须不写。
//   - Gemini CLI 走 API Key 需要两处：~/.gemini/.env 的 GOOGLE_GEMINI_BASE_URL +
//     GEMINI_API_KEY，以及 settings.json 的 security.auth.selectedType =
//     "gemini-api-key"；只写 .env 不会切认证方式。
package agentcfg

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Local 是连接器对 Agent 暴露的端点信息。
type Local struct {
	// AnthropicBase 形如 http://127.0.0.1:3426（不带 /v1）。
	AnthropicBase string
	// OpenAIBase 形如 http://127.0.0.1:3426/v1。
	OpenAIBase string
	// Key 是写进 Agent 配置的占位 Key，连接器转发时替换为真实虚拟 Key。
	Key string
	// Model 是默认模型名（来自 /v1/models）；为空则不写模型键。
	Model string
}

// Prev 记录一个键在本工具改写之前的原貌。
type Prev struct {
	// Present=false 表示原本没有这个键，解除时删除它。
	Present bool   `json:"present"`
	Value   string `json:"value,omitempty"`
}

// 状态文件里的逻辑文件名。一个 Agent 可能改多个文件（如 Gemini 的 .env 与 settings.json）。
const (
	fileMain = "main" // Claude settings.json / Codex config.toml / Gemini settings.json
	fileEnv  = "env"  // Gemini .env
)

// FileState 是单个文件被改写前的快照。
type FileState struct {
	Path string `json:"path"`
	// Prev 的键是「命名空间.字段」，例如 env.ANTHROPIC_BASE_URL、security.auth.selectedType。
	Prev map[string]Prev `json:"prev"`
	// Injected 是本工具写进 Codex 顶层的整行原文；重复 link 时先按原文删掉，
	// 免得把自己上次写的内容当成用户原值记进 Prev。
	Injected []string `json:"injected,omitempty"`
}

// LinkState 是某个 Agent 被改写前的全部文件快照。
type LinkState struct {
	Files map[string]FileState `json:"files"`
}

// Paths 返回该 Agent 被改过的文件路径（按逻辑名排序，输出稳定）。
func (s LinkState) Paths() []string {
	out := make([]string, 0, len(s.Files))
	for _, fs := range s.Files {
		if fs.Path != "" {
			out = append(out, fs.Path)
		}
	}
	sort.Strings(out)
	return out
}

func (s *LinkState) file(logical, path string) *FileState {
	if s.Files == nil {
		s.Files = map[string]FileState{}
	}
	fs, ok := s.Files[logical]
	if !ok {
		fs = FileState{Prev: map[string]Prev{}}
	}
	if fs.Path == "" {
		fs.Path = path
	}
	if fs.Prev == nil {
		fs.Prev = map[string]Prev{}
	}
	return &fs
}

func (s *LinkState) commit(logical string, fs *FileState) {
	if s.Files == nil {
		s.Files = map[string]FileState{}
	}
	s.Files[logical] = *fs
}

// State 是连接器的本地状态，落在 ~/.keykeep/state.json。
type State struct {
	Linked map[string]LinkState `json:"linked"`
}

// Agent 描述一个可一键接入的客户端。
type Agent struct {
	ID   string
	Name string
	// Files 返回该客户端在**本机**的配置文件（逻辑名 → 绝对路径）。
	Files func() (map[string]string, error)
	// Link 把配置指向 local，并把原值写入 st。
	Link func(files map[string]string, local Local, st *LinkState) error
	// Unlink 按 st 里记录的原值还原。
	Unlink func(files map[string]string, st *LinkState) error
}

// Agents 返回内置支持列表；顺序即展示顺序。
func Agents() []Agent {
	return []Agent{claudeAgent(), codexAgent(), geminiAgent(), openCodeAgent(), qwenAgent()}
}

// Find 按 id 查 Agent（大小写不敏感）。
func Find(id string) (Agent, bool) {
	for _, a := range Agents() {
		if strings.EqualFold(a.ID, id) {
			return a, true
		}
	}
	return Agent{}, false
}

// StatePath 是连接器状态的落盘位置。
func StatePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".keykeep", "state.json"), nil
}

// LoadState 读取状态；文件不存在返回空状态。
func LoadState() (*State, error) {
	path, err := StatePath()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &State{Linked: map[string]LinkState{}}, nil
	}
	if err != nil {
		return nil, err
	}
	var st State
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, fmt.Errorf("状态文件损坏（%s）：%w", path, err)
	}
	if st.Linked == nil {
		st.Linked = map[string]LinkState{}
	}
	return &st, nil
}

// SaveState 原子写回状态文件（0600：里面只有路径与旧值，但仍是本机配置）。
func SaveState(st *State) error {
	path, err := StatePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "state-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	// 先压盘再改名：崩溃/断电后 rename 不能指向半截文件。
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// ---------------------------------------------------------------- Claude Code

func claudeAgent() Agent {
	return Agent{
		ID:   "claude",
		Name: "Claude Code",
		Files: func() (map[string]string, error) {
			return map[string]string{fileMain: filepath.Join(claudeDir(), "settings.json")}, nil
		},
		Link: func(files map[string]string, local Local, st *LinkState) error {
			path := files[fileMain]
			doc, err := readJSONObject(path)
			if err != nil {
				return err
			}
			env, err := jsonObjectAt(doc, "env")
			if err != nil {
				return err
			}
			fs := st.file(fileMain, path)

			want := map[string]string{
				"ANTHROPIC_BASE_URL":   local.AnthropicBase,
				"ANTHROPIC_AUTH_TOKEN": local.Key,
			}
			// ANTHROPIC_MODEL 会压过 /model 里的选择；主模型只写顶层 model。
			if _, stray := env["ANTHROPIC_MODEL"]; stray {
				recordPrev(fs, "env.ANTHROPIC_MODEL", env["ANTHROPIC_MODEL"])
				delete(env, "ANTHROPIC_MODEL")
			}
			for key, value := range want {
				recordPrev(fs, "env."+key, env[key])
				env[key] = value
			}
			doc["env"] = env
			if local.Model != "" {
				recordPrev(fs, "model", doc["model"])
				doc["model"] = local.Model
			}
			st.commit(fileMain, fs)
			return writeJSONObject(path, doc)
		},
		Unlink: func(files map[string]string, st *LinkState) error {
			fs, ok := st.Files[fileMain]
			if !ok {
				return nil
			}
			path := pickPath(fs.Path, files[fileMain])
			doc, err := readJSONObject(path)
			if err != nil {
				return err
			}
			env, err := jsonObjectAt(doc, "env")
			if err != nil {
				return err
			}
			restore := func(key, prevKey string) {
				p, known := fs.Prev[prevKey]
				if !known {
					return
				}
				if p.Present {
					env[key] = p.Value
				} else {
					delete(env, key)
				}
			}
			restore("ANTHROPIC_BASE_URL", "env.ANTHROPIC_BASE_URL")
			restore("ANTHROPIC_AUTH_TOKEN", "env.ANTHROPIC_AUTH_TOKEN")
			restore("ANTHROPIC_MODEL", "env.ANTHROPIC_MODEL")
			doc["env"] = env
			restoreTop(doc, fs, "model")
			return writeJSONObject(path, doc)
		},
	}
}

// claudeDir 支持 CLAUDE_CONFIG_DIR 覆盖（Claude Code 自己的约定）。
func claudeDir() string {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude")
}

// ---------------------------------------------------------------- Codex

const codexMarker = "# >>> keykeep connector (kkconnect link 写入；kkconnect unlink 移除)"

func codexAgent() Agent {
	return Agent{
		ID:   "codex",
		Name: "Codex CLI",
		Files: func() (map[string]string, error) {
			dir := os.Getenv("CODEX_HOME")
			if dir == "" {
				home, err := os.UserHomeDir()
				if err != nil {
					return nil, err
				}
				dir = filepath.Join(home, ".codex")
			}
			return map[string]string{fileMain: filepath.Join(dir, "config.toml")}, nil
		},
		Link: func(files map[string]string, local Local, st *LinkState) error {
			path := files[fileMain]
			raw, err := readText(path)
			if err != nil {
				return err
			}
			fs := st.file(fileMain, path)
			clean := stripCodexBlock(raw)
			// 上次 link 留下的顶层键也要先清掉，否则会被当成「用户原值」记进 Prev，
			// unlink 后反而把 keykeep 写了回去。
			clean = dropLines(clean, fs.Injected)
			// 状态文件丢了也要幂等：指向不存在 provider 的 model_provider = "keykeep"
			// 本身就是坏配置，直接当残留清掉。
			clean = dropLines(clean, []string{`model_provider = "keykeep"`})
			if strings.Contains(clean, "[model_providers.keykeep]") {
				// 用户手写过同名表：不覆盖，让用户自己决定。
				return fmt.Errorf("%s 里已有 [model_providers.keykeep]，请先手动删除或改名", path)
			}
			lines := strings.Split(clean, "\n")
			topEnd := codexTopEnd(lines)
			// 顶层键必须写在任何表头之前，否则会被解析成该表的键。
			var kept []string
			removed := map[string]string{}
			for i, line := range lines {
				trim := strings.TrimSpace(line)
				if i < topEnd {
					key, hasValue := codexTopKey(trim)
					if hasValue && (key == "model_provider" || key == "model") {
						removed[key] = line
						continue
					}
				}
				kept = append(kept, line)
			}
			lines = kept
			topEnd = codexTopEnd(lines)

			for _, key := range []string{"model_provider", "model"} {
				if _, known := fs.Prev[key]; known {
					continue // 重复 link：保留第一次记下的用户原值
				}
				if line, ok := removed[key]; ok {
					fs.Prev[key] = Prev{Present: true, Value: line}
				} else {
					fs.Prev[key] = Prev{Present: false}
				}
			}

			head := append([]string{}, lines[:topEnd]...)
			tail := lines[topEnd:]
			injected := []string{`model_provider = "keykeep"`}
			head = append(head, injected...)
			if local.Model != "" {
				modelLine := fmt.Sprintf("model = %q", local.Model)
				head = append(head, modelLine)
				injected = append(injected, modelLine)
			}
			block := []string{
				"",
				codexMarker,
				"[model_providers.keykeep]",
				`name = "KeyKeep"`,
				fmt.Sprintf("base_url = %q", local.OpenAIBase),
				`wire_api = "responses"`,
				fmt.Sprintf("experimental_bearer_token = %q", local.Key),
				"# <<< keykeep connector",
				"",
			}
			fs.Injected = injected
			st.commit(fileMain, fs)
			return writeText(path, strings.Join(append(append(head, block...), tail...), "\n"))
		},
		Unlink: func(files map[string]string, st *LinkState) error {
			fs, ok := st.Files[fileMain]
			if !ok {
				return nil
			}
			path := pickPath(fs.Path, files[fileMain])
			raw, err := readText(path)
			if err != nil {
				return err
			}
			out := dropLines(stripCodexBlock(raw), fs.Injected)
			lines := strings.Split(out, "\n")
			topEnd := codexTopEnd(lines)
			insert := make([]string, 0, 2)
			for _, key := range []string{"model_provider", "model"} {
				if p, known := fs.Prev[key]; known && p.Present {
					insert = append(insert, p.Value)
				}
			}
			if len(insert) > 0 {
				lines = append(append(append([]string{}, lines[:topEnd]...), insert...), lines[topEnd:]...)
			}
			return writeText(path, strings.Join(lines, "\n"))
		},
	}
}

// codexTopEnd 返回第一个表头之前的位置（即顶层键的结尾）。
func codexTopEnd(lines []string) int {
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "[") {
			return i
		}
	}
	return len(lines)
}

// codexTopKey 解析一行 "key = value"；不是顶层键行时 ok=false。
func codexTopKey(trim string) (string, bool) {
	if trim == "" || strings.HasPrefix(trim, "#") {
		return "", false
	}
	key, _, found := strings.Cut(trim, "=")
	if !found {
		return "", false
	}
	return strings.TrimSpace(key), true
}

// dropLines 从文本里删掉与给定行完全相同的行（各删一次）；为空则原样返回。
func dropLines(raw string, targets []string) string {
	if len(targets) == 0 {
		return raw
	}
	pending := make(map[string]int, len(targets))
	for _, line := range targets {
		pending[line]++
	}
	lines := strings.Split(raw, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if pending[line] > 0 {
			pending[line]--
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// stripCodexBlock 移除本工具写入的标记块（幂等：没有块时原样返回）。
func stripCodexBlock(raw string) string {
	start := strings.Index(raw, codexMarker)
	if start < 0 {
		return raw
	}
	end := strings.Index(raw[start:], "# <<< keykeep connector")
	if end < 0 {
		return raw
	}
	end = start + end + len("# <<< keykeep connector")
	head := strings.TrimRight(raw[:start], "\n")
	tail := strings.TrimLeft(raw[end:], "\n")
	if head == "" {
		return tail
	}
	if tail == "" {
		return head + "\n"
	}
	return head + "\n\n" + tail
}

// ---------------------------------------------------------------- Gemini CLI

func geminiAgent() Agent {
	return Agent{
		ID:   "gemini",
		Name: "Gemini CLI",
		Files: func() (map[string]string, error) {
			return map[string]string{
				fileMain: filepath.Join(geminiDir(), "settings.json"),
				fileEnv:  filepath.Join(geminiDir(), ".env"),
			}, nil
		},
		Link: func(files map[string]string, local Local, st *LinkState) error {
			// 1) settings.json：把认证方式切成 API Key（否则 CLI 仍走登录态）。
			settingsPath := files[fileMain]
			rawSettings, _ := os.ReadFile(settingsPath)
			doc, err := readJSONObject(settingsPath)
			if err != nil {
				return err
			}
			fs := st.file(fileMain, settingsPath)
			// 记原值用 jsonGet，写用 jsonSet（父对象不存在就建）。
			if value, ok := jsonGet(doc, "security.auth.selectedType"); ok {
				recordPrev(fs, "security.auth.selectedType", value)
			} else {
				recordPrev(fs, "security.auth.selectedType", nil)
			}
			if err := jsonSet(doc, "security.auth.selectedType", "gemini-api-key"); err != nil {
				return err
			}
			if local.Model != "" {
				value, _ := jsonGet(doc, "model.name")
				recordPrev(fs, "model.name", value)
				if err := jsonSet(doc, "model.name", local.Model); err != nil {
					return err
				}
			}
			st.commit(fileMain, fs)
			if err := writeJSONObject(settingsPath, doc); err != nil {
				return err
			}

			// 2) .env：网关地址与占位 Key。
			envPath := files[fileEnv]
			raw, err := readText(envPath)
			if err != nil {
				return err
			}
			envFS := st.file(fileEnv, envPath)
			lines, prevs := setEnvFile(raw, map[string]string{
				"GOOGLE_GEMINI_BASE_URL": local.AnthropicBase,
				"GEMINI_API_KEY":         local.Key,
			})
			for key, p := range prevs {
				if _, known := envFS.Prev[key]; known {
					continue // 重复 link：当前值是我们上次写的，不能覆盖原值
				}
				envFS.Prev[key] = p
			}
			st.commit(fileEnv, envFS)
			if err := writeText(envPath, strings.Join(lines, "\n")); err != nil {
				// settings 已写、.env 写失败：把 settings 还原，避免留下半套配置。
				if rawSettings == nil {
					_ = os.Remove(settingsPath)
				} else {
					_ = writeText(settingsPath, string(rawSettings))
				}
				return err
			}
			return nil
		},
		Unlink: func(files map[string]string, st *LinkState) error {
			if fs, ok := st.Files[fileMain]; ok {
				path := pickPath(fs.Path, files[fileMain])
				doc, err := readJSONObject(path)
				if err != nil {
					return err
				}
				restoreJSON(doc, fs, "security.auth.selectedType")
				restoreJSON(doc, fs, "model.name")
				if err := writeJSONObject(path, doc); err != nil {
					return err
				}
			}
			if fs, ok := st.Files[fileEnv]; ok {
				path := pickPath(fs.Path, files[fileEnv])
				raw, err := readText(path)
				if err != nil {
					return err
				}
				env, err := restoreEnvFile(raw, fs)
				if err != nil {
					return err
				}
				if err := writeText(path, env); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

// geminiDir 支持 GEMINI_CLI_HOME 覆盖。
func geminiDir() string {
	if dir := os.Getenv("GEMINI_CLI_HOME"); dir != "" {
		return dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".gemini")
}

// setEnvFile 改写 KEY=VALUE 形式的 .env 行，返回新行与每个键的原值。
func setEnvFile(raw string, values map[string]string) ([]string, map[string]Prev) {
	lines := strings.Split(raw, "\n")
	prevs := make(map[string]Prev, len(values))
	seen := map[string]bool{}
	out := make([]string, 0, len(lines)+len(values))
	for _, line := range lines {
		trim := strings.TrimSpace(line)
		key, _, ok := strings.Cut(trim, "=")
		key = strings.TrimSpace(key)
		if ok {
			if want, has := values[key]; has {
				if seen[key] {
					continue
				}
				prevs[key] = Prev{Present: true, Value: trim}
				seen[key] = true
				out = append(out, key+"="+want)
				continue
			}
		}
		out = append(out, line)
	}
	// 新键插在末尾换行之前，避免在文件尾部留下一个空行再接新键。
	tail := ""
	if n := len(out); n > 0 && out[n-1] == "" {
		out = out[:n-1]
		tail = "\n"
	}
	missing := make([]string, 0, len(values))
	for key := range values {
		if !seen[key] {
			prevs[key] = Prev{Present: false}
			missing = append(missing, key)
		}
	}
	sort.Strings(missing)
	for _, key := range missing {
		out = append(out, key+"="+values[key])
	}
	if tail != "" || len(missing) > 0 {
		out = append(out, "")
	}
	return out, prevs
}

// restoreEnvFile 按快照还原 .env：原本有的写回原行，原本没有的删掉。
func restoreEnvFile(raw string, fs FileState) (string, error) {
	lines := strings.Split(raw, "\n")
	remove := map[string]bool{}
	restore := map[string]string{}
	for key, p := range fs.Prev {
		if !strings.Contains(key, ".") && !strings.Contains(key, " ") { // .env 的键不带命名空间
			if p.Present {
				restore[key] = p.Value
			} else {
				remove[key] = true
			}
		}
	}
	out := make([]string, 0, len(lines))
	done := map[string]bool{}
	for _, line := range lines {
		trim := strings.TrimSpace(line)
		key, _, ok := strings.Cut(trim, "=")
		key = strings.TrimSpace(key)
		if ok && remove[key] {
			continue
		}
		if ok {
			if original, has := restore[key]; has {
				out = append(out, original)
				done[key] = true
				continue
			}
		}
		out = append(out, line)
	}
	for key, original := range restore {
		if !done[key] {
			out = append(out, original)
		}
	}
	return strings.Join(out, "\n"), nil
}

// ---------------------------------------------------------------- JSON helpers

func readJSONObject(path string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, err
		}
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return map[string]any{}, nil
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(trimmed), &doc); err != nil {
		return nil, fmt.Errorf("%s 不是合法 JSON（%v）：为避免覆盖你的配置，请手动修改或先修复它", path, err)
	}
	if doc == nil {
		doc = map[string]any{}
	}
	return doc, nil
}

func writeJSONObject(path string, doc map[string]any) error {
	if err := backup(path); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return writeText(path, string(raw)+"\n")
}

// jsonObjectAt 取 object[field] 为对象；不存在则新建，类型不符则报错。
func jsonObjectAt(doc map[string]any, field string) (map[string]any, error) {
	value, ok := doc[field]
	if !ok || value == nil {
		created := map[string]any{}
		doc[field] = created
		return created, nil
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%q 不是对象（实际是 %T），请手动修改后再运行", field, value)
	}
	return object, nil
}

// jsonGet 按点分路径取值。
func jsonGet(doc map[string]any, path string) (any, bool) {
	parts := strings.Split(path, ".")
	var current any = doc
	for _, part := range parts {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = object[part]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

// jsonSet 按点分路径写值，缺失的父对象自动创建。
func jsonSet(doc map[string]any, path string, value any) error {
	parts := strings.Split(path, ".")
	current := doc
	for _, part := range parts[:len(parts)-1] {
		next, ok := current[part]
		if !ok || next == nil {
			created := map[string]any{}
			current[part] = created
			current = created
			continue
		}
		object, ok := next.(map[string]any)
		if !ok {
			return fmt.Errorf("%q 不是对象（实际是 %T），请手动修改后再运行", part, next)
		}
		current = object
	}
	current[parts[len(parts)-1]] = value
	return nil
}

// jsonDel 按点分路径删键；父对象不存在时什么也不做。
// 删完顺带回收因删除而变空的父对象——否则 unlink 会留下 `"model": {}` 这种空壳，
// 对 CLI 来说「配置存在但为空」比「配置不存在」更难判断。
func jsonDel(doc map[string]any, path string) {
	parts := strings.Split(path, ".")
	chain := make([]map[string]any, 0, len(parts)-1)
	current := doc
	for _, part := range parts[:len(parts)-1] {
		next, ok := current[part].(map[string]any)
		if !ok {
			return
		}
		chain = append(chain, current)
		current = next
	}
	delete(current, parts[len(parts)-1])
	for i := len(chain) - 1; i >= 0; i-- {
		parent := chain[i]
		child, ok := parent[parts[i]].(map[string]any)
		if !ok || len(child) > 0 {
			return
		}
		delete(parent, parts[i])
	}
}

// restoreJSON 按 Prev 还原一个点分路径；原本不存在则删除。
func restoreJSON(doc map[string]any, fs FileState, path string) {
	p, known := fs.Prev[path]
	if !known {
		return
	}
	if p.Present {
		_ = jsonSet(doc, path, p.Value)
		return
	}
	jsonDel(doc, path)
}

// restoreTop 还原 JSON 顶层字符串键（值只可能是字符串）。
func restoreTop(doc map[string]any, fs FileState, key string) {
	restoreJSON(doc, fs, key)
}

// recordPrev 记录某个键的原值；value 为 nil 表示原本不存在。
func recordPrev(fs *FileState, key string, value any) {
	if _, done := fs.Prev[key]; done {
		return
	}
	if value == nil {
		fs.Prev[key] = Prev{Present: false}
		return
	}
	text, ok := value.(string)
	if !ok {
		text = fmt.Sprint(value)
	}
	fs.Prev[key] = Prev{Present: true, Value: text}
}

func pickPath(recorded, current string) string {
	if recorded != "" {
		return recorded
	}
	return current
}

func readText(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return "", err
		}
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func writeText(path, content string) error {
	if err := backup(path); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".keykeep-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		return err
	}
	// 先压盘再改名；Go 的 rename 在 Windows 上可直接覆盖，不需要先删原文件。
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// backup 在首次修改前留一份副本；已存在则不覆盖（保留最初状态）。
func backup(path string) error {
	bak := path + ".keykeep.bak"
	if _, err := os.Stat(bak); err == nil {
		return nil
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return os.WriteFile(bak, raw, 0o600)
}
