# KeyKeep 数据面网关（阶段 1 脚手架）

路线 A：在进程内嵌入 CLIProxyAPI `v8.0.47` 的 `sdk/cliproxy`。外层只负责：

- 对外监听 `:8080`；
- `/healthz`（不经过 SDK，返回 `status` 与 `version`）；
- 其余请求反向代理到内嵌 SDK 的 `127.0.0.1:18080`。

这样健康检查、虚拟 Key 校验与未来的限流钩子都留在自己的进程里，协议转换仍由 SDK 完成。
P-01 已证明 SDK 可嵌入（113 行、CGO=0、冷编译 42.54 s）。

设置 `KEYKEEP_DATABASE_URL` 后，网关会查控制面的 `ApiKey` 表校验虚拟 Key，
把 `Authorization` 换成 `<KEYKEEP_INTERNAL_KEY_PREFIX><keyId>`（默认 `ik_<id>`）再交给 SDK，用量回调据此归属员工；撤销（`revokedAt` 非空）即返回 401。

同时设置 `KEYKEEP_KEK`（base64 32 字节）后，网关会从 `ProviderCredential` + `ModelMap`
读取启用的供应商、解密上游 Key，并生成 SDK v8 配置；禁用凭据不进入路由。控制面与网关必须使用同一个 KEK。

## 运行

```bash
cd src/gateway
cp config.example.yaml config.yaml   # 替换占位 Key；本地联调可用 P-01 的 mock
go mod tidy
go run ./cmd/gateway
curl -s localhost:8080/healthz
```

Go 版本：`go.mod` 要求 **1.26.10**（候选库要求）；本机通过 `GOTOOLCHAIN=auto` 下载。

## 环境变量

| 变量 | 默认 | 说明 |
|---|---|---|
| `KEYKEEP_GATEWAY_LISTEN` | `:8080` | 对外监听地址 |
| `KEYKEEP_SDK_CONFIG` | `config.yaml` | SDK 配置路径 |
| `KEYKEEP_SDK_ADDR` | `127.0.0.1:18080` | 内嵌 SDK 监听地址（仅回环） |
| `KEYKEEP_DATABASE_URL` | 空 | 设置后启用虚拟 Key 查库校验；例如 `postgresql://keykeep:...@postgres:5432/keykeep?sslmode=disable` |
| `KEYKEEP_INTERNAL_KEY_PREFIX` | `ik_` | 网关替换给 SDK 的内部 Key 前缀；SDK 的 `access.api-keys` 由网关按未撤销 Key 自动生成 |
| `KEYKEEP_KEK` | 空 | base64 32 字节；设置后从数据库读取并解密供应商凭据；与控制面共用 |

## 用量账本与预算

- SDK 的用量回调（`svc.RegisterUsagePlugin`）写入 `UsageRecord`：Sha-256 哈希链
  （`prevHash` + `rowHash`，时间取 Unix 秒）串行插入，并按 `UsageDaily` 双写日聚合。
- 单价来自 `ModelCatalog.pricing*Cents`，单位是「分 / 100 万 token」，向上取整到分；
  查不到价格时记 `unpriced=true`、费用 0。
- 两级预算（员工 / 全局；部门维度已移出产品面）在请求进入时按请求体估算预扣，响应结束后释放；
  实际费用在账本落库后结算（`BudgetLedger` 以 `(budgetId, rid, kind)` 幂等）。
  任一维度不足即返回 402，只回最紧一级的剩余额度。
- 预扣占用 `Budget.reservedCents`；进程启动时清零遗留预扣（单实例前提，多实例需改为带租约的预扣表）。

## 测试

```bash
go test ./...
```

## 边界

- 已实现虚拟 Key 校验、供应商配置、用量账本与两级预算；管理面审计与网关拒绝事件已实现（M7 部分），
  RPM/TPM 限速已实现（Key 级 + 全局级）；多实例预扣租约仍未实现。
- 上游凭据不写入本目录；生产由管理面加密后写入 SDK 的 `auth-dir`。
- 反向代理会保留 SSE 流式刷新；首字节与断流行为需在 M3 验收中复测。
