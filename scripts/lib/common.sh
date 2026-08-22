#!/bin/bash
# ==============================================================================
# NAS Backup System - Shell 公共函数库
# ==============================================================================
# 被 deploy.sh / start.sh 通过 source 引入，提供：
#   - 颜色输出（info / success / warn / fail / step）
#   - 路径检测（项目根、后端/前端目录、数据目录）
#   - 平台检测（macOS / Debian / 其他 Linux）
#   - 端口检测（跨平台：lsof / ss / netstat）
#   - HTTP 健康检查
#   - 端口等待
#
# 用法（在其他脚本顶部）：
#   SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
#   # shellcheck source=lib/common.sh
#   source "${SCRIPT_DIR}/lib/common.sh"
# ==============================================================================

# 防止重复 source
[[ -n "${_NAS_BACKUP_COMMON_LOADED:-}" ]] && return 0
_NAS_BACKUP_COMMON_LOADED=1

# ------------------------------------------------------------------------------
# 颜色输出（所有终端通用）
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
# 平台检测
# 返回：OS_TYPE = "macos" | "debian" | "linux-other"
#       GOOS = "darwin" | "linux"
#       GOARCH = "amd64" | "arm64" | "arm"
#       RCLONE_ARCH = "amd64" | "arm64" | "arm-v7"
# ------------------------------------------------------------------------------
detect_platform() {
    OS_TYPE=""
    GOOS="$(uname -s | tr '[:upper:]' '[:lower:]')"
    ARCH="$(uname -m)"

    case "$GOOS" in
        darwin)
            OS_TYPE="macos"
            GOOS="darwin"
            ;;
        linux)
            if [[ -f /etc/os-release ]]; then
                . /etc/os-release 2>/dev/null
                if [[ "${ID:-}" == "debian" ]] || [[ "${ID_LIKE:-}" == *debian* ]]; then
                    OS_TYPE="debian"
                else
                    OS_TYPE="linux-other"
                fi
            else
                OS_TYPE="linux-other"
            fi
            ;;
        *)
            fail "不支持的操作系统: $(uname -s)"
            ;;
    esac

    case "$ARCH" in
        x86_64|amd64) GOARCH="amd64"; RCLONE_ARCH="amd64" ;;
        arm64|aarch64) GOARCH="arm64"; RCLONE_ARCH="arm64" ;;
        armv7l|armhf)  GOARCH="arm";   RCLONE_ARCH="arm-v7" ;;
        *)             fail "不支持的架构: $ARCH" ;;
    esac

    export OS_TYPE GOOS GOARCH RCLONE_ARCH ARCH
}

# ------------------------------------------------------------------------------
# 路径检测
# 设置全局变量：PROJECT_ROOT / BACKEND_DIR / FRONTEND_DIR / DATA_DIR / CONFIG_FILE
# 参数（可选）：$1 = 指定的安装目录（用于 Debian 生产部署）
# ------------------------------------------------------------------------------
detect_paths() {
    local install_dir="${1:-}"

    SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[1]:-${BASH_SOURCE[0]}}")" && pwd)"

    if [[ -z "$install_dir" ]]; then
        local detected_root
        detected_root="$(cd "${SCRIPT_DIR}/.." && pwd)"
        if [[ -d "${detected_root}/nas-backup-backend" && -d "${detected_root}/nas-backup-frontend" ]]; then
            PROJECT_ROOT="$detected_root"
        else
            PROJECT_ROOT="/opt/nas-backup"
        fi
    else
        PROJECT_ROOT="$install_dir"
    fi

    BACKEND_DIR="${PROJECT_ROOT}/nas-backup-backend"
    FRONTEND_DIR="${PROJECT_ROOT}/nas-backup-frontend"
    DATA_DIR="${BACKEND_DIR}/data"
    CONFIG_DIR="${BACKEND_DIR}/config"
    CONFIG_FILE="${CONFIG_DIR}/config.yaml"
    APP_LOG_FILE="${DATA_DIR}/logs/nas-backup.log"

    export PROJECT_ROOT BACKEND_DIR FRONTEND_DIR DATA_DIR CONFIG_DIR CONFIG_FILE APP_LOG_FILE
}

# ------------------------------------------------------------------------------
# 端口检测（跨平台）
# macOS 使用 lsof，Linux 使用 ss（优先）/ netstat（兜底）
# 参数：$1 = 端口号
# 返回：0 = 监听中，1 = 未监听
# ------------------------------------------------------------------------------
port_listening() {
    local port="$1"
    if [[ "$OS_TYPE" == "macos" ]] || command -v lsof &>/dev/null; then
        lsof -nP -iTCP:"$port" -sTCP:LISTEN >/dev/null 2>&1
    else
        ss -tlnp 2>/dev/null | grep -q ":${port} " || netstat -tlnp 2>/dev/null | grep -q ":${port} "
    fi
}

