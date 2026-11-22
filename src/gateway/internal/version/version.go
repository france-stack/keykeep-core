// Package version 是构建期注入的版本号唯一入口。
//
// 仓库根目录的 VERSION 文件是唯一事实来源；scripts/build 下的构建脚本读取它，
// 通过 -ldflags "-X github.com/keykeep/gateway/internal/version.Value=x.y.z" 注入。
// 未注入时（go run / 单测）回退到下面的占位值，不假装自己是发布版本。
package version

// Value 由构建脚本注入，例如 0.1.0-test.1。
var Value = "0.0.0-dev"

// String 返回当前版本，供 CLI 与 /healthz 使用。
func String() string { return Value }
