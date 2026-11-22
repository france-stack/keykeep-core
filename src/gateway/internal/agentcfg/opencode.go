// OpenCode 的配置写入。
//
// 真相来源（2026-11-06 对照 magpie v0.1.3019 的 internal/agent/agents.go 与
// opencode_providers.go 核对，并读本机 ~/.config/opencode/opencode.json 验证形状）：
//
//   - 自定义供应商放在顶层 provider.<id>：{npm, name, options:{baseURL, apiKey}, models}；
//     OpenCode 2 也读同一份 provider，另有一套 providers.<id>.settings 写法，但两边同名时
//     以 providers 为准——所以只要用户自己写过 providers.keykeep，我们就拒绝写，
//     免得写了却被另一份盖住（magpie 也是这么处理的：同名的属于用户）。
//   - baseURL 必须带 /v1（OpenAI 兼容形状），不是 Anthropic 那种根地址。
//   - 顶层 model 的取值是 "provider/model"（如 keykeep/mock-openclaw），
//     不是裸模型名；写成裸名 OpenCode 会当成没配。
//   - 配置文件优先 opencode.jsonc，其次 opencode.json，再次 config.json；都没有时新建
//     opencode.json。JSONC 里的注释本工具无法原样保留（读取后按纯 JSON 重写），
//     所以遇到注释或尾逗号直接报错，让用户先去掉，而不是悄悄把注释抹掉。
package agentcfg

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// openCodeProvider 是本工具写入的供应商 id；与用户自己的同名条目冲突时拒绝写。
const openCodeProvider = "keykeep"

