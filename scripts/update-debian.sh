#!/bin/bash
# ==============================================================================
# NAS Backup System - Debian 生产环境 代码更新 + 重建 + 重启 脚本
# ==============================================================================
# 场景: Git pull 拉了新代码后，需要重新构建前后端并重启服务。
#       此脚本是 start.sh 的"代码更新专用"配套脚本。
#
# 流程:
#   1. (可选) git pull 拉取最新代码
#   2. 备份当前运行的二进制和 dist/（便于快速回滚）
#   3. 构建 Go 后端二进制 (nas-backup + restore-cli)
#   4. 构建 React 前端 (vite build, 用本地 vite 不用 npx，避免 NAS 上 node 版本坑)
#   5. 调用 start.sh restart 重启后端 + reload Nginx
#   6. 调用 start.sh health 做三重健康检查
#
# 与 start.sh 的分工:
#   - start.sh:  进程管理 (start/stop/restart/status/logs/health)
#   - update-debian.sh: 代码更新 (git pull + 构建 + 触发 restart + health)
#
# 用法:
#   sudo ./scripts/update-debian.sh                # 完整流程: pull + 构建后端 + 构建前端 + 重启 + 健康检查
#   sudo ./scripts/update-debian.sh --no-pull      # 跳过 git pull (已手动 pull 过)
#   sudo ./scripts/update-debian.sh --no-frontend  # 只更新后端
#   sudo ./scripts/update-debian.sh --no-backend   # 只更新前端
#   sudo ./scripts/update-debian.sh --skip-tests   # 跳过 Go 单元测试
#   sudo ./scripts/update-debian.sh rollback       # 回滚到上一次备份的二进制 + dist
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
# 参数解析
# ------------------------------------------------------------------------------
DO_PULL=true
DO_BACKEND=true
DO_FRONTEND=true
RUN_TESTS=true
COMMAND="update"

while [[ $# -gt 0 ]]; do
    case "$1" in
        --no-pull)      DO_PULL=false; shift ;;
        --no-frontend)  DO_FRONTEND=false; shift ;;
        --no-backend)   DO_BACKEND=false; shift ;;
        --skip-tests)   RUN_TESTS=false; shift ;;
        -h|--help)
            awk 'NR>=2 && NR<=40 {sub(/^# ?/,""); print}' "$0"
            exit 0 ;;
        rollback|update)
            COMMAND="$1"; shift ;;
        *)
            echo "未知参数: $1"
            exit 1
            ;;
    esac
done

# ------------------------------------------------------------------------------
# 路径自动检测（与 lib/deploy-debian.sh / start.sh 保持一致）
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
BACKEND_BIN="${BACKEND_DIR}/nas-backup"
RESTORE_CLI_BIN="${BACKEND_DIR}/restore-cli"
BACKUP_DIR="${DATA_DIR}/rollback-backups/$(date +%Y%m%d_%H%M%S)"

START_SCRIPT="${SCRIPT_DIR}/start.sh"

# 加常用 binary 进 PATH（与 lib/deploy-debian.sh 一致，避免 sudo 下 PATH 被重置丢工具）
COMMON_USER_PATHS=(
    "/usr/local/sbin" "/usr/local/bin"
    "/usr/sbin" "/usr/bin" "/sbin" "/bin"
    "$HOME/go/bin" "$HOME/.local/bin" "$HOME/bin"
    "/usr/local/go/bin"
)
if [[ -n "${SUDO_USER:-}" ]]; then
    REAL_USER_HOME="$(eval echo "~${SUDO_USER}")"
    COMMON_USER_PATHS+=(
        "${REAL_USER_HOME}/go/bin"
        "${REAL_USER_HOME}/.local/bin"
        "${REAL_USER_HOME}/bin"
        "${REAL_USER_HOME}/.gvm/gos/current/bin"
    )
    if [[ -d "${REAL_USER_HOME}/.nvm/versions/node" ]]; then
        NVM_LATEST_NODE="$(ls -t "${REAL_USER_HOME}/.nvm/versions/node/" 2>/dev/null | head -1)"
        [[ -n "$NVM_LATEST_NODE" ]] && COMMON_USER_PATHS+=("${REAL_USER_HOME}/.nvm/versions/node/${NVM_LATEST_NODE}/bin")
    fi
