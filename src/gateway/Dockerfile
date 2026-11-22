FROM golang:1.26.10 AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOTOOLCHAIN=auto
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -trimpath -ldflags="-s -w" -o /out/gateway ./cmd/gateway

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/gateway /app/gateway
COPY config.example.yaml /app/config.example.yaml
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/app/gateway"]
