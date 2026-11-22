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

FROM node:22-alpine AS runtime
WORKDIR /app
ENV NODE_ENV=production NEXT_TELEMETRY_DISABLED=1
COPY --from=build /app/.next/standalone ./
COPY --from=build /app/.next/static ./.next/static
COPY --from=build /app/public ./public
EXPOSE 3000
CMD ["node", "server.js"]
