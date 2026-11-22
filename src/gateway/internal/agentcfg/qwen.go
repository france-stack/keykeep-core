// Qwen Code 的配置写入。
//
// 真相来源（2026-11-06 实测 Qwen Code 0.13.1 + 对照 magpie internal/agent/qwen.go）：
//
//   - 配置目录**只认 $HOME/.qwen**。实测 QWEN_HOME 与 QWEN_CONFIG_DIR 都不生效
//     （en0 401 之后把 USERPROFILE/HOME 指到隔离目录才通），所以这里不提供环境变量覆盖，
//     免得给出「设了就能用」的假承诺。
//   - 走 OpenAI 兼容形状需要三处同时改，缺一不可：
//     env.<KEY>  = 占位 Key（由本工具自定义变量名，避免被 shell 里同名变量遮蔽）
//     modelProviders.openai[] 增一条 {id, name, baseUrl, envKey}
//     security.auth.selectedType = "openai"（否则 CLI 仍走登录态）
//     另外 model.name 指到刚加的那条，不写的话会停在旧模型上。
//   - baseUrl 必须是 /v1 结尾的 OpenAI 形状。
package agentcfg

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// qwenEnvKey 是 modelProviders 条目引用的环境变量名。用专属前缀而不是 OPENAI_API_KEY：
// Qwen Code 会优先读 shell 里的同名变量，用通用名容易被用户自己的值盖住。
const qwenEnvKey = "KEYKEEP_QWEN_API_KEY"

func qwenAgent() Agent {
	return Agent{
		ID:   "qwen",
		Name: "Qwen Code",
		Files: func() (map[string]string, error) {
			return map[string]string{fileMain: filepath.Join(qwenDir(), "settings.json")}, nil
		},
		Link: func(files map[string]string, local Local, st *LinkState) error {
			path := files[fileMain]
			doc, err := readJSONObject(path)
			if err != nil {
				return err
			}
			fs := st.file(fileMain, path)

			// 1) env.<KEY> = 占位 Key
			env, err := jsonObjectAt(doc, "env")
			if err != nil {
				return err
			}
			recordPrev(fs, "env."+qwenEnvKey, env[qwenEnvKey])
			env[qwenEnvKey] = local.Key
			doc["env"] = env

			// 2) modelProviders.openai[]：先删本工具上次写的那条，再追加。
			entries, err := qwenEntries(doc)
			if err != nil {
				return err
			}
			kept := make([]any, 0, len(entries)+1)
			for _, entry := range entries {
				if object, ok := entry.(map[string]any); ok && object["envKey"] == qwenEnvKey {
					continue // 本工具写的旧条目，整条替换
				}
				kept = append(kept, entry)
			}
			if local.Model != "" {
				kept = append(kept, map[string]any{
					"id":      local.Model,
					"name":    local.Model,
					"baseUrl": local.OpenAIBase,
					"envKey":  qwenEnvKey,
				})
			}
			if len(kept) > 0 {
				if err := jsonSet(doc, "modelProviders.openai", kept); err != nil {
					return err
				}
			} else {
				jsonDel(doc, "modelProviders.openai")
			}
			// 整组的原貌记一次，unlink 时按它还原（用户自己那条不会被动到）。
			if _, known := fs.Prev["modelProviders.openai"]; !known {
				fs.Prev["modelProviders.openai"] = prevOfJSON(entries)
			}

			// 3) 认证方式切成 openai
			value, ok := jsonGet(doc, "security.auth.selectedType")
			if ok {
				recordPrev(fs, "security.auth.selectedType", value)
			} else {
				recordPrev(fs, "security.auth.selectedType", nil)
			}
			if err := jsonSet(doc, "security.auth.selectedType", "openai"); err != nil {
				return err
			}

			// 4) model.name 指到刚加的那条
			if local.Model != "" {
				current, _ := jsonGet(doc, "model.name")
				recordPrev(fs, "model.name", current)
				if err := jsonSet(doc, "model.name", local.Model); err != nil {
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
			doc, err := readJSONObject(path)
			if err != nil {
				return err
			}
			// env.<KEY>：原本没有就删（jsonDel 会回收变空的 env 对象）。
			restoreJSON(doc, fs, "env."+qwenEnvKey)
			// modelProviders.openai：按快照整体写回，保留用户自己的条目与原顺序。
			if p, known := fs.Prev["modelProviders.openai"]; known {
				if p.Present {
					var original []any
					if err := json.Unmarshal([]byte(p.Value), &original); err == nil && len(original) > 0 {
						_ = jsonSet(doc, "modelProviders.openai", original)
					} else {
						jsonDel(doc, "modelProviders.openai")
					}
				} else {
					jsonDel(doc, "modelProviders.openai")
				}
			}
			restoreJSON(doc, fs, "security.auth.selectedType")
			restoreJSON(doc, fs, "model.name")
			return writeJSONObject(path, doc)
		},
	}
}

// qwenEntries 返回 modelProviders.openai 的原始元素列表；不存在时是空列表。
func qwenEntries(doc map[string]any) ([]any, error) {
	value, ok := jsonGet(doc, "modelProviders.openai")
	if !ok || value == nil {
		return nil, nil
	}
	list, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf(
			"modelProviders.openai 不是数组（实际是 %T）。Qwen Code 的配置被别的工具改过形状，"+
				"请手动检查后再运行", value)
	}
	return list, nil
}

// qwenDir 是 Qwen Code 的配置目录。实测它读 os.homedir()/.qwen，
// QWEN_HOME / QWEN_CONFIG_DIR 都不生效，所以这里不提供覆盖。
func qwenDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".qwen")
}
