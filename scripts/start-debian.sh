#!/bin/bash
# ==============================================================================
# NAS Backup System - Debian 生产环境 一键启动 / 停止 / 重启 / 状态 脚本
# ==============================================================================
# 假定生产环境已通过 deploy-debian.sh 部署完成：
#   - 后端二进制:        <INSTALL_DIR>/nas-backup-backend/nas-backup
#   - 后端配置:          <INSTALL_DIR>/nas-backup-backend/config.yaml
#   - 主密钥:            <INSTALL_DIR>/nas-backup-backend/data/master.key
#   - rclone:            <INSTALL_DIR>/nas-backup-backend/bin/rclone
#   - 前端构建产物:      <INSTALL_DIR>/nas-backup-frontend/dist/
#   - systemd 服务:      /etc/systemd/system/nas-backup.service
#   - Nginx 配置:        /etc/nginx/{conf.d|server.d|sites-enabled}/nas-backup.conf
#
# 架构说明:
#   - 后端通过 systemd 服务 nas-backup 管理（监听 127.0.0.1:8080）
#   - 前端由 Nginx 托管静态文件（监听 0.0.0.0:9000），并反向代理 /api/ 到 8080
#   - 因此本脚本只需管理两个对象: nas-backup 服务 + nginx 服务
#   - 重要: stop 命令只停后端，不停 nginx（NAS 系统 Nginx 通常被多方共享）
#
# 用法:
#   sudo ./scripts/start-debian.sh           # 默认 = start
#   sudo ./scripts/start-debian.sh start     # 启动后端服务 + 启动/重载 Nginx
#   sudo ./scripts/start-debian.sh stop      # 仅停止后端服务（保留 Nginx）
#   sudo ./scripts/start-debian.sh stop-all  # 停止后端 + 停止 Nginx（慎用）
#   sudo ./scripts/start-debian.sh restart   # 重启后端 + reload Nginx
#   sudo ./scripts/start-debian.sh status    # 查看运行状态 + API 健康检查
#   sudo ./scripts/start-debian.sh logs      # 实时查看后端日志 (Ctrl+C 退出)
#   sudo ./scripts/start-debian.sh logs be   # 仅后端 systemd journal
#   sudo ./scripts/start-debian.sh logs app  # 仅应用日志文件
#   sudo ./scripts/start-debian.sh logs fe   # 仅 Nginx error 日志
#   sudo ./scripts/start-debian.sh reload    # 仅 reload Nginx（不改后端）
#   sudo ./scripts/start-debian.sh health    # API 健康检查
# ==============================================================================

set -euo pipefail

# ------------------------------------------------------------------------------
# 颜色输出
# ------------------------------------------------------------------------------
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
BOLD='\033[1m'
NC='\033[0m'

info()    { echo -e "${BLUE}[INFO]${NC}  $*"; }
success() { echo -e "${GREEN}[OK]${NC}    $*"; }
warn()    { echo -e "${YELLOW}[WARN]${NC}  $*"; }
fail()    { echo -e "${RED}[FAIL]${NC}  $*"; exit 1; }
step()    { echo -e "\n${BOLD}>>> $*${NC}"; }

# ------------------------------------------------------------------------------
# 路径自动检测（与 deploy-debian.sh 逻辑保持一致）
# ------------------------------------------------------------------------------
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DETECTED_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

if [[ -d "${DETECTED_ROOT}/nas-backup-backend" && -d "${DETECTED_ROOT}/nas-backup-frontend" ]]; then
    INSTALL_DIR="$DETECTED_ROOT"
else
    INSTALL_DIR="/opt/nas-backup"
fi

BACKEND_DIR="${INSTALL_DIR}/nas-backup-backend"
FRONTEND_DIR="${INSTALL_DIR}/nas-backup-frontend"
DATA_DIR="${BACKEND_DIR}/data"
CONFIG_FILE="${BACKEND_DIR}/config.yaml"
APP_LOG_FILE="${DATA_DIR}/logs/nas-backup.log"
BACKEND_BIN="${BACKEND_DIR}/nas-backup"

