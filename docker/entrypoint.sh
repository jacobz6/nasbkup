#!/bin/bash
# =============================================================================
# NAS Backup 容器启动脚本
# 职责:
#   1. 首次启动: 若 /app/data 为空，生成 master.key 与默认 rclone.conf 模板
#   2. 校验关键数据文件存在
#   3. 启动 nginx (前台) + 后端服务 (后台)
#   4. 捕获 SIGTERM/SIGINT 并优雅关闭所有进程
# =============================================================================

set -eu

DATA_DIR="${DATA_DIR:-/app/data}"
LOG_DIR="${DATA_DIR}/logs"
KEY_FILE="${KEY_FILE:-${DATA_DIR}/master.key}"
RCLONE_CONF="${RCLONE_CONF:-${DATA_DIR}/rclone.conf}"
NAS_BACKUP_BIN="/usr/local/bin/nas-backup"
CONFIG_FILE="${CONFIG_FILE:-/app/config.yaml}"

# -----------------------------------------------------------------------------
# 颜色输出
# -----------------------------------------------------------------------------
log()  { printf "\033[1;34m[entrypoint]\033[0m %s\n" "$*"; }
warn() { printf "\033[1;33m[entrypoint]\033[0m %s\n" "$*" >&2; }
err()  { printf "\033[1;31m[entrypoint]\033[0m %s\n" "$*" >&2; }

# -----------------------------------------------------------------------------
# 1. 初始化数据目录
# -----------------------------------------------------------------------------
log "数据目录: ${DATA_DIR}"
mkdir -p "${DATA_DIR}" "${LOG_DIR}"

# -----------------------------------------------------------------------------
# 2. 首次启动生成主密钥 (32 字节随机，base64 编码)
#    警告: 此密钥是恢复加密备份数据的唯一凭证，请务必备份到容器外！
# -----------------------------------------------------------------------------
if [ ! -f "${KEY_FILE}" ]; then
    log "首次启动，生成加密主密钥: ${KEY_FILE}"
    if command -v openssl >/dev/null 2>&1; then
        openssl rand -base64 32 > "${KEY_FILE}"
    else
        # fallback: /dev/urandom
        head -c 32 /dev/urandom | base64 > "${KEY_FILE}"
    fi
    chmod 600 "${KEY_FILE}"
    warn "=========================================================="
    warn "  已生成主密钥: ${KEY_FILE}"
    warn "  丢失此密钥将无法解密任何备份数据！"
    warn "  请立即将密钥文件备份到安全位置。"
    warn "=========================================================="
fi

# -----------------------------------------------------------------------------
# 3. 首次启动生成 rclone.conf 模板
#    用户需通过 Web UI 或手动编辑填写 OSS 凭证
# -----------------------------------------------------------------------------
if [ ! -f "${RCLONE_CONF}" ]; then
    log "首次启动，生成 rclone.conf 模板: ${RCLONE_CONF}"
    cat > "${RCLONE_CONF}" <<'EOF'
# rclone configuration for NAS Backup
# 在 Web UI 的"策略设置 -> 上传"页配置 OSS 凭证后会自动重写此文件
# 也可手动编辑后重启容器

[oss]
type = s3
provider = Aliyun
access_key_id = REPLACE_WITH_YOUR_ACCESS_KEY_ID
secret_access_key = REPLACE_WITH_YOUR_ACCESS_KEY_SECRET
endpoint = oss-cn-hangzhou.aliyuncs.com
acl = private

[oss-crypt]
type = crypt
remote = oss:your-bucket-name/nas-backup
filename_encryption = standard
directory_name_encryption = true
password = REPLACE_WITH_RCLONE_CRYPT_PASSWORD
EOF
    chmod 600 "${RCLONE_CONF}"
    warn "=========================================================="
    warn "  已生成 rclone.conf 模板，请配置 OSS 凭证后重启容器"
    warn "  路径: ${RCLONE_CONF}"
    warn "=========================================================="
fi

# -----------------------------------------------------------------------------
# 4. 校验后端二进制与配置文件
# -----------------------------------------------------------------------------
if [ ! -x "${NAS_BACKUP_BIN}" ]; then
    err "未找到后端二进制: ${NAS_BACKUP_BIN}"
    exit 1
fi

if [ ! -f "${CONFIG_FILE}" ]; then
    err "未找到配置文件: ${CONFIG_FILE}"
    exit 1
fi

# -----------------------------------------------------------------------------
# 5. 信号处理: 优雅关闭 nginx 与后端
# -----------------------------------------------------------------------------
shutdown() {
    log "收到终止信号，正在关闭..."
    # nginx 收到信号后会优雅退出
    if [ -n "${NGINX_PID:-}" ] && kill -0 "${NGINX_PID}" 2>/dev/null; then
        kill -TERM "${NGINX_PID}" 2>/dev/null || true
    fi
    # 后端进程
    if [ -n "${BACKEND_PID:-}" ] && kill -0 "${BACKEND_PID}" 2>/dev/null; then
        kill -TERM "${BACKEND_PID}" 2>/dev/null || true
        # 等待最多 30 秒
        i=0
        while kill -0 "${BACKEND_PID}" 2>/dev/null && [ $i -lt 30 ]; do
            sleep 1
            i=$((i+1))
        done
        # 强制 kill
        kill -KILL "${BACKEND_PID}" 2>/dev/null || true
    fi
    log "已退出"
    exit 0
}
trap shutdown TERM INT

# -----------------------------------------------------------------------------
# 6. 启动后端 (后台)
# -----------------------------------------------------------------------------
log "启动 NAS Backup 后端服务..."
${NAS_BACKUP_BIN} -config "${CONFIG_FILE}" > "${LOG_DIR}/nas-backup-stdout.log" 2>&1 &
BACKEND_PID=$!
log "后端 PID: ${BACKEND_PID}"

# 等待后端就绪
i=0
while [ $i -lt 30 ]; do
    if ! kill -0 "${BACKEND_PID}" 2>/dev/null; then
        err "后端进程异常退出，查看日志: ${LOG_DIR}/nas-backup-stdout.log"
        tail -n 30 "${LOG_DIR}/nas-backup-stdout.log" >&2 || true
        exit 1
    fi
    if curl -fsS "http://127.0.0.1:8080/api/dashboard/stats" -o /dev/null 2>/dev/null; then
        log "后端就绪 ✓"
        break
    fi
    sleep 1
    i=$((i+1))
done

if [ $i -ge 30 ]; then
    warn "后端启动超时，仍继续启动 nginx (30s 内 API 不可用)"
fi

# -----------------------------------------------------------------------------
# 7. 启动 nginx (前台，阻塞)
# -----------------------------------------------------------------------------
log "启动 Nginx (前端 + 反向代理)..."
nginx -g "daemon off;" &
NGINX_PID=$!
log "Nginx PID: ${NGINX_PID}"

# 等待任一进程退出
wait -n "${BACKEND_PID}" "${NGINX_PID}" 2>/dev/null || true

# 如果其中之一退出，触发整体关闭
log "检测到子进程退出，准备关闭..."
shutdown
