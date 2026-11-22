# 源码目录

| 路径 | 内容 | 技术选型 |
|---|---|---|
| `gateway/` | 数据面：协议转发、虚拟 Key 校验、限流、预算、账本、脱敏、熔断 | Go + net/http |
| `gateway/cmd/kkconnect/` | 员工端连接器：本地回环转发 + 改写 Claude Code / Codex / Gemini CLI / OpenCode / Qwen 配置 | Go，单二进制 |
| `gateway/cmd/kkdesk/`, `kkapp/` | Windows 桌面版进程看护与托盘 | Go + WebView2 |
| `gateway/cmd/kkmock/` | 桌面版内置演示上游 | Go |
| `control/` | 管理面：控制台页面 + 管理 API + 数据模型 | Next.js App Router + TypeScript |
| `control/prisma/` | 数据模型与迁移（Prisma Migrate，14 个迁移） | PostgreSQL 16 |
| `desktop-tools/` | Windows 安装包的迁移 / 首装种子 / 备份恢复脚本（随包分发） | Node（无打包，直接 node 运行） |

## 硬约束

1. `gateway/` 与 `control/` 之间只通过 PostgreSQL 与 HTTP 交互，不共享代码。
2. 不得在任何目录下存放凭据、`.env` 或私钥；真实上游 Key 只能经管理面加密入库。
3. 网关侧的新依赖必须能在 `CGO_ENABLED=0` 下交叉编译（桌面版与 Linux 镜像都要用它）。
4. `control/` 的迁移一经发布不得修改，只能新增。
