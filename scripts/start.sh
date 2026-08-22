#!/bin/bash
# ==============================================================================
# NAS Backup System - 统一启停脚本（macOS + Debian 自动适配）
# ==============================================================================
# 自动检测操作系统，按平台管理服务：
#
#   macOS  (本地开发/测试):
#     - 后端: nohup + PID 文件（无 systemd）
#     - 前端: Vite dev server（nohup + PID 文件）
#     - 进程管理: PID 文件 + 优雅退出 + 强杀兜底
#
#   Debian (生产 NAS):
#     - 后端: systemctl 管理 nas-backup 服务（监听 127.0.0.1:8080）
#     - 前端: Nginx 托管静态文件（监听 0.0.0.0:9000）+ 反向代理 /api/
#     - stop 只停后端，不停 Nginx（NAS 系统 Nginx 常被多方共享）
#     - Nginx 使用 reload 而非 restart（UGREEN NAS restart 会触发 reset_nginx_config 钩子）
#
# 用法:
#   ./scripts/start.sh                 # 默认 = start
#   sudo ./scripts/start.sh start      # 启动（Debian 需 root 操作 systemd）
#   ./scripts/start.sh stop            # 停止
#   ./scripts/start.sh restart         # 重启
#   ./scripts/start.sh status          # 查看状态
#   ./scripts/start.sh health          # API 健康检查
#   ./scripts/start.sh logs [目标]     # 实时日志
#                                       目标: all | be | app | fe
#   ./scripts/start.sh help            # 显示帮助
# ==============================================================================

set -euo pipefail

# ------------------------------------------------------------------------------
# 加载公共函数库
# ------------------------------------------------------------------------------
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib/common.sh
source "${SCRIPT_DIR}/lib/common.sh"

enhance_path
detect_platform
detect_paths

# 端口与服务名
BACKEND_PORT="$(parse_backend_port || echo "8080")"
[[ -z "$BACKEND_PORT" ]] && BACKEND_PORT=8080
BACKEND_HOST="127.0.0.1"

if [[ "$OS_TYPE" == "macos" ]]; then
    FRONTEND_PORT=5173
    RUN_DIR="${SCRIPT_DIR}/.run"
    BACKEND_STDOUT_LOG="${RUN_DIR}/backend.stdout.log"
    FRONTEND_STDOUT_LOG="${RUN_DIR}/frontend.stdout.log"
    BACKEND_PID_FILE="${RUN_DIR}/backend.pid"
    FRONTEND_PID_FILE="${RUN_DIR}/frontend.pid"
    mkdir -p "$RUN_DIR" "${DATA_DIR}/logs"
else
    FRONTEND_PORT=9000
    SERVICE_NAME="nas-backup"
    NGINX_SERVICE="nginx"
fi

# npm 路径（macOS + nvm 兼容）
if [[ "$OS_TYPE" == "macos" ]]; then
    export PATH="${HOME}/.nvm/versions/node/$(ls -1 "${HOME}/.nvm/versions/node/" 2>/dev/null | tail -1)/bin:/opt/homebrew/bin:/usr/local/bin:${PATH}"
fi

BACKEND_BIN="${BACKEND_DIR}/nas-backup"

# ------------------------------------------------------------------------------
# 权限检查（Debian 模式 systemctl 需要 root）
# ------------------------------------------------------------------------------
ensure_root() {
    if [[ "$OS_TYPE" != "macos" ]] && [[ $EUID -ne 0 ]]; then
        warn "Debian 模式下操作 systemd/nginx 需要 root 权限"
        warn "正在通过 sudo 重新执行..."
        exec sudo -E bash "$0" "$@"
    fi
}

# ------------------------------------------------------------------------------
# PID 文件操作（macOS 模式）
# ------------------------------------------------------------------------------
check_pid_file() {
    local pid_file="$1"
    [[ ! -f "$pid_file" ]] && return 1
    local pid
    pid=$(cat "$pid_file" 2>/dev/null || true)
    if [[ -z "$pid" ]] || ! pid_alive "$pid"; then
        rm -f "$pid_file"
        return 1
    fi
    echo "$pid"
    return 0
}

# ==============================================================================
# macOS 模式函数
# ==============================================================================