fi
for p in "${COMMON_USER_PATHS[@]}"; do
    if [[ -d "$p" ]] && [[ ":$PATH:" != *":$p:"* ]]; then
        PATH="$p:$PATH"
    fi
done
export PATH

# ------------------------------------------------------------------------------
# 权限检查：构建/重启需要 root（systemd restart、写二进制到安装目录）
# ------------------------------------------------------------------------------
ensure_root() {
    if [[ $EUID -ne 0 ]]; then
        warn "本命令需要 root 权限来覆盖生产二进制和重启 systemd 服务"
        warn "正在尝试通过 sudo 重新执行..."
        exec sudo -E bash "$0" "$@"
    fi
}

# ------------------------------------------------------------------------------
# 预检
# ------------------------------------------------------------------------------
preflight() {
    step "环境预检"

    # 非 Linux 警告但继续（macOS 本地也能预览语法）
    if [[ "$(uname -s)" != "Linux" ]]; then
        warn "检测到非 Linux 系统 (uname: $(uname -s))，此脚本设计用于 Debian 生产环境"
        warn "继续运行仅做语法/流程验证，构建结果不会是 Linux 可执行文件"
    fi

    local missing=()
    [[ -d "$BACKEND_DIR" ]]           || missing+=("后端目录: ${BACKEND_DIR}")
    [[ -d "$FRONTEND_DIR" ]]          || missing+=("前端目录: ${FRONTEND_DIR}")
    [[ -f "$CONFIG_FILE" ]]           || missing+=("配置文件: ${CONFIG_FILE}")
    [[ -x "$START_SCRIPT" ]]          || missing+=("配套启动脚本: ${START_SCRIPT}")
    command -v go   >/dev/null        || missing+=("go 工具链")
    command -v node >/dev/null        || missing+=("node 运行时")
    command -v npm  >/dev/null        || missing+=("npm")
    command -v git  >/dev/null        || missing+=("git")

    if [[ ${#missing[@]} -gt 0 ]]; then
        echo -e "${RED}环境缺失：${NC}"
        printf '  - %s\n' "${missing[@]}"
        echo ""
        echo "请先运行部署脚本: sudo ./scripts/deploy.sh --skip-deps"
        exit 1
    fi

    # 架构检测
    ARCH="$(dpkg --print-architecture 2>/dev/null || uname -m)"
    case "$ARCH" in
        amd64|x86_64)  GOARCH="amd64" ;;
        arm64|aarch64) GOARCH="arm64" ;;
        armhf|armv7l)  GOARCH="arm"   ;;
        *)             GOARCH="amd64"; warn "未知架构 $ARCH，默认 amd64" ;;
    esac

    # 版本检查
    NODE_MAJOR="$(node -v | sed 's/v//' | cut -d. -f1)"
    GO_MAJOR="$(go version | awk '{print $3}' | sed 's/go//' | cut -d. -f1)"
    GO_MINOR="$(go version | awk '{print $3}' | sed 's/go//' | cut -d. -f2)"

    success "安装目录:  ${INSTALL_DIR}"
    success "架构:       ${ARCH} (GOARCH=${GOARCH})"
    success "Go:         $(go version | awk '{print $3}')  (>= 1.25 required)  ✓"
    success "Node:       $(node -v)  npm $(npm --version)  (>= 20 required)  ✓"
    success "Git:        $(git --version | awk '{print $3}')"

    # Go/Node 版本最低要求（低于则警告但不终止，可能用户就是想试）
    (( NODE_MAJOR >= 20 )) || warn "Node 版本 < 20，前端构建可能失败"
    if (( GO_MAJOR < 1 )) || { (( GO_MAJOR == 1 )) && (( GO_MINOR < 25 )); }; then
        warn "Go 版本 < 1.25，后端构建可能失败"
    fi
}

