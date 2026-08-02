#!/bin/bash
# ==============================================================================
# NAS Backup System - macOS 一键启动 / 停止 / 重启 / 状态 脚本
# ==============================================================================
# 假定环境已部署完成（deploy-macos.sh 已跑过或手工配置好）：
#   - nas-backup-backend/nas-backup      二进制已构建
#   - nas-backup-backend/config.yaml     配置文件已生成
#   - nas-backup-backend/data/master.key 主密钥已生成
#   - nas-backup-backend/bin/rclone      rclone 已就位
#   - nas-backup-frontend/node_modules   前端依赖已安装
#
# 用法:
#   ./scripts/start-macos.sh           # 默认 = start
#   ./scripts/start-macos.sh start     # 后台启动后端 + 前端
#   ./scripts/start-macos.sh stop      # 停止全部
#   ./scripts/start-macos.sh restart   # 重启
#   ./scripts/start-macos.sh status    # 查看运行状态
#   ./scripts/start-macos.sh logs      # 实时查看日志 (Ctrl+C 退出)
#   ./scripts/start-macos.sh logs be   # 仅后端日志
#   ./scripts/start-macos.sh logs fe   # 仅前端日志
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
# 路径
# ------------------------------------------------------------------------------
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
BACKEND_DIR="${PROJECT_ROOT}/nas-backup-backend"
FRONTEND_DIR="${PROJECT_ROOT}/nas-backup-frontend"
RUN_DIR="${SCRIPT_DIR}/.run"                       # PID 与 stdout 日志目录
BACKEND_BIN="${BACKEND_DIR}/nas-backup"
BACKEND_CONFIG="${BACKEND_DIR}/config.yaml"
BACKEND_LOG_DIR="${BACKEND_DIR}/data/logs"
BACKEND_STDOUT_LOG="${RUN_DIR}/backend.stdout.log"
FRONTEND_STDOUT_LOG="${RUN_DIR}/frontend.stdout.log"
BACKEND_PID_FILE="${RUN_DIR}/backend.pid"
FRONTEND_PID_FILE="${RUN_DIR}/frontend.pid"

BACKEND_PORT=8080
FRONTEND_PORT=5173
BACKEND_HOST="127.0.0.1"

