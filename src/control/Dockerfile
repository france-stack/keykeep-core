# 构建期可达性：corepack 按 packageManager 字段即时下载 pnpm 二进制，默认
# registry 在部分网络下会长时间挂住（2026-11-08 E2E 实测两次挂死 25 分钟以上）。
# ARG 默认值保持官方源不变；E2E 叠加文件经 build.args 注入可达镜像源，与
# Dockerfile.e2e 的 GOPROXY=goproxy.cn 同一哲学。
ARG COREPACK_NPM_REGISTRY=https://registry.npmjs.org

FROM node:22-alpine AS deps
ARG COREPACK_NPM_REGISTRY
ENV COREPACK_NPM_REGISTRY=${COREPACK_NPM_REGISTRY} npm_config_registry=${COREPACK_NPM_REGISTRY}
WORKDIR /app
RUN corepack enable
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml ./
RUN pnpm install --frozen-lockfile

FROM node:22-alpine AS build
ARG COREPACK_NPM_REGISTRY
ENV COREPACK_NPM_REGISTRY=${COREPACK_NPM_REGISTRY} NEXT_TELEMETRY_DISABLED=1
WORKDIR /app
RUN corepack enable
COPY --from=deps /app/node_modules ./node_modules
COPY . .
RUN pnpm prisma generate && pnpm build

# 一次性迁移 + 首装种子镜像（compose 的 migrate 服务用它）。
#
# 为什么单独一层而不是塞进控制面镜像：控制面是 next standalone，里面没有
# prisma CLI（也不需要——运行时不该带迁移工具）。把 CLI 装进运行镜像等于
# 让生产进程随时能改表结构。这里只取 deps 阶段的完整依赖树 + prisma/
# + lib/（seed 的时区规整函数），跑完即退出。
#
# seed 是幂等的：同名账号已存在时只更新显示名/角色/启用位，不动口令哈希。
# 因此「每次 up 都跑一遍」不会把运维改过的管理员口令打回初始值。
FROM node:22-alpine AS migrate
ARG COREPACK_NPM_REGISTRY
ENV COREPACK_NPM_REGISTRY=${COREPACK_NPM_REGISTRY} npm_config_registry=${COREPACK_NPM_REGISTRY}
WORKDIR /app
RUN corepack enable
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml ./
RUN pnpm install --frozen-lockfile
COPY prisma ./prisma
COPY lib ./lib
RUN pnpm prisma generate
CMD ["sh", "-c", "pnpm prisma migrate deploy && node prisma/seed.mjs"]

FROM node:22-alpine AS runtime
WORKDIR /app
ENV NODE_ENV=production NEXT_TELEMETRY_DISABLED=1
COPY --from=build /app/.next/standalone ./
COPY --from=build /app/.next/static ./.next/static
COPY --from=build /app/public ./public
EXPOSE 3000
CMD ["node", "server.js"]
