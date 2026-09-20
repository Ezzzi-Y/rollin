# =============================================================================
# Rollin 后端镜像（多阶段）
#   阶段一：golang:1.25 编译静态二进制（CGO 关闭，go.mod 声明 go 1.25）
#   阶段二：alpine 最小运行时，非 root 用户，UTC 时区，/healthz 健康检查
#
# 同一二进制两种入口（见 cmd/server/main.go）：
#   /app/rollin-server                    运行 HTTP 服务（默认）
#   /app/rollin-server migrate up         执行数据库迁移（迁移需手动执行，见根 docker-compose.yml 注释）
#
# 构建：docker build -t rollin-backend:latest ./rollin-backend
# =============================================================================
FROM golang:1.25-alpine AS builder

WORKDIR /src
# go-mod-accelerate：先拷依赖清单并下载，源码变更不击穿依赖层缓存
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/rollin-server ./cmd/server

# -----------------------------------------------------------------------------
# 运行镜像
# -----------------------------------------------------------------------------
FROM alpine:3.21

# ca-certificates：出站 SMTP（STARTTLS/TLS）与 HTTPS 依赖根证书
# tzdata + /etc/localtime：容器时区固定 UTC；应用内部（DSN Loc=UTC）本就全程 UTC，
#                         日志时间戳与数据库时间保持一致
RUN apk add --no-cache ca-certificates tzdata \
    && cp /usr/share/zoneinfo/UTC /etc/localtime \
    && echo "UTC" > /etc/timezone \
    && addgroup -S rollin \
    && adduser -S -G rollin rollin

ENV TZ=UTC
WORKDIR /app
COPY --from=builder /out/rollin-server /app/rollin-server

# 非 root 运行：容器内不监听特权端口，也不需要写文件系统
USER rollin

# HTTP_ADDRESS 默认 :8080（internal/config/config.go）。
# 若通过环境变量修改端口，必须同步修改下面的健康检查目标端口。
EXPOSE 8080

# 健康检查打在后端自带的 GET /healthz（无鉴权、零副作用，router.go 注册）
HEALTHCHECK --interval=30s --timeout=3s --start-period=15s --retries=3 \
    CMD wget -q --spider http://127.0.0.1:8080/healthz || exit 1

ENTRYPOINT ["/app/rollin-server"]