SERVICE_NAME="nas-backup"
NGINX_SERVICE="nginx"

# 端口（与 deploy-debian.sh / nginx 配置一致）
BACKEND_PORT=8080
FRONTEND_PORT=9000
BACKEND_HOST="127.0.0.1"

# 从 config.yaml 读取端口（兜底默认 8080）
if [[ -f "$CONFIG_FILE" ]]; then
    PARSED_PORT=$(awk '
        /^server:/ { in_server=1; next }
        /^[a-z]+:/ { in_server=0 }
        in_server && /^[[:space:]]*port:/ {
            gsub(/[^0-9]/, "", $2); print $2; exit
        }
    ' "$CONFIG_FILE" 2>/dev/null || true)
    [[ -n "${PARSED_PORT:-}" ]] && BACKEND_PORT="$PARSED_PORT"
fi

# ------------------------------------------------------------------------------
# 权限检查：systemctl / nginx 操作需要 root
# ------------------------------------------------------------------------------
ensure_root() {
    if [[ $EUID -ne 0 ]]; then
        warn "本命令需要 root 权限来操作系统服务（systemctl / nginx）"
        warn "正在尝试通过 sudo 重新执行..."
        exec sudo -E bash "$0" "$@"
    fi
}

# ------------------------------------------------------------------------------
# 工具函数
# ------------------------------------------------------------------------------
# 判断某 systemd 服务是否 active
svc_active() {
    systemctl is-active --quiet "$1" 2>/dev/null
}

# 判断某 systemd 服务是否 enabled（开机自启）
svc_enabled() {
    systemctl is-enabled --quiet "$1" 2>/dev/null
}

# 判断某端口是否被监听
port_listening() {
    local port="$1"
    ss -tlnp 2>/dev/null | grep -q ":${port} " || netstat -tlnp 2>/dev/null | grep -q ":${port} "
}

# 等待端口监听，超时返回 1
wait_port() {
    local port="$1"
    local timeout="${2:-15}"
    local elapsed=0
    while ! port_listening "$port"; do
        sleep 1
        elapsed=$((elapsed + 1))
        if [[ $elapsed -ge $timeout ]]; then
            return 1
        fi
    done
    return 0
}

# 等待 systemd 服务进入 active 状态
wait_svc_active() {
    local svc="$1"
    local timeout="${2:-15}"
    local elapsed=0
    while ! svc_active "$svc"; do
        sleep 1
        elapsed=$((elapsed + 1))
        if [[ $elapsed -ge $timeout ]]; then
            return 1
        fi
    done
    return 0
}

# HTTP 健康检查（返回 HTTP 状态码，000 表示连接失败）
http_code() {
    local url="$1"
    curl -sS -o /dev/null -w "%{http_code}" --connect-timeout 3 --max-time 5 "$url" 2>/dev/null || echo "000"
}

# 获取服务器主 IP（用于展示访问地址）
get_server_ip() {
    hostname -I 2>/dev/null | awk '{print $1}' || echo "YOUR_SERVER_IP"
}

# ------------------------------------------------------------------------------
# 预检
# ------------------------------------------------------------------------------
preflight() {
    step "环境预检"
    [[ -f /etc/os-release ]] || warn "无法识别操作系统（非 Debian 系？）"

    local missing=()
    [[ -x "$BACKEND_BIN" ]]            || missing+=("后端二进制: ${BACKEND_BIN}")
    [[ -f "$CONFIG_FILE" ]]            || missing+=("配置文件: ${CONFIG_FILE}")
    [[ -f "${DATA_DIR}/master.key" ]]  || missing+=("主密钥: ${DATA_DIR}/master.key")
    [[ -d "${FRONTEND_DIR}/dist" ]]    || missing+=("前端构建: ${FRONTEND_DIR}/dist/")
    [[ -f "/etc/systemd/system/${SERVICE_NAME}.service" ]] || missing+=("systemd 服务: /etc/systemd/system/${SERVICE_NAME}.service")

    if [[ ${#missing[@]} -gt 0 ]]; then
        echo -e "${RED}环境缺失，无法启动：${NC}"
        printf '  - %s\n' "${missing[@]}"
        echo ""
        echo "请先运行部署脚本: sudo ./scripts/deploy-debian.sh"
        exit 1
    fi

    success "安装目录:     ${INSTALL_DIR}"
    success "后端二进制:   $(basename "$BACKEND_BIN")"
    success "后端配置:     config.yaml (端口 ${BACKEND_PORT})"
    success "主密钥:       data/master.key"
    success "前端构建:     dist/ 已就绪"
    success "systemd 服务: ${SERVICE_NAME}.service"
    command -v nginx >/dev/null && success "Nginx:        $(nginx -v 2>&1)" || warn "Nginx 未安装，前端无法托管"
}

# ------------------------------------------------------------------------------
# 启动后端 systemd 服务
# ------------------------------------------------------------------------------
start_backend() {
    if svc_active "$SERVICE_NAME"; then
        warn "后端服务 ${SERVICE_NAME} 已在运行，跳过"
        return 0
    fi

    info "启动后端服务 ${SERVICE_NAME} ..."
    systemctl start "$SERVICE_NAME"

    info "等待服务进入 active 状态 ..."
    if wait_svc_active "$SERVICE_NAME" 20; then
        success "后端服务已启动"
    else
        warn "后端服务 20 秒内未进入 active 状态"
        warn "最近 15 行 journal 日志:"
        journalctl -u "$SERVICE_NAME" -n 15 --no-pager 2>/dev/null | sed 's/^/    /' || true
        fail "请排查后通过 sudo ./scripts/start-debian.sh restart 重试"
    fi

    # 等待端口监听
    info "等待后端监听端口 ${BACKEND_PORT} ..."
    if wait_port "$BACKEND_PORT" 15; then
        success "后端端口 ${BACKEND_PORT} 已监听"
    else
        warn "后端 15 秒内未开始监听端口（可能仍在初始化 OSS / 数据库）"
        warn "最近 15 行 journal 日志:"
        journalctl -u "$SERVICE_NAME" -n 15 --no-pager 2>/dev/null | sed 's/^/    /' || true
    fi
}

# ------------------------------------------------------------------------------
# 启动 / 重载 Nginx
# ------------------------------------------------------------------------------
start_nginx() {
    if ! command -v nginx >/dev/null; then
        warn "Nginx 未安装，跳过前端托管（后端 API 仍可用: http://127.0.0.1:${BACKEND_PORT}）"
        return 0
    fi

    # 测试配置有效性
    if ! nginx -t 2>&1; then
        fail "Nginx 配置测试失败，请检查后重试"
    fi

    if svc_active "$NGINX_SERVICE"; then
        # 已在运行：reload 以加载可能的配置变更（restart 在 UGREEN NAS 上会触发 reset_nginx_config 钩子）
        info "Nginx 已在运行，执行 reload 以加载最新配置 ..."
        systemctl reload "$NGINX_SERVICE" 2>/dev/null || nginx -s reload 2>&1
        success "Nginx 已 reload"
    else
        info "启动 Nginx 服务 ..."
        systemctl start "$NGINX_SERVICE" 2>/dev/null || service nginx start 2>/dev/null || { nginx 2>&1 || fail "Nginx 启动失败"; }
        if wait_svc_active "$NGINX_SERVICE" 10; then
            success "Nginx 已启动"
        else
            warn "Nginx 服务状态异常，但进程可能已启动（继续检查端口）"
        fi
    fi

    # 检查 9000 端口
    sleep 1
    if port_listening "$FRONTEND_PORT"; then
        success "Nginx 端口 ${FRONTEND_PORT} 已监听"
    else
        warn "Nginx 端口 ${FRONTEND_PORT} 未监听"
        warn "可能原因:"
        warn "  1. UGREEN NAS reset_nginx_config 钩子清掉了 nas-backup.conf"
        warn "  2. 端口 ${FRONTEND_PORT} 被其他服务占用"
        warn "  3. nginx.conf 未 include 对应目录"
        warn "排查: sudo nginx -T 2>/dev/null | grep -A 20 'listen.*${FRONTEND_PORT}'"
    fi
}

# ------------------------------------------------------------------------------
# 启动全部
# ------------------------------------------------------------------------------
do_start() {
    preflight
    start_backend
    start_nginx

    local server_ip
    server_ip=$(get_server_ip)

    echo ""
    echo -e "${GREEN}${BOLD}======================================================================${NC}"
    echo -e "${GREEN}${BOLD}  NAS Backup System 已启动${NC}"
    echo -e "${GREEN}${BOLD}======================================================================${NC}"
    echo -e "  ${BOLD}Web UI (局域网):${NC}  http://${server_ip}:${FRONTEND_PORT}"
    echo -e "  ${BOLD}Web UI (本机):${NC}    http://127.0.0.1:${FRONTEND_PORT}"
    echo -e "  ${BOLD}后端 API:${NC}         http://127.0.0.1:${BACKEND_PORT}"
    echo -e "  ${BOLD}API 代理:${NC}         /api → http://127.0.0.1:${BACKEND_PORT}"
    echo ""
    echo -e "  ${BOLD}停止:${NC}  sudo ./scripts/start-debian.sh stop"
    echo -e "  ${BOLD}状态:${NC}  sudo ./scripts/start-debian.sh status"
    echo -e "  ${BOLD}日志:${NC}  sudo ./scripts/start-debian.sh logs"
    echo -e "  ${BOLD}健康:${NC}  sudo ./scripts/start-debian.sh health"
    echo ""
}

# ------------------------------------------------------------------------------
# 停止后端服务（保留 Nginx）
# ------------------------------------------------------------------------------
do_stop() {
    step "停止后端服务（保留 Nginx）"
    if svc_active "$SERVICE_NAME"; then
        info "停止 ${SERVICE_NAME} ..."
        systemctl stop "$SERVICE_NAME"
        sleep 1
        if svc_active "$SERVICE_NAME"; then
            warn "服务仍在运行，等待 5 秒后强制检查 ..."
            sleep 5
        fi
        if svc_active "$SERVICE_NAME"; then
            fail "后端服务未能停止，请排查: systemctl status ${SERVICE_NAME}"
        else
            success "后端服务已停止"
        fi
    else
        info "后端服务未在运行"
    fi

    if command -v nginx >/dev/null && svc_active "$NGINX_SERVICE"; then
        info "Nginx 保留运行（NAS 系统共享，未停止）"
        info "如需停止 Nginx，请使用: sudo ./scripts/start-debian.sh stop-all"
    fi
}

# ------------------------------------------------------------------------------
# 停止后端 + Nginx（慎用）
# ------------------------------------------------------------------------------
do_stop_all() {
    step "停止后端 + Nginx（慎用：会影响 NAS 系统其他 Web 服务）"
    do_stop
    if command -v nginx >/dev/null && svc_active "$NGINX_SERVICE"; then
        info "停止 Nginx ..."
        systemctl stop "$NGINX_SERVICE" 2>/dev/null || service nginx stop 2>/dev/null || nginx -s stop 2>&1
        sleep 1
        if svc_active "$NGINX_SERVICE"; then
            warn "Nginx 仍在运行，可能被 NAS 系统守护"
        else
            success "Nginx 已停止"
        fi
    else
        info "Nginx 未在运行"
    fi
}

# ------------------------------------------------------------------------------
# 重启（后端 restart + Nginx reload）
# ------------------------------------------------------------------------------
do_restart() {
    preflight
    step "重启后端服务"
    systemctl restart "$SERVICE_NAME"
    if wait_svc_active "$SERVICE_NAME" 20; then
        success "后端服务已重启"
    else
        fail "后端服务重启失败，请排查: journalctl -u ${SERVICE_NAME} -n 50"
    fi
    if wait_port "$BACKEND_PORT" 15; then
        success "后端端口 ${BACKEND_PORT} 已监听"
    fi
    start_nginx
    success "重启完成"
}

# ------------------------------------------------------------------------------
# 仅 reload Nginx
# ------------------------------------------------------------------------------
do_reload() {
    step "Reload Nginx"
    if ! command -v nginx >/dev/null; then
        fail "Nginx 未安装"
    fi
    nginx -t 2>&1 || fail "Nginx 配置测试失败"
    systemctl reload "$NGINX_SERVICE" 2>/dev/null || nginx -s reload 2>&1
    success "Nginx 已 reload"
}

# ------------------------------------------------------------------------------
# 状态
# ------------------------------------------------------------------------------
do_status() {
    echo -e "\n${BOLD}NAS Backup System 运行状态${NC}"
    echo "-----------------------------------"

    # 后端 systemd 服务
    if svc_active "$SERVICE_NAME"; then
        local pid
        pid=$(systemctl show -p MainPID --value "$SERVICE_NAME" 2>/dev/null || echo "?")
        local enabled_str="未启用开机自启"
        svc_enabled "$SERVICE_NAME" && enabled_str="已启用开机自启"
        if port_listening "$BACKEND_PORT"; then
            echo -e "后端服务:  ${GREEN}运行中${NC}  PID ${pid}  端口 ${BACKEND_PORT} (监听中)  [${enabled_str}]"
        else
            echo -e "后端服务:  ${YELLOW}运行中${NC}  PID ${pid}  端口 ${BACKEND_PORT} (未监听，可能正在初始化)  [${enabled_str}]"
        fi
    else
        echo -e "后端服务:  ${RED}未运行${NC}  (systemctl start ${SERVICE_NAME})"
    fi

    # Nginx
    if command -v nginx >/dev/null; then
        if svc_active "$NGINX_SERVICE"; then
            if port_listening "$FRONTEND_PORT"; then
                echo -e "Nginx:      ${GREEN}运行中${NC}  端口 ${FRONTEND_PORT} (监听中)"
            else
                echo -e "Nginx:      ${YELLOW}运行中${NC}  端口 ${FRONTEND_PORT} (未监听，配置可能被 NAS 系统重置)"
            fi
        else
            echo -e "Nginx:      ${RED}未运行${NC}  (systemctl start ${NGINX_SERVICE})"
        fi
    else
        echo -e "Nginx:      ${RED}未安装${NC}"
    fi

    echo ""
}

# ------------------------------------------------------------------------------
# API 健康检查
# ------------------------------------------------------------------------------
do_health() {
    step "API 健康检查"

    # 后端直连
    local be_code
    be_code=$(http_code "http://127.0.0.1:${BACKEND_PORT}/api/dashboard/stats")
    if [[ "$be_code" == "200" ]]; then
        success "后端 API (直连 8080):    HTTP 200 ✓"
    else
        warn "后端 API (直连 8080):    HTTP ${be_code} ✗"
    fi

    # 存储健康
    local storage_resp
    storage_resp=$(curl -sS --connect-timeout 3 --max-time 5 "http://127.0.0.1:${BACKEND_PORT}/api/storage/health" 2>/dev/null || echo "{}")
    if echo "$storage_resp" | grep -q '"status":"ok"'; then
        success "存储/OSS 连接:          健康 ✓"
    else
        warn "存储/OSS 连接:          异常 ✗  响应: ${storage_resp:0:200}"
    fi

    # Nginx 代理
    if command -v nginx >/dev/null && port_listening "$FRONTEND_PORT"; then
        local fe_code
        fe_code=$(http_code "http://127.0.0.1:${FRONTEND_PORT}/")
        if [[ "$fe_code" == "200" ]]; then
            success "前端首页 (Nginx 9000):   HTTP 200 ✓"
        else
            warn "前端首页 (Nginx 9000):   HTTP ${fe_code} ✗"
        fi

        local proxy_code
        proxy_code=$(http_code "http://127.0.0.1:${FRONTEND_PORT}/api/dashboard/stats")
        if [[ "$proxy_code" == "200" ]]; then
            success "API 代理 (/api → 8080): HTTP 200 ✓"
        else
            warn "API 代理 (/api → 8080): HTTP ${proxy_code} ✗"
        fi
    else
        warn "Nginx/前端不可达，跳过代理检查"
    fi

    echo ""
}

# ------------------------------------------------------------------------------
# 日志
# ------------------------------------------------------------------------------
do_logs() {
    local target="${1:-all}"
    case "$target" in
        all)
            info "同时跟踪后端 journal + 应用日志 + Nginx error (Ctrl+C 退出)"
            # journalctl -f 会阻塞，串行不可行；这里默认跟应用日志（最常用）
            # 如需 journal，请用 logs be
            if [[ -f "$APP_LOG_FILE" ]]; then
                info "应用日志: ${APP_LOG_FILE}"
                tail -n 100 -F "$APP_LOG_FILE"
            else
                warn "应用日志不存在: ${APP_LOG_FILE}，回退到 journal"
                journalctl -u "$SERVICE_NAME" -f -n 100
            fi
            ;;
        be|backend|journal)
            info "跟踪 ${SERVICE_NAME} systemd journal (Ctrl+C 退出)"
            journalctl -u "$SERVICE_NAME" -f -n 100
            ;;
        app)
            if [[ -f "$APP_LOG_FILE" ]]; then
                info "跟踪应用日志: ${APP_LOG_FILE} (Ctrl+C 退出)"
                tail -n 100 -F "$APP_LOG_FILE"
            else
                warn "应用日志不存在: ${APP_LOG_FILE}"
                fail "请确认服务已启动且 logging.file_path 配置正确"
            fi
            ;;
        fe|nginx)
            info "跟踪 Nginx error 日志 (Ctrl+C 退出)"
            local ng_err="/var/log/nginx/error.log"
            [[ -f "$ng_err" ]] || fail "Nginx error 日志不存在: ${ng_err}"
            tail -n 100 -F "$ng_err"
            ;;
        *)
            fail "未知日志目标: $target (可选: all | be | app | fe)"
            ;;
    esac
}