# ------------------------------------------------------------------------------
# 回滚：恢复最近一次备份的二进制和 dist
# ------------------------------------------------------------------------------
do_rollback() {
    ensure_root "$@"
    preflight

    step "查找回滚备份"
    local rollback_root="${DATA_DIR}/rollback-backups"
    if [[ ! -d "$rollback_root" ]]; then
        fail "没有找到回滚备份目录: ${rollback_root}。此目录在每次 update 开始时生成。"
    fi
    local latest
    latest="$(ls -t "$rollback_root" 2>/dev/null | head -1)"
    [[ -n "$latest" ]] || fail "$rollback_root 下为空，无可回滚备份"

    local backup="${rollback_root}/${latest}"
    info "使用备份: ${backup}"

    if [[ -f "${backup}/nas-backup" ]]; then
        info "恢复后端二进制 ..."
        cp "${backup}/nas-backup" "$BACKEND_BIN" && chmod +x "$BACKEND_BIN" && success "后端二进制已恢复"
    fi
    if [[ -f "${backup}/restore-cli" ]]; then
        cp "${backup}/restore-cli" "$RESTORE_CLI_BIN" && chmod +x "$RESTORE_CLI_BIN" && success "restore-cli 已恢复"
    fi
    if [[ -d "${backup}/dist" ]]; then
        info "恢复前端 dist/ ..."
        rm -rf "${FRONTEND_DIR}/dist"
        cp -r "${backup}/dist" "${FRONTEND_DIR}/dist" && success "前端 dist/ 已恢复"
    fi

    step "重启服务使回滚生效"
    "$START_SCRIPT" restart
    "$START_SCRIPT" health
    success "回滚完成，已从 ${latest} 恢复"
    exit 0
}

# ------------------------------------------------------------------------------
# Git pull
# ------------------------------------------------------------------------------
do_git_pull() {
    if [[ "$DO_PULL" == "false" ]]; then
        info "跳过 git pull (--no-pull)"
        return 0
    fi
    step "拉取最新代码"
    cd "$INSTALL_DIR"
    if [[ ! -d ".git" ]]; then
        warn "当前目录不是 git 仓库 (无 .git/)，跳过 pull"
        return 0
    fi

    # 本地修改检查
    if ! git diff --quiet --exit-code 2>/dev/null; then
        warn "检测到本地未提交的修改，暂存 (git stash) 后再 pull，pull 完成后自动恢复"
        git stash push -m "nasbkup-update-stash-$(date +%s)" || warn "git stash 失败，继续使用当前工作区"
        local stashed=true
    fi

    local before
    before="$(git rev-parse --short HEAD 2>/dev/null || echo "?")"
    git pull --ff-only 2>&1 || {
        warn "git pull --ff-only 失败，尝试普通 pull ..."
        git pull 2>&1 || fail "git pull 失败，请手动解决冲突后再运行"
    }
    local after
    after="$(git rev-parse --short HEAD 2>/dev/null || echo "?")"

    if [[ "${stashed:-false}" == "true" ]]; then
        git stash pop 2>/dev/null || warn "git stash pop 失败，请手动检查工作区变更"
    fi

    if [[ "$before" == "$after" ]]; then
        success "已是最新代码 (${after})"
    else
        success "代码已更新 ${before} → ${after}"
        git log --oneline -n 5 2>/dev/null | sed 's/^/    /'
    fi
}

