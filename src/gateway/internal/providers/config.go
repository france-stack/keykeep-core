// Package providers 从控制面读取供应商凭据与模型映射，构建嵌入 SDK 的配置。
package providers

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/keykeep/gateway/internal/breaker"
	"github.com/keykeep/gateway/internal/dbpool"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"gopkg.in/yaml.v3"
)

type Model struct {
	Alias   string
	ModelID string
}

type Provider struct {
	ID             string
	Name           string
	Type           string // openai | anthropic | custom
	BaseURL        string
	AuthHeaderName string
	AuthScheme     string
	APIKey         string
	ExtraHeaders   map[string]string
	Models         []Model
}

// LoadKEK 解析 base64 编码的 32 字节 KEK。
func LoadKEK(raw string) ([]byte, error) {
	kek, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("decode KEK: %w", err)
	}
	if len(kek) != 32 {
		return nil, fmt.Errorf("KEK must be 32 bytes, got %d", len(kek))
	}
	return kek, nil
}

// decrypt 解 AES-256-GCM；ct 为 ciphertext || tag（与 Node crypto 一致）。
func decrypt(ct, nonce, kek []byte) (string, error) {
	block, err := aes.NewCipher(kek)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	plain, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", errors.New("decrypt failed")
	}
	return string(plain), nil
}