# 从 config.yaml 读取端口（兜底默认 8080）
if [[ -f "$BACKEND_CONFIG" ]]; then
    PARSED_PORT=$(awk '
        /^server:/ { in_server=1; next }
        /^[a-z]+:/ { in_server=0 }
        in_server && /^[[:space:]]*port:/ {
            gsub(/[^0-9]/, "", $2); print $2; exit
        }
    ' "$BACKEND_CONFIG" 2>/dev/null || true)
    [[ -n "${PARSED_PORT:-}" ]] && BACKEND_PORT="$PARSED_PORT"
fi

# npm 可执行路径（兼容 nvm / homebrew）
export PATH="${HOME}/.nvm/versions/node/$(ls -1 "${HOME}/.nvm/versions/node/" 2>/dev/null | tail -1)/bin:/opt/homebrew/bin:/usr/local/bin:${PATH}"

# ------------------------------------------------------------------------------
# 工具函数
# ------------------------------------------------------------------------------
mkdir -p "$RUN_DIR" "$BACKEND_LOG_DIR"

# 判断某 PID 是否存活
pid_alive() {
    local pid="$1"
    [[ -z "$pid" ]] && return 1
    kill -0 "$pid" 2>/dev/null
}

# 判断某端口是否被监听
port_listening() {
    local port="$1"
    lsof -nP -iTCP:"$port" -sTCP:LISTEN >/dev/null 2>&1
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

# 读取 PID 文件并校验存活；返回 1 表示进程未运行
check_pid_file() {
    local pid_file="$1"
    if [[ ! -f "$pid_file" ]]; then
        return 1
    fi
    local pid
    pid=$(cat "$pid_file" 2>/dev/null || true)
    if [[ -z "$pid" ]] || ! pid_alive "$pid"; then
        rm -f "$pid_file"
        return 1
    fi
    echo "$pid"
    return 0
}

# ------------------------------------------------------------------------------
# 预检
# ------------------------------------------------------------------------------
preflight() {
    step "环境预检"
    [[ "$(uname -s)" == "Darwin" ]] || fail "本脚本仅支持 macOS"

    local missing=()
    [[ -x "$BACKEND_BIN" ]]            || missing+=("后端二进制: ${BACKEND_BIN}")
    [[ -f "$BACKEND_CONFIG" ]]         || missing+=("配置文件: ${BACKEND_CONFIG}")
    [[ -f "${BACKEND_DIR}/data/master.key" ]] || missing+=("主密钥: data/master.key")
    [[ -x "${BACKEND_DIR}/bin/rclone" ]] || warn "rclone 不在 bin/ 下（main.go 会自动 EnsureRcloneConfig，但建议放置）"
    [[ -d "${FRONTEND_DIR}/node_modules" ]] || missing+=("前端依赖: nas-backup-frontend/node_modules")

    # npm / node 必须可用
    command -v node >/dev/null || missing+=("node 可执行文件")
    command -v npm  >/dev/null || missing+=("npm 可执行文件")

    if [[ ${#missing[@]} -gt 0 ]]; then
        echo -e "${RED}环境缺失，无法启动：${NC}"
        printf '  - %s\n' "${missing[@]}"
        echo ""
        echo "请先运行: ./scripts/deploy-macos.sh --skip-deps  完成部署"
        exit 1
    fi

    success "后端二进制:   $(basename "$BACKEND_BIN")"
    success "后端配置:     config.yaml (端口 ${BACKEND_PORT})"
    success "主密钥:       data/master.key"
    success "前端依赖:     node_modules 已就绪"
    success "Node:         $(node --version)  npm: $(npm --version)"
}

# ------------------------------------------------------------------------------
# 启动后端
# ------------------------------------------------------------------------------
start_backend() {
    if pid=$(check_pid_file "$BACKEND_PID_FILE"); then
        warn "后端已在运行 (PID ${pid})，跳过"
        return 0
    fi

    if port_listening "$BACKEND_PORT"; then
        fail "端口 ${BACKEND_PORT} 已被占用，但 PID 文件不存在。请先排查: lsof -nP -iTCP:${BACKEND_PORT} -sTCP:LISTEN"
    fi

    info "启动后端 -> http://${BACKEND_HOST}:${BACKEND_PORT}"
    cd "$BACKEND_DIR"
    nohup "$BACKEND_BIN" -config "$BACKEND_CONFIG" \
        >"$BACKEND_STDOUT_LOG" 2>&1 &
    local pid=$!
    echo "$pid" >"$BACKEND_PID_FILE"
    disown "$pid" 2>/dev/null || true

    info "等待后端监听端口 ${BACKEND_PORT} ..."
    if wait_port "$BACKEND_PORT" 20; then
        success "后端已启动 (PID ${pid})"
    else
        warn "后端 20 秒内未开始监听端口，可能仍在初始化 OSS / 数据库"
        warn "最近 15 行日志:"
        tail -n 15 "$BACKEND_STDOUT_LOG" 2>/dev/null | sed 's/^/    /'
        if ! pid_alive "$pid"; then
            fail "后端进程已退出，请查看日志: ${BACKEND_STDOUT_LOG}"
        fi
    fi
}

# ------------------------------------------------------------------------------
# 启动前端 (Vite dev server)
# ------------------------------------------------------------------------------
start_frontend() {
    if pid=$(check_pid_file "$FRONTEND_PID_FILE"); then
        warn "前端已在运行 (PID ${pid})，跳过"
        return 0
    fi

    if port_listening "$FRONTEND_PORT"; then
        fail "端口 ${FRONTEND_PORT} 已被占用，但 PID 文件不存在。请先排查: lsof -nP -iTCP:${FRONTEND_PORT} -sTCP:LISTEN"
    fi

    info "启动前端 Vite dev server -> http://localhost:${FRONTEND_PORT}"
    cd "$FRONTEND_DIR"
    # 使用项目本地 vite，避免全局 vite 版本不匹配（之前在 NAS 上踩过坑）
    nohup npm run dev -- --port "${FRONTEND_PORT}" --strictPort \
        >"$FRONTEND_STDOUT_LOG" 2>&1 &
    local pid=$!
    echo "$pid" >"$FRONTEND_PID_FILE"
    disown "$pid" 2>/dev/null || true

    info "等待前端监听端口 ${FRONTEND_PORT} ..."
    if wait_port "$FRONTEND_PORT" 30; then
        success "前端已启动 (PID ${pid})"
    else
        warn "前端 30 秒内未开始监听端口，请查看日志: ${FRONTEND_STDOUT_LOG}"
        warn "最近 15 行日志:"
        tail -n 15 "$FRONTEND_STDOUT_LOG" 2>/dev/null | sed 's/^/    /'
        if ! pid_alive "$pid"; then
            fail "前端进程已退出，请查看日志: ${FRONTEND_STDOUT_LOG}"
        fi
    fi
}

# ------------------------------------------------------------------------------
# 启动全部
# ------------------------------------------------------------------------------
do_start() {
    preflight
    start_backend
    start_frontend
    echo ""
    echo -e "${GREEN}${BOLD}======================================================================${NC}"
    echo -e "${GREEN}${BOLD}  NAS Backup System 已启动${NC}"
    echo -e "${GREEN}${BOLD}======================================================================${NC}"
    echo -e "  ${BOLD}前端 Web UI:${NC}  http://localhost:${FRONTEND_PORT}"
    echo -e "  ${BOLD}后端 API:${NC}     http://${BACKEND_HOST}:${BACKEND_PORT}"
    echo -e "  ${BOLD}API 代理:${NC}     /api → http://${BACKEND_HOST}:${BACKEND_PORT}"
    echo ""
    echo -e "  ${BOLD}停止:${NC}  ./scripts/start-macos.sh stop"
    echo -e "  ${BOLD}状态:${NC}  ./scripts/start-macos.sh status"
    echo -e "  ${BOLD}日志:${NC}  ./scripts/start-macos.sh logs"
    echo ""
}

# ------------------------------------------------------------------------------
# 停止单个进程（带优雅退出 + 强杀兜底）
# ------------------------------------------------------------------------------
stop_one() {
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
    if pid_alive "$pid"; then
        fail "${name} 未能停止 (PID ${pid})"
    else
        success "${name} 已停止"
    fi
}

do_stop() {
    step "停止服务"
    stop_one "$FRONTEND_PID_FILE" "前端"
    stop_one "$BACKEND_PID_FILE"  "后端"
}

# ------------------------------------------------------------------------------
# 重启
# ------------------------------------------------------------------------------
do_restart() {
    do_stop
    do_start
}

# ------------------------------------------------------------------------------
# 状态
# ------------------------------------------------------------------------------
do_status() {
    echo -e "\n${BOLD}NAS Backup System 运行状态${NC}"
    echo "-----------------------------------"

    # 后端
    if pid=$(check_pid_file "$BACKEND_PID_FILE"); then
        if port_listening "$BACKEND_PORT"; then
            echo -e "后端:  ${GREEN}运行中${NC}  PID ${pid}  端口 ${BACKEND_PORT} (监听中)"
        else
            echo -e "后端:  ${YELLOW}运行中${NC}  PID ${pid}  端口 ${BACKEND_PORT} (未监听，可能正在初始化)"
        fi
    else
        if port_listening "$BACKEND_PORT"; then
            echo -e "后端:  ${YELLOW}未知进程占用端口 ${BACKEND_PORT}${NC}  (PID 文件不存在)"
        else
            echo -e "后端:  ${RED}未运行${NC}"
        fi
    fi

    # 前端
    if pid=$(check_pid_file "$FRONTEND_PID_FILE"); then
        if port_listening "$FRONTEND_PORT"; then
            echo -e "前端:  ${GREEN}运行中${NC}  PID ${pid}  端口 ${FRONTEND_PORT} (监听中)"
        else
            echo -e "前端:  ${YELLOW}运行中${NC}  PID ${pid}  端口 ${FRONTEND_PORT} (未监听，可能正在启动)"
        fi
    else
        if port_listening "$FRONTEND_PORT"; then
            echo -e "前端:  ${YELLOW}未知进程占用端口 ${FRONTEND_PORT}${NC}  (PID 文件不存在)"
        else
            echo -e "前端:  ${RED}未运行${NC}"
        fi
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
            info "同时跟踪前后端日志 (Ctrl+C 退出)"
            tail -n 50 -F "$BACKEND_STDOUT_LOG" "$FRONTEND_STDOUT_LOG" 2>/dev/null || \
                warn "日志文件不存在，服务可能尚未启动"
            ;;
        be|backend)
            tail -n 100 -F "$BACKEND_STDOUT_LOG" 2>/dev/null || \
                warn "后端日志不存在: ${BACKEND_STDOUT_LOG}"
            ;;
        fe|frontend)
            tail -n 100 -F "$FRONTEND_STDOUT_LOG" 2>/dev/null || \
                warn "前端日志不存在: ${FRONTEND_STDOUT_LOG}"
            ;;
        *)
            fail "未知日志目标: $target (可选: all | be | fe)"
            ;;
    esac
}

# ------------------------------------------------------------------------------
# 用法
# ------------------------------------------------------------------------------
usage() {
    cat <<EOF
NAS Backup System - macOS 一键启动脚本

用法:
  ./scripts/start-macos.sh [命令]

命令:
  start      启动后端 + 前端 (默认)
  stop       停止全部
  restart    重启
  status     查看运行状态
  logs [目标]  实时查看日志
                目标: all (默认) | be (后端) | fe (前端)
  help       显示此帮助

示例:
  ./scripts/start-macos.sh
  ./scripts/start-macos.sh start
  ./scripts/start-macos.sh logs be
EOF
}

# ------------------------------------------------------------------------------
# 入口
# ------------------------------------------------------------------------------
COMMAND="${1:-start}"
case "$COMMAND" in
    start)   do_start ;;
    stop)    do_stop ;;
    restart) do_restart ;;
    status)  do_status ;;
    logs)    shift; do_logs "${1:-all}" ;;
    help|-h|--help) usage ;;
    *)
        echo "未知命令: $COMMAND"
        usage
        exit 1
        ;;
esac
