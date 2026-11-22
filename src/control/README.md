# KeyKeep 控制面（阶段 1 脚手架）

Next.js App Router + TypeScript + Prisma。当前包含：

- 控制台外壳（对齐 Magpie 0.1.2145 布局与动效）：`app/(console)/layout.tsx` 常驻 `AppShell`，
  侧栏 212px（可拖 168–320），窄屏自动切顶部胶囊导航；导航定义见 `app/components/nav.ts`；
- 页面：概览 `/`（真实数据）、用量与请求、Key、部门员工、凭据、模型目录与价格、全局限流、预算、审计、设置；
- Prisma schema（dev-spec §3.2 的十一张表）；
- 管理面端口约定：Next 内部监听 `3000`，Compose 对外发布 `8443`（R-05 管理面与转发面分离）。

## 已实现（M1 + M2/M3 纵向切片）

- 登录 / 登出 / 会话：Argon2id 口令、失败 5 次锁定 15 分钟、HMAC 签名 Cookie。
- 虚拟 Key：批量签发、明文只显示一次、库内 SHA-256 + 前缀/后 6 指纹、撤销。
- 部门与员工：两级部门、员工创建/禁用/重置口令、admin / dept_admin / employee 权限矩阵。
- 凭据接入：上游 Key 用 AES-256-GCM + KEK 加密；供应商新增/停用/连通性测试；`alias=上游模型名` 模型映射。
- 网关侧：查 `ApiKey` 校验虚拟 Key，撤销即 401；设置 `KEYKEEP_DATABASE_URL` + `KEYKEEP_KEK` 后从
  `ProviderCredential` + `ModelMap` 解密并生成 SDK 配置。
- 测试：46 条 Vitest（会话/口令/Key/权限/加密/导航/侧栏宽度）+ Go 中间件/服务/provider 测试；`tsc` 与 `next build` 通过。

## 运行

```bash
cd src/control
pnpm install --frozen-lockfile
export DATABASE_URL="postgresql://keykeep:keykeep-dev-only@127.0.0.1:5432/keykeep?schema=public"
export SESSION_SECRET="local-dev-session-secret-0123456789"
export KEYKEEP_KEK="<base64 32 字节；openssl rand -base64 32>"   # 网关必须使用同一个
pnpm prisma migrate deploy          # 需要可用的 PostgreSQL
pnpm prisma:seed                    # 未设置 KEYKEEP_SEED_* 时随机生成并打印一次
pnpm dev                            # http://localhost:3000
```

说明：运行时使用 `@prisma/adapter-pg`（`pg` 驱动），因此 Windows 宿主机连 WSL 内的
PostgreSQL 也能工作；`prisma migrate` / `prisma:seed` 仍走 Prisma 引擎。若本机引擎连不上
WSL 的 PG，可用 `psql -f prisma/migrations/20261030000000_init/migration.sql` 建表，
再用应用侧的 adapter 访问。不要把 `.env.local` 留在工作区——`secret-scan` 会按
`sensitive-filename` 报出它（这是有意的）。

构建：

```bash
pnpm lint
pnpm build
pnpm start
```

## 环境变量

| 变量 | 示例 | 说明 |
|---|---|---|
| `DATABASE_URL` | `postgresql://keykeep:keykeep@localhost:5432/keykeep` | PostgreSQL 16（唯一存储） |
| `KEYKEEP_GATEWAY_URL` | `http://localhost:8080` | 转发面地址（后续 API 代理用） |
| `KEYKEEP_KEK` | base64 32 字节 | 上游凭据加密主密钥；必须与网关一致 |
| `KEYKEEP_AUDIT_CHAIN_KEY` | 32 字节以上随机串 | 审计链 HMAC 密钥；必须与网关一致 |
| `SESSION_SECRET` | ≥16 字符 | 会话 Cookie 的 HMAC 密钥；生产必填 |

Redis 不在栈内：限流、预算与幂等全部走 PostgreSQL，本仓库源码对 Redis 零引用。

## 边界

- 已实现登录、Key 签发、组织、供应商接入、用量看板/明细/哈希链校验与预算管理；
  审计（M7）、RPM/TPM 限速与报表导出仍未实现。
- `prisma/migrations/` 已生成三个迁移（init / providers / usage_ledger），按顺序 `psql -f` 或
  `prisma migrate deploy` 应用。
- 管理面必须与转发面不同端口；Compose 用 `8443:3000` 表达。
