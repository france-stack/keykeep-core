package breaker

import (
	"context"
	"net/http"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth" // secret-scan:ignore high-entropy-string 公开 Go 模块路径，非凭据
)

// CredentialFailure 把一次 SDK 执行结果分类为「是否计入凭据健康」。
//
// 只计凭据/上游侧故障（5xx、超时、凭据失效）；4xx 客户端错误（400/413/422）
// 是请求本身的问题，换个凭据也一样失败——计进去只会把好凭据打掉。
// 返回 true 表示计入失败；返回 false 表示不计（既不算失败也不算成功）。
func CredentialFailure(result coreauth.Result) bool {
	if result.Success {
		return false
	}
	status := 0
	code := ""
	if result.Error != nil {
		status = result.Error.HTTPStatus
		code = result.Error.Code
	}
	switch {
	case status >= 500 || status == http.StatusRequestTimeout ||
		status == http.StatusTooManyRequests:
		return true
	}
	switch code {
	case "upstream_error", "upstream_timeout", "timeout", "connection_error",
		"network_error", "auth_not_found", "unauthorized", "invalid_api_key",
		"quota_exceeded", "rate_limited":
		return true
	}
	// 没有状态码也没有已知 code：多数是传输层失败（连接被拒/重置/DNS）。
	return status == 0 && code != ""
}

// Policy 是 coreauth.ResultPolicy 的实现：把 SDK 执行结果喂给状态机。
type Policy struct {
	machine *Machine
}

// NewPolicy 构造结果策略。machine 为 nil 时返回 nil（未启用熔断）。
func NewPolicy(machine *Machine) coreauth.ResultPolicy {
	if machine == nil {
		return nil
	}
	return &Policy{machine: machine}
}

// ApplyResultPolicy 记录失败/成功。成功一律记（清计数）；失败只在
// CredentialFailure 判为凭据侧故障时记。
func (p *Policy) ApplyResultPolicy(_ context.Context, result coreauth.Result) coreauth.Result {
	if p == nil || p.machine == nil {
		return result
	}
	key := result.AuthID
	if key == "" {
		return result
	}
	switch {
	case result.Success:
		p.machine.Observe(key, true)
	case CredentialFailure(result):
		p.machine.Observe(key, false)
	}
	return result
}

// Now 暴露时钟给测试与调用方。
func (m *Machine) Now() time.Time {
	if m == nil {
		return time.Now()
	}
	return m.opts.Now()
}