# ------------------------------------------------------------------------------
# 等待端口监听
# 参数：$1 = 端口号，$2 = 超时秒数（默认 15）
# 返回：0 = 已监听，1 = 超时
# ------------------------------------------------------------------------------
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

# ------------------------------------------------------------------------------
# HTTP 状态码检测
# 参数：$1 = URL
# 输出：HTTP 状态码（000 = 连接失败）
# ------------------------------------------------------------------------------
http_code() {
    local url="$1"
    curl -sS -o /dev/null -w "%{http_code}" --connect-timeout 3 --max-time 5 "$url" 2>/dev/null || echo "000"
}

# ------------------------------------------------------------------------------
# 从 config.yaml 读取后端端口
# 参数：$1 = config.yaml 路径（可选，默认使用 $CONFIG_FILE）
# 输出：端口号（解析失败时静默，由调用方使用默认值）
# ------------------------------------------------------------------------------
parse_backend_port() {
    local cfg="${1:-$CONFIG_FILE}"
    [[ -f "$cfg" ]] || return 0
    awk '
        /^server:/ { in_server=1; next }
        /^[a-z]+:/ { in_server=0 }
        in_server && /^[[:space:]]*port:/ {
            gsub(/[^0-9]/, "", $2); print $2; exit
        }
    ' "$cfg" 2>/dev/null || true
}

# ------------------------------------------------------------------------------
# 获取服务器主 IP（用于展示访问地址）
# ------------------------------------------------------------------------------
get_server_ip() {
    if [[ "$OS_TYPE" == "macos" ]]; then
        ipconfig getifaddr en0 2>/dev/null || echo "127.0.0.1"
    else
        hostname -I 2>/dev/null | awk '{print $1}' || echo "YOUR_SERVER_IP"
    fi
}

# ------------------------------------------------------------------------------
# 判断 PID 是否存活
# ------------------------------------------------------------------------------
pid_alive() {
    local pid="$1"
    [[ -z "$pid" ]] && return 1
    kill -0 "$pid" 2>/dev/null
}

# ------------------------------------------------------------------------------
# systemd 服务状态检测（仅 Linux）
# ------------------------------------------------------------------------------
svc_active() {
    systemctl is-active --quiet "$1" 2>/dev/null
}

svc_enabled() {
    systemctl is-enabled --quiet "$1" 2>/dev/null
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

# ------------------------------------------------------------------------------
# PATH 增强（兼容 nvm / homebrew / 用户安装的 Go/Node）
# 在脚本启动时调用一次，确保后续命令能找到 go / node / npm
# ------------------------------------------------------------------------------
enhance_path() {
    local extra_paths=()

    # 用户安装目录
    extra_paths+=("$HOME/go/bin" "$HOME/.local/bin" "$HOME/bin")
    extra_paths+=("/usr/local/go/bin" "/usr/local/bin")

    # Homebrew（macOS）
    if [[ -d /opt/homebrew/bin ]]; then
        extra_paths+=("/opt/homebrew/bin")
    fi

    # nvm 最新 Node 版本
    if [[ -d "$HOME/.nvm/versions/node" ]]; then
        local nvm_node
        nvm_node="$(ls -t "$HOME/.nvm/versions/node/" 2>/dev/null | head -1)"
        [[ -n "$nvm_node" ]] && extra_paths+=("$HOME/.nvm/versions/node/${nvm_node}/bin")
    fi

    # sudo 场景：加入真实用户的路径
    if [[ -n "${SUDO_USER:-}" ]]; then
        local real_home
        real_home="$(eval echo "~${SUDO_USER}")"
        extra_paths+=("${real_home}/go/bin" "${real_home}/.local/bin" "${real_home}/bin")
        extra_paths+=("${real_home}/.gvm/gos/current/bin")
        if [[ -d "${real_home}/.nvm/versions/node" ]]; then
            local nvm_node
            nvm_node="$(ls -t "${real_home}/.nvm/versions/node/" 2>/dev/null | head -1)"
            [[ -n "$nvm_node" ]] && extra_paths+=("${real_home}/.nvm/versions/node/${nvm_node}/bin")
        fi
    fi

    for p in "${extra_paths[@]}"; do
        if [[ -d "$p" ]] && [[ ":$PATH:" != *":$p:"* ]]; then
            PATH="$p:$PATH"
        fi
    done
    export PATH
}