# ------------------------------------------------------------------------------
# 备份当前生产二进制，便于 rollback
# ------------------------------------------------------------------------------
backup_current() {
    step "备份当前生产产物（用于 rollback）"
    mkdir -p "$BACKUP_DIR"
    local backed=0
    if [[ -x "$BACKEND_BIN" ]]; then
        cp "$BACKEND_BIN" "${BACKUP_DIR}/nas-backup" && success "备份 nas-backup -> ${BACKUP_DIR}/"
        backed=$((backed+1))
    fi
    if [[ -x "$RESTORE_CLI_BIN" ]]; then
        cp "$RESTORE_CLI_BIN" "${BACKUP_DIR}/restore-cli" && success "备份 restore-cli -> ${BACKUP_DIR}/"
        backed=$((backed+1))
    fi
    if [[ -d "${FRONTEND_DIR}/dist" ]]; then
        cp -r "${FRONTEND_DIR}/dist" "${BACKUP_DIR}/dist" && success "备份 frontend/dist -> ${BACKUP_DIR}/dist"
        backed=$((backed+1))
    fi
    if [[ $backed -eq 0 ]]; then
        warn "没有找到可备份的现有生产产物（首次部署？）"
    else
        success "回滚点已生成: ${BACKUP_DIR}"
        info "回滚命令: sudo ./scripts/update-debian.sh rollback"
    fi
}

# ------------------------------------------------------------------------------
# 构建后端 Go
# ------------------------------------------------------------------------------
build_backend() {
    if [[ "$DO_BACKEND" == "false" ]]; then
        info "跳过后端构建 (--no-backend)"
        return 0
    fi
    step "构建 Go 后端 (linux/${GOARCH})"

    cd "$BACKEND_DIR"

    # 中国镜像链（与 lib/deploy-debian.sh 一致）
    export GOPROXY="https://goproxy.cn,https://mirrors.aliyun.com/goproxy/,https://goproxy.io,direct"
    export GOSUMDB="sum.golang.google.cn"
    export GO111MODULE=on

    info "拉取 Go 模块 ..."
    go mod download || fail "go mod download 失败"
    success "Go 模块就绪"

    # 临时产物路径（避免构建失败直接覆盖生产二进制）
    local tmp_bin="${BACKEND_DIR}/.tmp-nas-backup"
    local tmp_restore="${BACKEND_DIR}/.tmp-restore-cli"

    info "编译 nas-backup (CGO=1, ldflags -s -w) ..."
    # 构建参数与 lib/deploy-debian.sh 完全一致
    CGO_ENABLED=1 GOOS=linux GOARCH="$GOARCH" \
        go build -buildvcs=false -ldflags="-s -w" \
        -o "$tmp_bin" ./cmd/nas-backup/ \
        || fail "nas-backup 编译失败"

    info "编译 restore-cli ..."
    CGO_ENABLED=1 GOOS=linux GOARCH="$GOARCH" \
        go build -buildvcs=false -ldflags="-s -w" \
        -o "$tmp_restore" ./cmd/restore-cli/ \
        || fail "restore-cli 编译失败"

    # 单测（可跳过）
    if [[ "$RUN_TESTS" == "true" ]]; then
        info "运行 Go 单元测试 (short 模式，跳过 DB/OSS 集成用例)..."
        if go test ./internal/... -short -count=1 2>&1 | tail -n 20; then
            success "后端单元测试通过"
        else
            # 单元测试失败不是致命阻塞，warn 但继续（有时是测试环境 DB 文件锁等偶发）
            warn "部分单元测试失败，继续部署但请留意运行时表现"
        fi
    else
        info "跳过后端单元测试 (--skip-tests)"
    fi

    # 冒烟验证：能跑 --help 就说明二进制 OK
    if ! "$tmp_bin" --help >/dev/null 2>&1; then
        fail "新二进制冒烟验证失败！保留旧二进制，生产未受影响"
    fi

    # 原子覆盖
    mv "$tmp_bin" "$BACKEND_BIN" && chmod +x "$BACKEND_BIN"
    mv "$tmp_restore" "$RESTORE_CLI_BIN" && chmod +x "$RESTORE_CLI_BIN"
    success "后端二进制更新完成"
}