// Load 从 ProviderCredential / ModelMap 构建配置；禁用凭据不进入 SDK。
// 返回解析后的配置与生成 YAML 原文（供写入临时配置文件，避免 SDK watcher 覆盖运行时 access keys）。
func Load(ctx context.Context, databaseURL, kekRaw string, base *config.Config, basePath, keyPrefix string, overrides Overrides, skip func(breakerID string) bool) (*config.Config, []byte, error) {
	kek, err := LoadKEK(kekRaw)
	if err != nil {
		return nil, nil, err
	}
	pool, err := dbpool.Open(ctx, databaseURL)
	if err != nil {
		return nil, nil, err
	}
	defer pool.Close()

	rows, err := pool.Query(ctx,
		`SELECT c.id::text, c.name, c.provider, c."providerType", c."baseUrl",
		        c."authHeaderName", c."authScheme", c."extraHeaders", c.ct, c.nonce,
		        COALESCE(m.alias, ''), COALESCE(m."modelId", '')
		   FROM "ProviderCredential" c
		   LEFT JOIN "ModelMap" m ON m."credentialId" = c.id
		  WHERE c.disabled = false
		  ORDER BY c."createdAt", m.alias`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	order := make([]string, 0)
	byID := make(map[string]*Provider)
	// 解密失败的凭据 id：这一行的模型映射也不能挂到别的凭据上。
	skipped := make(map[string]struct{})
	for rows.Next() {
		var (
			id, name, provider, providerType, baseURL string
			authHeader, authScheme                    string
			rawHeaders, ct, nonce                     []byte
			alias, modelID                            string
		)
		if err := rows.Scan(&id, &name, &provider, &providerType, &baseURL,
			&authHeader, &authScheme, &rawHeaders, &ct, &nonce, &alias, &modelID); err != nil {
			return nil, nil, err
		}
		p, ok := byID[id]
		if !ok {
			apiKey, err := decrypt(ct, nonce, kek)
			if err != nil {
				// 单条凭据解不开不等于整站不可用：换过 KEK、或库里留了旧密钥的
				// 加密行时，跳过这一条，其余渠道照常转发。整进程退出会让所有
				// 员工一起断（实测踩过：旧 mock 凭据把网关直接打挂）。
				log.Printf("provider %s (%s): %v; skipping this credential", name, id, err)
				skipped[id] = struct{}{}
				continue
			}
			headers := map[string]string{}
			if len(rawHeaders) > 0 {
				_ = json.Unmarshal(rawHeaders, &headers)
			}
			p = &Provider{
				ID:             id,
				Name:           name,
				Type:           providerType,
				BaseURL:        baseURL,
				AuthHeaderName: authHeader,
				AuthScheme:     authScheme,
				APIKey:         apiKey,
				ExtraHeaders:   headers,
			}
			byID[id] = p
			order = append(order, id)
		}
		if _, dropped := skipped[id]; dropped {
			continue
		}
		if alias != "" && modelID != "" {
			p.Models = append(p.Models, Model{Alias: alias, ModelID: modelID})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	list := make([]Provider, 0, len(order))
	for _, id := range order {
		list = append(list, *byID[id])
	}

	accessKeys := make([]string, 0)
	keyRows, err := pool.Query(ctx, `SELECT k.id::text FROM "ApiKey" k JOIN "User" u ON u.id = k."empId"
		   WHERE k."revokedAt" IS NULL AND u.disabled = false`)
	if err != nil {
		return nil, nil, err
	}
	defer keyRows.Close()
	for keyRows.Next() {
		var id string
		if err := keyRows.Scan(&id); err != nil {
			return nil, nil, err
		}
		accessKeys = append(accessKeys, keyPrefix+id)
	}
	if err := keyRows.Err(); err != nil {
		return nil, nil, err
	}

	return BuildConfigFromBase(basePath, base, list, accessKeys, overrides, nil)
}

// BuildConfig 保留旧签名：用空基础文档构建（供单测与无基础配置场景）。
func BuildConfig(base *config.Config, list []Provider, accessKeys []string) (*config.Config, error) {
	configPath := ""
	if base != nil && base.AuthDir != "" {
		configPath = ""
	}
	cfg, _, err := BuildConfigFromBase(configPath, base, list, accessKeys, Overrides{}, nil)
	return cfg, err
}

// BuildConfigFromBase 以 basePath 的 YAML 为底稿：保留 routing/retry/cooldown/streaming 等字段，
// 只覆盖 access、供应商组与 auth-dir。basePath 为空或读取失败时从空文档开始。
func BuildConfigFromBase(basePath string, base *config.Config, list []Provider, accessKeys []string, overrides Overrides, skip func(breakerID string) bool) (*config.Config, []byte, error) {
	doc := map[string]any{}
	if basePath != "" {
		raw, err := os.ReadFile(basePath)
		if err == nil {
			if err := yaml.Unmarshal(raw, &doc); err != nil {
				return nil, nil, fmt.Errorf("parse base config: %w", err)
			}
		}
	}
	if doc == nil {
		doc = map[string]any{}
	}

	claudeGroups := make([]map[string]any, 0)
	openaiGroups := make([]map[string]any, 0)
	geminiKeys := make([]map[string]any, 0)
	vertexKeys := make([]map[string]any, 0)
	xaiKeys := make([]map[string]any, 0)
	codexKeys := make([]map[string]any, 0)

	for _, p := range list {
		models := make([]map[string]any, 0, len(p.Models))
		for _, m := range p.Models {
			models = append(models, map[string]any{"name": m.ModelID, "alias": m.Alias})
		}

		switch p.Type {
		case "anthropic":
			// 熔断打开时整个凭据组不出现在配置里：SDK 看不到它就不会再选它（FR-27）。
			// 组内模型别名随之消失，其他凭据的同名别名不受影响（别名本来就是多对一）。
			if skip != nil && skip(breaker.CredentialID(p.APIKey, p.BaseURL)) {
				continue
			}
			group := map[string]any{
				"name":     p.Name,
				"base-url": p.BaseURL,
				"models":   models,
				"keys":     []map[string]any{{"api-key": p.APIKey}},
			}
			if headers := anthropicHeaders(p); len(headers) > 0 {
				group["headers"] = headers
			}
			claudeGroups = append(claudeGroups, group)
		case "gemini", "vertex", "xai", "codex":
			// 这四类在 SDK 里是顶层 *-api-key 数组，每项一把 Key + 模型别名。
			key := apiKeyEntry(p, models)
			switch p.Type {
			case "gemini":
				geminiKeys = append(geminiKeys, key)
			case "vertex":
				vertexKeys = append(vertexKeys, key)
			case "xai":
				xaiKeys = append(xaiKeys, key)
			case "codex":
				codexKeys = append(codexKeys, key)
			}
		default:
			group := map[string]any{
				"name":            p.Name,
				"base-url":        p.BaseURL,
				"models":          models,
				"api-key-entries": []map[string]any{{"api-key": p.APIKey}},
			}
			if len(p.ExtraHeaders) > 0 {
				group["headers"] = p.ExtraHeaders
			}
			openaiGroups = append(openaiGroups, group)
		}
	}

	doc["config-version"] = 8
	// 空访问列表绝不能写进 SDK：内嵌 SDK 对零 access provider 是 fail-open
	// （v8.0.47 sdk/access/manager.go Authenticate 零 provider 直接放行），
	// 那会让「键已签发、重载未发生」窗口里的请求绕过 SDK 层鉴权，且账本拿不到
	// 内部键而丢账（2026-11-08 E2E 复跑实测丢 5 笔）。空列表时保留底稿的 access
	// 段（fail-closed：没人会拿着 "keykeep-internal" 来调用），直到下一次重载
	// 把真实内部键写进去。
	if len(accessKeys) == 0 {
		if _, hasBase := doc["access"].(map[string]any); !hasBase {
			doc["access"] = map[string]any{"api-keys": []string{"keykeep-internal"}}
		}
	} else {
		access, _ := doc["access"].(map[string]any)
		if access == nil {
			access = map[string]any{}
		}
		access["api-keys"] = accessKeys
		doc["access"] = access
	}

	if base != nil && base.AuthDir != "" {
		doc["auth-dir"] = base.AuthDir
	}
	apiKeys, _ := doc["api-keys"].(map[string]any)
	if apiKeys == nil {
		apiKeys = map[string]any{}
	}
	delete(apiKeys, "claude")
	if len(claudeGroups) > 0 {
		apiKeys["claude"] = claudeGroups
	}
	if len(apiKeys) > 0 {
		doc["api-keys"] = apiKeys
	} else {
		delete(doc, "api-keys")
	}
	if len(openaiGroups) > 0 {
		doc["openai-compatibility"] = openaiGroups
	} else {
		delete(doc, "openai-compatibility")
	}
	setKeySection(doc, "gemini-api-key", geminiKeys)
	setKeySection(doc, "vertex-api-key", vertexKeys)
	setKeySection(doc, "xai-api-key", xaiKeys)
	setKeySection(doc, "codex-api-key", codexKeys)

	applyOverrides(doc, overrides)

	raw, err := yaml.Marshal(doc)
	if err != nil {
		return nil, nil, err
	}
	cfg, err := config.ParseConfigBytes(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("parse generated config: %w", err)
	}
	return cfg, raw, nil
}

// anthropicHeaders 把控制面配置的鉴权头写进 SDK 的 headers 段。
//
// SDK 只对 api.anthropic.com 用 x-api-key，其余 base-url 一律发 "Authorization: Bearer <key>"。
// Anthropic 兼容中转（如 OpenDesign）若只认 x-api-key 或裸 key，就用这里覆盖：
// 自定义 headers 在 PrepareRequest 之后应用，所以同名的 Authorization 会被覆盖。
func anthropicHeaders(p Provider) map[string]string {
	headers := map[string]string{}
	for k, v := range p.ExtraHeaders {
		headers[k] = v
	}
	name := p.AuthHeaderName
	if name == "" {
		name = "Authorization"
	}
	scheme := p.AuthScheme
	if strings.EqualFold(name, "Authorization") && (scheme == "" || strings.EqualFold(scheme, "Bearer")) {
		// SDK 默认形状即可，不额外覆盖。
		return headers
	}
	value := strings.TrimSpace(scheme + " " + p.APIKey)
	if scheme == "" {
		value = p.APIKey
	}
	headers[name] = value
	return headers
}

// apiKeyEntry 生成 gemini/vertex/xai/codex 顶层数组的一项。
func apiKeyEntry(p Provider, models []map[string]any) map[string]any {
	entry := map[string]any{"api-key": p.APIKey, "models": models}
	if p.BaseURL != "" {
		entry["base-url"] = p.BaseURL
	}
	if len(p.ExtraHeaders) > 0 {
		entry["headers"] = p.ExtraHeaders
	}
	return entry
}

func setKeySection(doc map[string]any, key string, entries []map[string]any) {
	if len(entries) > 0 {
		doc[key] = entries
	} else {
		delete(doc, key)
	}
}

// applyOverrides 把显式环境变量写到 v8 规范路径（routing.retry / routing.cooldown）。
func applyOverrides(doc map[string]any, o Overrides) {
	if o.IsZero() {
		return
	}
	routing, _ := doc["routing"].(map[string]any)
	if routing == nil {
		routing = map[string]any{}
	}
	if o.RequestRetry != nil || o.MaxRetryCredentials != nil || o.MaxRetryInterval != nil {
		retry, _ := routing["retry"].(map[string]any)
		if retry == nil {
			retry = map[string]any{}
		}
		if o.RequestRetry != nil {
			retry["request-retry"] = *o.RequestRetry
		}
		if o.MaxRetryCredentials != nil {
			retry["max-retry-credentials"] = *o.MaxRetryCredentials
		}
		if o.MaxRetryInterval != nil {
			retry["max-retry-interval"] = *o.MaxRetryInterval
		}
		routing["retry"] = retry
	}
	if o.DisableCooling != nil || o.TransientCooldown != nil {
		cooldown, _ := routing["cooldown"].(map[string]any)
		if cooldown == nil {
			cooldown = map[string]any{}
		}
		if o.DisableCooling != nil {
			cooldown["disable-cooling"] = *o.DisableCooling
		}
		if o.TransientCooldown != nil {
			cooldown["transient-error-cooldown-seconds"] = *o.TransientCooldown
		}
		routing["cooldown"] = cooldown
	}
	doc["routing"] = routing
}
