# 基础镜像可覆盖：gcr.io 在部分网络（国内、企业代理后）会 TLS 握手超时，
# 而这是 `docker compose up` 的第一道坎。默认值保持官方 distroless 不变
# （非 root、无 shell、最小攻击面），需要镜像源时用 build-arg 换一个等价底座：
#   GATEWAY_BASE_IMAGE=docker.m.daocloud.io/gcr.io/distroless/static-debian12:nonroot
# 同一哲学见 src/control/Dockerfile 的 COREPACK_NPM_REGISTRY。
ARG GATEWAY_BASE_IMAGE=gcr.io/distroless/static-debian12:nonroot

FROM golang:1.26.10 AS build
WORKDIR /src
# 构建期 Go 模块代理：默认官方源，可在 .env 里用 GOPROXY 换国内镜像。
ARG GOPROXY=https://proxy.golang.org,direct
ENV CGO_ENABLED=0 GOTOOLCHAIN=auto GOPROXY=${GOPROXY}
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -trimpath -ldflags="-s -w" -o /out/gateway ./cmd/gateway

FROM ${GATEWAY_BASE_IMAGE}
WORKDIR /app
COPY --from=build /out/gateway /app/gateway
COPY config.example.yaml /app/config.example.yaml
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/app/gateway"]
