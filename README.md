# KeyKeep（钥守）

> **状态：早期可用（v0.1.2）。** 适用于个人学习、内网自建、小团队自托管评估；
> 尚未在生产环境长期验证。安装包未做代码签名，接入真实供应商前请自行核对对方条款。

公司统一保管真实的模型上游凭据，员工只拿到**可撤销、可限额、可审计**的虚拟 Key。
员工用现有的 AI 工具照常工作，而真实凭据、预算和审计始终握在公司手里。

- 数据面（转发）：`8080`，OpenAI / Anthropic 兼容
- 管理面（控制台）：`8443`
- 存储：PostgreSQL 16（唯一依赖，**不需要 Redis**）

## 五分钟跑通（Docker Compose）

```bash
git clone https://github.com/france-stack/keykeep-core.git
cd keykeep-core

# 1) 生成配置：只需要 4 个必填项
cp .env.example .env
openssl rand -base64 32   # → KEYKEEP_KEK
openssl rand -hex 16      # → POSTGRES_PASSWORD
openssl rand -hex 32      # → SESSION_SECRET
# 再把 KEYKEEP_SEED_ADMIN_PASSWORD 改成你自己的管理员口令（≥12 位）

# 2) 起栈（首次会构建镜像；migrate 服务自动跑迁移 + 建管理员）
docker compose up -d

# 3) 等三个服务就绪
docker compose ps          # migrate 应该是 Exited (0)
curl -s localhost:8443/api/healthz   # {"status":"ok","checks":{...}}

# 4) 打开控制台
#    http://127.0.0.1:8443   账号 admin，口令 = 你在 .env 里设的那个
```

**跑一次完整链路**（登录 → 接上游 → 建员工 → 签 Key → 转发 → 记账 → 撤销）：

```bash
# 用栈内自带的假上游（不需要任何真实 Key）
docker compose --profile demo up -d mock
python3 scripts/demo/quickstart.py \
  --mock --base-url http://mock:19011 --provider anthropic \
  --model claude-3-5-sonnet-20241022 \
  --admin-password "$KEYKEEP_SEED_ADMIN_PASSWORD"

# 接真实 OpenAI（控制面默认禁止私网上游，公网地址不受影响）
OPENAI_API_KEY=sk-... python3 scripts/demo/quickstart.py \
  --provider openai --base-url https://api.openai.com/v1 \
  --upstream-key-env OPENAI_API_KEY --model gpt-4o-mini \
  --price-prompt 250 --price-completion 1000 \
  --admin-password "$KEYKEEP_SEED_ADMIN_PASSWORD"
```

脚本会逐步打印每一步的结果，任何一步失败都直接告诉你断在哪。

## 它解决什么问题

| 没有钥守 | 有了钥守 |
|---|---|
| 上游 API Key 直接发给员工，收不回来 | 员工只拿虚拟 Key，随手可撤销，撤销**立即失效** |
| 谁用了多少 token、花了多少钱，没人说得清 | 每次调用按模型计价入账本，费用精确到员工 / Key |
| 疑似泄漏只能全网更换真实 Key | 撤销虚拟 Key 即可，真实凭据永不离开管理面 |
| 预算与用量失控 | 员工级 + 全局级两级预算、Key 级 + 全局级两级限流 |
| 敏感内容随提示词发往上游 | 规则式脱敏，按策略替换后再转发 |

## 核心能力

- **虚拟 Key 分发** — 批量签发、撤销即时生效；明文只显示一次，之后只显示指纹
- **凭据集中托管** — 上游 Key 用 AES-256-GCM + KEK 加密入库，只有管理面可写
- **多供应商与模型映射** — 逻辑别名映射上游模型，可多条映射分流；OpenAI / Anthropic / Gemini 协议，流式（SSE）与工具调用
- **两级限流 + 两级预算** — Key 级 / 全局级 RPM·TPM，员工级 / 全局级预算，超限 429 / 402
- **计量账本** — 按模型目录计价，价格支持版本管理；用量明细、CSV 导出、哈希链校验
- **规则式脱敏** — 内置规则 + 自定义规则 + 三种预设，替换后再转发
- **审计日志** — 登录、签发、撤销、调用失败、维护操作全程留痕（可选 HMAC 链）
- **员工自助** — 员工登录后自助查看接入方式、自己的 Key 与用量
- **员工端连接器** — `kkconnect` 单二进制，把 Claude Code / Codex / Gemini CLI / OpenCode / Qwen 的请求转到公司网关，员工本地不保存上游凭据
- **维护平面（默认关闭）** — 客户开工单审批后限时开启，短期凭据撤销 ≤1s；关闭时零监听
- **上游熔断** — 连续失败 3 次打开，冷却 5s 起指数翻倍至 300s 封顶，打开期间该凭据整组剔除

## 技术栈

| 层 | 技术 |
|---|---|
| 转发面 | Go（嵌入 [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) v8 SDK） |
| 管理面 | Next.js 16 App Router + React 19 + TypeScript |
| 数据 | PostgreSQL 16 + Prisma（限流/预算/幂等/并发闸门全部走 PG 或进程内存） |
| 桌面版 | Windows 安装器（内置便携 PG + Node），WebView2 原生窗口 |

## 目录

```
src/gateway/       Go 数据面：虚拟 Key 校验、限流、预算、账本、脱敏、熔断、连接器
src/control/       Next.js 控制台 + 管理 API + Prisma schema 与迁移
src/desktop-tools/ Windows 桌面版首装/迁移/备份脚本（随安装包分发）
scripts/demo/      端到端演示脚本（本 README 里的「五分钟跑通」）
docker-compose.yml PG16 + 转发面 + 管理面 + 一次性 migrate
.env.example       配置模板（只含占位符）
```

## Windows 桌面版

不想装 Docker 的话，桌面版内置了 PostgreSQL 与 Node 运行时，双击安装即可，
装完自带演示上游。安装包在 [Releases](https://github.com/france-stack/keykeep-releases/releases)。

## 已知限制

- **只在 Windows 桌面形态做过完整装机验收**；Compose 形态的升级/回滚尚未实现。
- 真实供应商只联调过 1 家（OpenDesign）。其余供应商目录条目按协议实现，**未逐家实测**。
- 多实例部署未验证：预算预扣是进程内模型，全局并发上限是单实例口径。
- 模型式脱敏检测器只有接口，默认 `rules-only`（离线规则）。
- 供应商条款、隐私声明、数据出境等法务结论**均未完成**。

## 开发

```bash
# 后端（Go 1.26+）
cd src/gateway && go vet ./... && go test ./...

# 前端（Node 22 + pnpm）
cd src/control && pnpm install && pnpm test && npx tsc --noEmit

# 端到端（需要 Docker；起一个隔离的栈并跑演示链路）
cd ../.. && docker compose up -d && docker compose --profile demo up -d mock
python3 scripts/demo/quickstart.py --mock --base-url http://mock:19011   --provider anthropic --model claude-3-5-sonnet-20241022   --admin-password <你在 .env 里设的管理员口令>
```

## 许可

[Apache License 2.0](./LICENSE)。第三方组件的许可证见
[NOTICE](./NOTICE)。商标与供应商图标的使用说明见
[src/control/public/provider-icons/NOTICE.md](src/control/public/provider-icons/NOTICE.md)。

> 本项目不是任何模型供应商的官方项目，未获任何厂商授权或背书。