_macos_preflight() {
    step "环境预检"
    [[ "$(uname -s)" == "Darwin" ]] || fail "macOS 模式仅支持 macOS"

    local missing=()
    [[ -x "$BACKEND_BIN" ]] || missing+=("后端二进制: ${BACKEND_BIN}")
    [[ -f "$CONFIG_FILE" ]] || missing+=("配置文件: ${CONFIG_FILE}")
    [[ -f "${CONFIG_DIR}/master.key" ]] || missing+=("主密钥: ${CONFIG_DIR#${PROJECT_ROOT}/}/master.key")

    if [[ ${#missing[@]} -gt 0 ]]; then
        echo -e "${RED}环境缺失:${NC}"
        printf '  - %s\n' "${missing[@]}"
        echo "请先运行: ./scripts/deploy.sh"
        exit 1
    fi

    success "后端二进制: $(basename "$BACKEND_BIN")"
    success "配置端口: ${BACKEND_PORT}"
    success "主密钥: config/master.key"
    success "前端依赖: node_modules 已就绪"
    success "Node: $(node --version)  npm: $(npm --version)"
}

_macos_start_backend() {
    if pid=$(check_pid_file "$BACKEND_PID_FILE"); then
        warn "后端已在运行 (PID ${pid})，跳过"
        return 0
    fi
    if port_listening "$BACKEND_PORT"; then
        fail "端口 ${BACKEND_PORT} 已被占用，但 PID 文件不存在。排查: lsof -nP -iTCP:${BACKEND_PORT} -sTCP:LISTEN"
    fi

    info "启动后端 -> http://${BACKEND_HOST}:${BACKEND_PORT}"
    cd "$BACKEND_DIR"
    nohup "$BACKEND_BIN" -config "$CONFIG_FILE" >"$BACKEND_STDOUT_LOG" 2>&1 &
    local pid=$!
    echo "$pid" >"$BACKEND_PID_FILE"
    disown "$pid" 2>/dev/null || true

    info "等待后端监听端口 ${BACKEND_PORT} ..."
    if wait_port "$BACKEND_PORT" 20; then
        success "后端已启动 (PID ${pid})"
    else
        warn "后端 20 秒内未监听端口（可能仍在初始化）"
        warn "最近 15 行日志:"
        tail -n 15 "$BACKEND_STDOUT_LOG" 2>/dev/null | sed 's/^/    /'
        if ! pid_alive "$pid"; then
            fail "后端进程已退出，日志: ${BACKEND_STDOUT_LOG}"
        fi
    fi
}

_macos_start_frontend() {
    if pid=$(check_pid_file "$FRONTEND_PID_FILE"); then
        warn "前端已在运行 (PID ${pid})，跳过"
        return 0
    fi
    if port_listening "$FRONTEND_PORT"; then
        fail "端口 ${FRONTEND_PORT} 已被占用。排查: lsof -nP -iTCP:${FRONTEND_PORT} -sTCP:LISTEN"
    fi

    info "启动前端 Vite dev server -> http://localhost:${FRONTEND_PORT}"
    cd "$FRONTEND_DIR"
    nohup npm run dev -- --port "${FRONTEND_PORT}" --strictPort >"$FRONTEND_STDOUT_LOG" 2>&1 &
    local pid=$!
    echo "$pid" >"$FRONTEND_PID_FILE"
    disown "$pid" 2>/dev/null || true

    info "等待前端监听端口 ${FRONTEND_PORT} ..."
    if wait_port "$FRONTEND_PORT" 30; then
        success "前端已启动 (PID ${pid})"
    else
        warn "前端 30 秒内未监听端口"
        warn "最近 15 行日志:"
        tail -n 15 "$FRONTEND_STDOUT_LOG" 2>/dev/null | sed 's/^/    /'
        if ! pid_alive "$pid"; then
            fail "前端进程已退出，日志: ${FRONTEND_STDOUT_LOG}"
        fi
    fi
}

_macos_stop_one() {
    local pid_file="$1"
    local name="$2"

    if ! pid=$(check_pid_file "$pid_file"); then
        info "${name} 未在运行"
        return 0
    fi

    info "停止 ${name} (PID ${pid}) ..."
    kill -TERM "$pid" 2>/dev/null || true
    local waited=0
    while pid_alive "$pid"; do
        sleep 0.5
        waited=$((waited + 1))
        if [[ $waited -ge 20 ]]; then
            warn "${name} 10 秒未退出，强制 kill -9"
            kill -9 "$pid" 2>/dev/null || true
            sleep 1
            break
        fi
    done
    rm -f "$pid_file"
    pid_alive "$pid" && fail "${name} 未能停止 (PID ${pid})" || success "${name} 已停止"
}

# ==============================================================================
# Debian 模式函数
# ==============================================================================

_debian_preflight() {
    step "环境预检"
    local missing=()
    [[ -x "$BACKEND_BIN" ]] || missing+=("后端二进制: ${BACKEND_BIN}")
    [[ -f "$CONFIG_FILE" ]] || missing+=("配置文件: ${CONFIG_FILE}")
    [[ -f "${CONFIG_DIR}/master.key" ]] || missing+=("主密钥: ${CONFIG_DIR}/master.key")
    [[ -d "${FRONTEND_DIR}/dist" ]] || missing+=("前端构建: ${FRONTEND_DIR}/dist/")
    [[ -f "/etc/systemd/system/${SERVICE_NAME}.service" ]] || missing+=("systemd 服务")

    if [[ ${#missing[@]} -gt 0 ]]; then
        echo -e "${RED}环境缺失:${NC}"
        printf '  - %s\n' "${missing[@]}"
        echo "请先运行: sudo ./scripts/deploy.sh"
        exit 1
    fi

    success "安装目录: ${PROJECT_ROOT}"
    success "后端二进制: $(basename "$BACKEND_BIN")"
    success "配置端口: ${BACKEND_PORT}"
    success "主密钥: config/master.key"
    success "前端构建: dist/ 已就绪"
    success "systemd 服务: ${SERVICE_NAME}.service"
    command -v nginx >/dev/null && success "Nginx: $(nginx -v 2>&1)" || warn "Nginx 未安装"
}

_debian_start_backend() {
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
        warn "后端 20 秒内未进入 active 状态"
        warn "最近 15 行 journal:"
        journalctl -u "$SERVICE_NAME" -n 15 --no-pager 2>/dev/null | sed 's/^/    /' || true
        fail "请排查后通过 sudo ./scripts/start.sh restart 重试"
    fi

    info "等待后端监听端口 ${BACKEND_PORT} ..."
    if wait_port "$BACKEND_PORT" 15; then
        success "后端端口 ${BACKEND_PORT} 已监听"
    else
        warn "后端 15 秒内未监听端口（可能仍在初始化 OSS / 数据库）"
    fi
}

_debian_start_nginx() {
    if ! command -v nginx >/dev/null; then
        warn "Nginx 未安装，跳过（后端 API 仍可用: http://127.0.0.1:${BACKEND_PORT}）"
        return 0
    fi

    nginx -t 2>&1 || fail "Nginx 配置测试失败"

    if svc_active "$NGINX_SERVICE"; then
        # 已运行: reload（UGREEN NAS restart 会触发 reset_nginx_config 清理自定义配置）
        info "Nginx 已在运行，执行 reload ..."
        systemctl reload "$NGINX_SERVICE" 2>/dev/null || nginx -s reload 2>&1
        success "Nginx 已 reload"
    else
        info "启动 Nginx ..."
        systemctl start "$NGINX_SERVICE" 2>/dev/null || service nginx start 2>/dev/null || { nginx 2>&1 || fail "Nginx 启动失败"; }
        if wait_svc_active "$NGINX_SERVICE" 10; then
            success "Nginx 已启动"
        else
            warn "Nginx 服务状态异常，继续检查端口"
        fi
    fi

    sleep 1
    if port_listening "$FRONTEND_PORT"; then
        success "Nginx 端口 ${FRONTEND_PORT} 已监听"
    else
        warn "Nginx 端口 ${FRONTEND_PORT} 未监听"
        warn "可能原因: UGREEN reset_nginx_config 钩子 / 端口被占用 / include 路径未检测"
        warn "排查: sudo nginx -T 2>/dev/null | grep -A 20 'listen.*${FRONTEND_PORT}'"
    fi
}

# ==============================================================================
# 统一命令实现（按平台分发）
# ==============================================================================

do_start() {
    if [[ "$OS_TYPE" == "macos" ]]; then
        _macos_preflight
        _macos_start_backend
        _macos_start_frontend
    else
        _debian_preflight
        _debian_start_backend
        _debian_start_nginx
    fi

    local server_ip
    server_ip=$(get_server_ip)

    echo ""
    echo -e "${GREEN}${BOLD}======================================================================${NC}"
    echo -e "${GREEN}${BOLD}  NAS Backup System 已启动${NC}"
    echo -e "${GREEN}${BOLD}======================================================================${NC}"
    if [[ "$OS_TYPE" == "macos" ]]; then
        echo -e "  ${BOLD}前端 Web UI:${NC}  http://localhost:${FRONTEND_PORT}"
        echo -e "  ${BOLD}后端 API:${NC}     http://${BACKEND_HOST}:${BACKEND_PORT}"
    else
        echo -e "  ${BOLD}Web UI (局域网):${NC}  http://${server_ip}:${FRONTEND_PORT}"
        echo -e "  ${BOLD}Web UI (本机):${NC}    http://127.0.0.1:${FRONTEND_PORT}"
        echo -e "  ${BOLD}后端 API:${NC}         http://${BACKEND_HOST}:${BACKEND_PORT}"
    fi
    echo ""
    echo -e "  ${BOLD}停止:${NC}  ./scripts/start.sh stop"
    echo -e "  ${BOLD}状态:${NC}  ./scripts/start.sh status"
    echo -e "  ${BOLD}日志:${NC}  ./scripts/start.sh logs"
    [[ "$OS_TYPE" != "macos" ]] && echo -e "  ${BOLD}健康:${NC}  ./scripts/start.sh health"
    echo ""
}

do_stop() {
    if [[ "$OS_TYPE" == "macos" ]]; then
        step "停止服务"
        _macos_stop_one "$FRONTEND_PID_FILE" "前端"
        _macos_stop_one "$BACKEND_PID_FILE"  "后端"
    else
        step "停止后端服务（保留 Nginx）"
        if svc_active "$SERVICE_NAME"; then
            info "停止 ${SERVICE_NAME} ..."
            systemctl stop "$SERVICE_NAME"
            sleep 1
            if svc_active "$SERVICE_NAME"; then
                warn "服务仍在运行，等待 5 秒..."
                sleep 5
            fi
            svc_active "$SERVICE_NAME" && fail "后端未能停止: systemctl status ${SERVICE_NAME}" || success "后端服务已停止"
        else
            info "后端服务未在运行"
        fi

        if command -v nginx >/dev/null && svc_active "$NGINX_SERVICE"; then
            info "Nginx 保留运行（NAS 系统共享，未停止）"
            info "如需停止 Nginx: sudo ./scripts/start.sh stop-all"
        fi
    fi
}

do_stop_all() {
    if [[ "$OS_TYPE" == "macos" ]]; then
        do_stop
    else
        step "停止后端 + Nginx（慎用：会影响 NAS 系统其他 Web 服务）"
        do_stop
        if command -v nginx >/dev/null && svc_active "$NGINX_SERVICE"; then
            info "停止 Nginx ..."
            systemctl stop "$NGINX_SERVICE" 2>/dev/null || service nginx stop 2>/dev/null || nginx -s stop 2>&1
            sleep 1
            svc_active "$NGINX_SERVICE" && warn "Nginx 仍在运行，可能被 NAS 系统守护" || success "Nginx 已停止"
        else
            info "Nginx 未在运行"
        fi
    fi
}

do_restart() {
    if [[ "$OS_TYPE" == "macos" ]]; then
        do_stop
        do_start
    else
        _debian_preflight
        step "重启后端服务"
        systemctl restart "$SERVICE_NAME"
        if wait_svc_active "$SERVICE_NAME" 20; then
            success "后端服务已重启"
        else
            fail "后端重启失败: journalctl -u ${SERVICE_NAME} -n 50"
        fi
        wait_port "$BACKEND_PORT" 15 && success "后端端口 ${BACKEND_PORT} 已监听"
        _debian_start_nginx
        success "重启完成"
    fi
}

do_reload() {
    if [[ "$OS_TYPE" == "macos" ]]; then
        warn "macOS 模式不支持 reload，请使用 restart"
        return
    fi
    step "Reload Nginx"
    command -v nginx >/dev/null || fail "Nginx 未安装"
    nginx -t 2>&1 || fail "Nginx 配置测试失败"
    systemctl reload "$NGINX_SERVICE" 2>/dev/null || nginx -s reload 2>&1
    success "Nginx 已 reload"
}

do_status() {
    echo -e "\n${BOLD}NAS Backup System 运行状态${NC}"
    echo "-----------------------------------"

    if [[ "$OS_TYPE" == "macos" ]]; then
        # 后端
        if pid=$(check_pid_file "$BACKEND_PID_FILE"); then
            if port_listening "$BACKEND_PORT"; then
                echo -e "后端:  ${GREEN}运行中${NC}  PID ${pid}  端口 ${BACKEND_PORT} (监听中)"
            else
                echo -e "后端:  ${YELLOW}运行中${NC}  PID ${pid}  端口 ${BACKEND_PORT} (未监听，可能正在初始化)"
            fi
        else
            port_listening "$BACKEND_PORT" \
                && echo -e "后端:  ${YELLOW}未知进程占用端口 ${BACKEND_PORT}${NC}" \
                || echo -e "后端:  ${RED}未运行${NC}"
        fi

        # 前端
        if pid=$(check_pid_file "$FRONTEND_PID_FILE"); then
            if port_listening "$FRONTEND_PORT"; then
                echo -e "前端:  ${GREEN}运行中${NC}  PID ${pid}  端口 ${FRONTEND_PORT} (监听中)"
            else
                echo -e "前端:  ${YELLOW}运行中${NC}  PID ${pid}  端口 ${FRONTEND_PORT} (未监听，可能正在启动)"
            fi
        else
            port_listening "$FRONTEND_PORT" \
                && echo -e "前端:  ${YELLOW}未知进程占用端口 ${FRONTEND_PORT}${NC}" \
                || echo -e "前端:  ${RED}未运行${NC}"
        fi
    else
        # Debian: 后端 systemd 服务
        if svc_active "$SERVICE_NAME"; then
            local pid
            pid=$(systemctl show -p MainPID --value "$SERVICE_NAME" 2>/dev/null || echo "?")
            local enabled_str="未启用开机自启"
            svc_enabled "$SERVICE_NAME" && enabled_str="已启用开机自启"
            if port_listening "$BACKEND_PORT"; then
                echo -e "后端服务:  ${GREEN}运行中${NC}  PID ${pid}  端口 ${BACKEND_PORT} (监听中)  [${enabled_str}]"
            else
                echo -e "后端服务:  ${YELLOW}运行中${NC}  PID ${pid}  端口 ${BACKEND_PORT} (未监听)  [${enabled_str}]"
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
    fi
    echo ""
}

do_health() {
    if [[ "$OS_TYPE" == "macos" ]]; then
        warn "macOS 模式无独立健康检查，请使用 status"
        do_status
        return
    fi

    step "API 健康检查"

    # 后端直连
    local be_code
    be_code=$(http_code "http://127.0.0.1:${BACKEND_PORT}/api/dashboard/stats")
    if [[ "$be_code" == "200" ]]; then
        success "后端 API (直连 ${BACKEND_PORT}):    HTTP 200 ✓"
    else
        warn "后端 API (直连 ${BACKEND_PORT}):    HTTP ${be_code} ✗"
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
            success "前端首页 (Nginx ${FRONTEND_PORT}):   HTTP 200 ✓"
        else
            warn "前端首页 (Nginx ${FRONTEND_PORT}):   HTTP ${fe_code} ✗"
        fi

        local proxy_code
        proxy_code=$(http_code "http://127.0.0.1:${FRONTEND_PORT}/api/dashboard/stats")
        if [[ "$proxy_code" == "200" ]]; then
            success "API 代理 (/api → ${BACKEND_PORT}): HTTP 200 ✓"
        else
            warn "API 代理 (/api → ${BACKEND_PORT}): HTTP ${proxy_code} ✗"
        fi
    else
        warn "Nginx/前端不可达，跳过代理检查"
    fi
    echo ""
}

do_logs() {
    local target="${1:-all}"
    case "$target" in
        all)
            if [[ "$OS_TYPE" == "macos" ]]; then
                info "同时跟踪前后端日志 (Ctrl+C 退出)"
                tail -n 50 -F "$BACKEND_STDOUT_LOG" "$FRONTEND_STDOUT_LOG" 2>/dev/null || \
                    warn "日志文件不存在，服务可能尚未启动"
            else
                info "默认跟踪应用日志（最常用）"
                if [[ -f "$APP_LOG_FILE" ]]; then
                    info "应用日志: ${APP_LOG_FILE}"
                    tail -n 100 -F "$APP_LOG_FILE"
                else
                    warn "应用日志不存在: ${APP_LOG_FILE}，回退到 journal"
                    journalctl -u "$SERVICE_NAME" -f -n 100
                fi
            fi
            ;;
        be|backend|journal)
            if [[ "$OS_TYPE" == "macos" ]]; then
                tail -n 100 -F "$BACKEND_STDOUT_LOG" 2>/dev/null || warn "后端日志不存在: ${BACKEND_STDOUT_LOG}"
            else
                info "跟踪 ${SERVICE_NAME} systemd journal (Ctrl+C 退出)"
                journalctl -u "$SERVICE_NAME" -f -n 100
            fi
            ;;
        app)
            if [[ "$OS_TYPE" == "macos" ]]; then
                tail -n 100 -F "$BACKEND_STDOUT_LOG" 2>/dev/null || warn "后端日志不存在: ${BACKEND_STDOUT_LOG}"
            else
                if [[ -f "$APP_LOG_FILE" ]]; then
                    info "跟踪应用日志: ${APP_LOG_FILE} (Ctrl+C 退出)"
                    tail -n 100 -F "$APP_LOG_FILE"
                else
                    fail "应用日志不存在: ${APP_LOG_FILE}"
                fi
            fi
            ;;
        fe|frontend|nginx)
            if [[ "$OS_TYPE" == "macos" ]]; then
                tail -n 100 -F "$FRONTEND_STDOUT_LOG" 2>/dev/null || warn "前端日志不存在: ${FRONTEND_STDOUT_LOG}"
            else
                info "跟踪 Nginx error 日志 (Ctrl+C 退出)"
                local ng_err="/var/log/nginx/error.log"
                [[ -f "$ng_err" ]] || fail "Nginx error 日志不存在: ${ng_err}"
                tail -n 100 -F "$ng_err"
            fi
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
NAS Backup System - 统一启停脚本（${OS_TYPE} 模式）

用法:
  $([ "$OS_TYPE" == "macos" ] && echo "./scripts/start.sh" || echo "sudo ./scripts/start.sh") [命令]

命令:
  start        启动服务 (默认)
  stop         停止服务$([ "$OS_TYPE" != "macos" ] && echo "（保留 Nginx）")
  stop-all     停止后端 + Nginx（Debian 模式，慎用）
  restart      重启服务
  reload       仅 reload Nginx（Debian 模式）
  status       查看运行状态
  health       API 健康检查（Debian 模式）
  logs [目标]  实时查看日志
                 目标: all (默认) | be (后端) | app (应用日志) | fe (前端/Nginx)
  help         显示此帮助

示例:
  $([ "$OS_TYPE" == "macos" ] && echo "./scripts/start.sh" || echo "sudo ./scripts/start.sh") start
  $([ "$OS_TYPE" == "macos" ] && echo "./scripts/start.sh" || echo "sudo ./scripts/start.sh") logs be
$([ "$OS_TYPE" != "macos" ] && echo "  sudo ./scripts/start.sh health")

注意:
$([ "$OS_TYPE" == "macos" ] \
    && echo "  - macOS 模式使用 nohup + PID 文件管理进程" \
    || echo "  - Debian 模式操作 systemd/nginx 需要 root 权限
  - stop 默认不停 Nginx（NAS 系统 Nginx 常被多方共享）
  - Nginx 配置变更使用 reload 而非 restart（UGREEN NAS 会触发 reset_nginx_config）")
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