# ------------------------------------------------------------------------------
# 用法
# ------------------------------------------------------------------------------
usage() {
    cat <<EOF
NAS Backup System - Debian 生产环境管理脚本

用法:
  sudo ./scripts/start-debian.sh [命令]

命令:
  start        启动后端服务 + 启动/重载 Nginx (默认)
  stop         仅停止后端服务（保留 Nginx，避免影响 NAS 系统其他 Web 服务）
  stop-all     停止后端 + 停止 Nginx（慎用）
  restart      重启后端 + reload Nginx
  reload       仅 reload Nginx（不重启后端）
  status       查看运行状态
  health       API 健康检查（后端直连 + 存储健康 + Nginx 代理）
  logs [目标]  实时查看日志
                 目标: all (默认=应用日志) | be (systemd journal) | app (应用日志) | fe (Nginx error)
  help         显示此帮助

示例:
  sudo ./scripts/start-debian.sh
  sudo ./scripts/start-debian.sh start
  sudo ./scripts/start-debian.sh restart
  sudo ./scripts/start-debian.sh logs be
  sudo ./scripts/start-debian.sh health

注意:
  - 操作 systemd / Nginx 需要 root 权限，脚本会自动通过 sudo 提权
  - stop 默认不停 Nginx（NAS 系统 Nginx 常被多方共享）
  - Nginx 配置变更使用 reload 而非 restart（UGREEN NAS restart 会触发 reset_nginx_config 清理自定义配置）
EOF
}

# ------------------------------------------------------------------------------
# 入口
# ------------------------------------------------------------------------------
COMMAND="${1:-start}"
case "$COMMAND" in
    start)     ensure_root "$@";     do_start ;;
    stop)      ensure_root "$@";     do_stop ;;
    stop-all)  ensure_root "$@";     do_stop_all ;;
    restart)   ensure_root "$@";     do_restart ;;
    reload)    ensure_root "$@";     do_reload ;;
    status)    ensure_root "$@";     do_status ;;
    health)    ensure_root "$@";     do_health ;;
    logs)      ensure_root "$@";     shift; do_logs "${1:-all}" ;;
    help|-h|--help) usage ;;
    *)
        echo "未知命令: $COMMAND"
        usage
        exit 1
        ;;
esac
