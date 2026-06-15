# =============================================================================
# NAS Backup - Multi-stage Dockerfile
# 阶段说明:
#   1. frontend-builder: 构建 React 前端静态资源
#   2. backend-builder:  编译 Go 后端二进制 (CGO 启用以支持 go-sqlite3)
#   3. runtime:          基于 debian-slim 集成 nginx + rclone + zstd + 二进制
# =============================================================================

# -----------------------------------------------------------------------------
# 阶段 1: 前端构建
# -----------------------------------------------------------------------------
FROM node:20-bookworm-slim AS frontend-builder

WORKDIR /build/frontend

# 先复制依赖文件以利用 Docker 缓存
COPY nas-backup-frontend/package*.json ./
RUN npm ci --no-audit --no-fund

# 复制源码并构建
COPY nas-backup-frontend/ ./
RUN npm run build


# -----------------------------------------------------------------------------
# 阶段 2: 后端编译
# -----------------------------------------------------------------------------
FROM golang:1.25-bookworm AS backend-builder

# 启用 CGO 所需的构建工具 (go-sqlite3 需要 CGO)
RUN apt-get update && apt-get install -y --no-install-recommends \
        gcc libc6-dev \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /build/backend

# 先复制 go.mod/go.sum 以利用缓存
COPY nas-backup-backend/go.mod nas-backup-backend/go.sum ./
RUN go mod download

# 复制源码并编译
COPY nas-backup-backend/ ./
# CGO_ENABLED=1: go-sqlite3 需要
# -ldflags "-s -w": 去除符号表与调试信息，减小体积
# -trimpath: 去除本地路径信息
RUN CGO_ENABLED=1 GOOS=linux go build \
        -ldflags="-s -w" \
        -trimpath \
        -o /build/nas-backup \
        ./cmd/nas-backup


# -----------------------------------------------------------------------------
# 阶段 3: 运行时镜像
# -----------------------------------------------------------------------------
FROM debian:bookworm-slim AS runtime

# 元信息
LABEL org.opencontainers.image.title="nas-backup" \
      org.opencontainers.image.description="NAS 自动化备份系统 (后端 + 前端一体)" \
      org.opencontainers.image.source="https://example.com/nasbkup_system"

# 安装运行时依赖:
#   - nginx:   提供前端静态资源服务 + API 反向代理
#   - rclone:  用于上传/下载 OSS 对象
#   - zstd:    压缩/解压
#   - sqlite3: 调试/手动维护数据库 (可选)
#   - tzdata:  时区支持
#   - ca-certificates: HTTPS 证书
#   - dumb-init: 正确的 PID 1 信号处理
RUN apt-get update && apt-get install -y --no-install-recommends \
        nginx \
        rclone \
        zstd \
        sqlite3 \
        tzdata \
        ca-certificates \
        dumb-init \
        curl \
    && rm -rf /var/lib/apt/lists/* \
    && ln -sf /usr/share/zoneinfo/Asia/Shanghai /etc/localtime \
    && echo "Asia/Shanghai" > /etc/timezone

# 创建非 root 用户运行应用 (更安全)
# 但因需要读取宿主机挂载的 NAS 目录，仍保留 root 能力
# 如需更严格隔离可改为: --user 1000:1000 并匹配宿主机 UID
RUN groupadd -r app && useradd -r -g app -d /app -s /sbin/nologin app

# 应用目录
WORKDIR /app

# 复制后端二进制
COPY --from=backend-builder /build/nas-backup /usr/local/bin/nas-backup
RUN chmod +x /usr/local/bin/nas-backup

# 复制前端构建产物到 nginx 静态目录
COPY --from=frontend-builder /build/frontend/dist /usr/share/nginx/html

# 复制 nginx 配置 (覆盖默认配置)
COPY docker/nginx.conf /etc/nginx/conf.d/default.conf
RUN rm -f /etc/nginx/sites-enabled/default

# 复制容器配置 + 启动脚本
COPY docker/config.docker.yaml /app/config.yaml
COPY docker/entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh \
    && mkdir -p /app/data/logs \
    && chown -R app:app /app /usr/share/nginx/html /var/log/nginx /var/lib/nginx \
    && touch /var/run/nginx.pid \
    && chown app:app /var/run/nginx.pid

# 数据卷声明
VOLUME ["/app/data"]

# 暴露端口
#   80  - nginx (前端 + API 统一入口)
#   8080 - 后端 API (可选直接访问)
EXPOSE 80 8080

# 健康检查: 探测 API 端点 (同时验证 nginx + 后端可用)
HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
    CMD curl -fsS http://127.0.0.1:80/api/dashboard/stats -o /dev/null || exit 1

# dumb-init 作为 PID 1 正确转发信号
ENTRYPOINT ["/usr/bin/dumb-init", "--", "/usr/local/bin/entrypoint.sh"]

# 默认命令 (entrypoint 已处理)
CMD [""]