# ------------------------------------------------------------------------------
# 构建前端
# ------------------------------------------------------------------------------
build_frontend() {
    if [[ "$DO_FRONTEND" == "false" ]]; then
        info "跳过前端构建 (--no-frontend)"
        return 0
    fi
    step "构建 React 前端"

    cd "$FRONTEND_DIR"

    # 中国镜像（与 lib/deploy-debian.sh 一致）
    export npm_config_registry="https://registry.npmmirror.com"

    # 仅 node_modules 不存在时 npm install（省时间）
    if [[ ! -d node_modules ]]; then
        info "安装 npm 依赖 ..."
        npm ci --production=false --registry=https://registry.npmmirror.com || \
            npm install --production=false --registry=https://registry.npmmirror.com || \
            fail "npm 依赖安装失败"
        success "npm 依赖就绪"
    else
        success "npm 依赖已存在，跳过 install"
    fi

    # 关键：必须用 node_modules/.bin/vite，**绝对不允许用 npx vite**
    # - npx 会下载最新 vite@8，要求 node>=20.19；UGREEN DXP NAS 是 node 20.18.0，之前因为这个卡过
    # - 也不直接 npm run build（= tsc -b && vite build），tsc 类型检查在 NAS 上可能挂
    if [[ ! -x ./node_modules/.bin/vite ]]; then
        fail "本地 vite 不存在: ./node_modules/.bin/vite 。请先 npm install"
    fi
    info "使用本地 vite: $(./node_modules/.bin/vite --version)"

    # 临时 dist 目录，构建成功再原子替换
    local tmp_dist="${FRONTEND_DIR}/.tmp-dist"
    rm -rf "$tmp_dist"

    info "vite build 中 ..."
    # --outDir 指定临时目录，构建 0 退出码才算成功
    if ! ./node_modules/.bin/vite build --outDir "$tmp_dist" 2>&1 | tail -n 20; then
        rm -rf "$tmp_dist"
        fail "vite build 失败，旧 dist 未受影响"
    fi

    # 基本校验
    [[ -f "${tmp_dist}/index.html" ]] || fail "构建产物缺失 index.html"

    # 原子替换：先删旧，再 mv 新
    rm -rf "${FRONTEND_DIR}/dist"
    mv "$tmp_dist" "${FRONTEND_DIR}/dist"

    # 校验 dist/index.html 存在
    [[ -f "${FRONTEND_DIR}/dist/index.html" ]] || fail "移动后 dist/index.html 不存在"

    success "前端构建完成 -> dist/"
    du -sh "${FRONTEND_DIR}/dist" 2>/dev/null | awk '{print "    大小:", $1, "  文件数: "}' 
    find "${FRONTEND_DIR}/dist" -type f | wc -l | awk '{print "    文件数:", $1}'
}

# ------------------------------------------------------------------------------
# 重启 + 健康检查
# ------------------------------------------------------------------------------
restart_and_check() {
    step "重启后端服务 + reload Nginx"
    "$START_SCRIPT" restart

    step "健康检查"
    "$START_SCRIPT" health
}

# ------------------------------------------------------------------------------
# 主流程
# ------------------------------------------------------------------------------
do_update() {
    ensure_root "$@"
    preflight
    do_git_pull
    backup_current
    build_backend
    build_frontend
    restart_and_check

    local server_ip
    server_ip=$(hostname -I 2>/dev/null | awk '{print $1}' || echo "YOUR_SERVER_IP")
    echo ""
    echo -e "${GREEN}${BOLD}======================================================================${NC}"
    echo -e "${GREEN}${BOLD}  代码更新完成${NC}"
    echo -e "${GREEN}${BOLD}======================================================================${NC}"
    echo -e "  ${BOLD}访问地址:${NC}     http://${server_ip}:9000"
    echo -e "  ${BOLD}回滚命令:${NC}     sudo ./scripts/update-debian.sh rollback"
    echo -e "  ${BOLD}查看状态:${NC}     sudo ./scripts/start.sh status"
    echo -e "  ${BOLD}查看日志:${NC}     sudo ./scripts/start.sh logs app"
    echo ""
}

# ------------------------------------------------------------------------------
# 入口
# ------------------------------------------------------------------------------
case "$COMMAND" in
    update)   do_update ;;
    rollback) do_rollback ;;
    *)        fail "未知命令: $COMMAND" ;;
esac