func openCodeAgent() Agent {
	return Agent{
		ID:   "opencode",
		Name: "OpenCode",
		Files: func() (map[string]string, error) {
			dir := openCodeDir()
			path := filepath.Join(dir, "opencode.json")
			for _, name := range []string{"opencode.jsonc", "opencode.json", "config.json"} {
				candidate := filepath.Join(dir, name)
				if _, err := os.Stat(candidate); err == nil {
					path = candidate
					break
				}
			}
			return map[string]string{fileMain: path}, nil
		},
		Link: func(files map[string]string, local Local, st *LinkState) error {
			path := files[fileMain]
			doc, err := readOpenCodeJSON(path)
			if err != nil {
				return err
			}
			// OpenCode 2 的同名 providers 条目会整份压过 provider，写了也白写。
			if _, taken := jsonGet(doc, "providers."+openCodeProvider); taken {
				return fmt.Errorf("%s 里已有 providers.%s，OpenCode 会用它盖过本工具写入的 provider.%s；请先改掉那个名字或删掉它", path, openCodeProvider, openCodeProvider)
			}
			var ours map[string]any
			if existing, ok := jsonGet(doc, "provider."+openCodeProvider); ok {
				if m, isObj := existing.(map[string]any); isObj &&
					m["npm"] != "@ai-sdk/openai-compatible" && m["npm"] != "" {
					return fmt.Errorf("%s 里已有 provider.%s（npm=%v），不是本工具写的；请先改名或删除", path, openCodeProvider, m["npm"])
				}
				ours, _ = existing.(map[string]any)
			}
			if ours == nil {
				ours = map[string]any{}
			}

			fs := st.file(fileMain, path)
			// 整块记一次原貌：第一次 link 记「原本没有」，重复 link 保留第一次的记录。
			if _, known := fs.Prev["provider."+openCodeProvider]; !known {
				fs.Prev["provider."+openCodeProvider] = prevOfJSON(ours)
			}

			options, err := jsonObjectAt(ours, "options")
			if err != nil {
				return err
			}
			options["baseURL"] = local.OpenAIBase
			options["apiKey"] = local.Key
			ours["npm"] = "@ai-sdk/openai-compatible"
			ours["name"] = "KeyKeep"
			if local.Model != "" {
				models, err := jsonObjectAt(ours, "models")
				if err != nil {
					return err
				}
				entry, ok := models[local.Model].(map[string]any)
				if !ok {
					entry = map[string]any{}
				}
				entry["name"] = local.Model
				models[local.Model] = entry
				ours["models"] = models
			}
			if err := jsonSet(doc, "provider."+openCodeProvider, ours); err != nil {
				return err
			}

			if local.Model != "" {
				value, _ := jsonGet(doc, "model")
				recordPrev(fs, "model", value)
				if err := jsonSet(doc, "model", openCodeProvider+"/"+local.Model); err != nil {
					return err
				}
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
			doc, err := readOpenCodeJSON(path)
			if err != nil {
				return err
			}
			// 原样还原 provider.keykeep：之前没有就删，之前有（同 npm 的旧配置）就写回去。
			if p, known := fs.Prev["provider."+openCodeProvider]; known && p.Present {
				var original map[string]any
				if err := json.Unmarshal([]byte(p.Value), &original); err == nil {
					_ = jsonSet(doc, "provider."+openCodeProvider, original)
				}
			} else {
				jsonDel(doc, "provider."+openCodeProvider)
			}
			restoreJSON(doc, fs, "model")
			return writeJSONObject(path, doc)
		},
	}
}

// readOpenCodeJSON 读 OpenCode 配置；含注释/尾逗号（JSONC）时给出可操作的中文错误。
func readOpenCodeJSON(path string) (map[string]any, error) {
	doc, err := readJSONObject(path)
	if err == nil {
		return doc, nil
	}
	raw, readErr := os.ReadFile(path)
	if readErr != nil {
		return nil, err
	}
	if looksLikeJSONC(string(raw)) {
		return nil, fmt.Errorf(
			"%s 是带注释或尾逗号的 JSONC。本工具读取后会按纯 JSON 重写，注释会丢失，"+
				"所以不做偷偷修改：请先手动去掉注释（OpenCode 自己也能读纯 JSON），再运行一次",
			path)
	}
	return nil, err
}

// looksLikeJSONC 判断解析失败是不是「JSONC 特有语法」造成的。
func looksLikeJSONC(raw string) bool {
	var stripped strings.Builder
	inString, escaped := false, false
	for i := 0; i < len(raw); i++ {
		ch := raw[i]
		if inString {
			stripped.WriteByte(ch)
			if escaped {
				escaped = false
			} else if ch == '\\' {
				escaped = true
			} else if ch == '"' {
				inString = false
			}
			continue
		}
		if ch == '"' {
			inString = true
			stripped.WriteByte(ch)
			continue
		}
		if ch == '/' && i+1 < len(raw) && (raw[i+1] == '/' || raw[i+1] == '*') {
			return true
		}
		stripped.WriteByte(ch)
	}
	// 尾逗号：去掉后若变成合法 JSON，则原文件就是 JSONC。
	cleaned := strings.NewReplacer(",}", "}", ",]", "]").Replace(stripped.String())
	var probe any
	return json.Unmarshal([]byte(cleaned), &probe) == nil
}

// prevOfJSON 把一个 JSON 值记成 Prev：不存在时 Present=false。
func prevOfJSON(value any) Prev {
	if value == nil {
		return Prev{Present: false}
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return Prev{Present: false}
	}
	if string(raw) == "{}" || string(raw) == "null" {
		return Prev{Present: false}
	}
	return Prev{Present: true, Value: string(raw)}
}

// openCodeDir 是 OpenCode 读用户配置的目录：$OPENCODE_CONFIG_DIR 优先，
// 其次系统配置目录下的 opencode（Linux/macOS ~/.config、Windows %APPDATA%）。
func openCodeDir() string {
	if dir := strings.TrimSpace(os.Getenv("OPENCODE_CONFIG_DIR")); dir != "" {
		return dir
	}
	base, err := os.UserConfigDir()
	if err != nil || base == "" {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, ".config", "opencode")
	}
	return filepath.Join(base, "opencode")
}
