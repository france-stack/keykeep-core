package providers

import (
	"os"
	"strconv"
)

// Overrides 是网关通过环境变量覆盖 SDK 重试/冷却行为的可选值；
// nil 表示沿用基础配置，不改变现场。
type Overrides struct {
	RequestRetry        *int
	MaxRetryCredentials *int
	MaxRetryInterval    *int
	DisableCooling      *bool
	TransientCooldown   *int
}

// OverridesFromEnv 读取 KEYKEEP_SDK_*；无法解析的值按未设置处理。
func OverridesFromEnv() Overrides {
	var o Overrides
	o.RequestRetry = envInt("KEYKEEP_SDK_REQUEST_RETRY")
	o.MaxRetryCredentials = envInt("KEYKEEP_SDK_MAX_RETRY_CREDENTIALS")
	o.MaxRetryInterval = envInt("KEYKEEP_SDK_MAX_RETRY_INTERVAL")
	o.TransientCooldown = envInt("KEYKEEP_SDK_TRANSIENT_COOLDOWN_SECONDS")
	if raw := os.Getenv("KEYKEEP_SDK_DISABLE_COOLING"); raw != "" {
		value, err := strconv.ParseBool(raw)
		if err == nil {
			o.DisableCooling = &value
		}
	}
	return o
}

func envInt(key string) *int {
	raw := os.Getenv(key)
	if raw == "" {
		return nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return nil
	}
	return &value
}

func (o Overrides) IsZero() bool {
	return o.RequestRetry == nil && o.MaxRetryCredentials == nil && o.MaxRetryInterval == nil &&
		o.DisableCooling == nil && o.TransientCooldown == nil
}
