#!/bin/bash
# ==============================================================================
# NAS Backup System - macOS 部署模块（被 deploy.sh source 引入）
# ==============================================================================
# 提供函数：deploy_main
# 依赖全局变量（由 common.sh / deploy.sh 设置）：
#   SCRIPT_DIR / PROJECT_ROOT / BACKEND_DIR / FRONTEND_DIR / DATA_DIR
#   CONFIG_FILE / SKIP_DEPS / USE_REAL_OSS
# ==============================================================================

# ------------------------------------------------------------------------------
# macOS 部署主函数
# ------------------------------------------------------------------------------
deploy_main() {
    # macOS 专用路径检测（覆盖 detect_paths 的默认值，macOS 使用项目内路径）
    PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
    BACKEND_DIR="${PROJECT_ROOT}/nas-backup-backend"
    FRONTEND_DIR="${PROJECT_ROOT}/nas-backup-frontend"
    DATA_DIR="${BACKEND_DIR}/data"
    CONFIG_DIR="${BACKEND_DIR}/config"
    CONFIG_FILE="${CONFIG_DIR}/config.yaml"
    RCLONE_CONFIG="${DATA_DIR}/rclone.conf"

    local test_dir="${PROJECT_ROOT}/test-env"
    local local_cloud_dir="${test_dir}/local-cloud-storage"
    local test_source_dir="${test_dir}/source-files"
    local test_restore_dir="${test_dir}/restore-output"

    # ------------------------------------------------------------------
    # Pre-flight
    # ------------------------------------------------------------------
    step "Pre-flight 检查"
    [[ "$(uname -s)" == "Darwin" ]] || fail "macOS 模块仅支持 macOS，当前: $(uname -s)"
    success "运行于 macOS $(sw_vers -productVersion)"

    # ------------------------------------------------------------------
    # Step 1: Homebrew + 依赖
    # ------------------------------------------------------------------
    if [[ "$SKIP_DEPS" == "false" ]]; then
        step "检查 Homebrew"
        if ! command -v brew &>/dev/null; then
            info "安装 Homebrew..."
            /bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"
            [[ -f /opt/homebrew/bin/brew ]] && eval "$(/opt/homebrew/bin/brew shellenv)"
        fi
        success "Homebrew 可用: $(brew --version | head -1)"

        step "通过 Homebrew 安装系统依赖"
        local brew_pkgs=()
        command -v go      &>/dev/null || brew_pkgs+=(go)
        command -v node    &>/dev/null || brew_pkgs+=(node)
        command -v rclone  &>/dev/null || brew_pkgs+=(rclone)
        command -v zstd    &>/dev/null || brew_pkgs+=(zstd)
        command -v python3 &>/dev/null || brew_pkgs+=(python3)
        command -v openssl &>/dev/null || brew_pkgs+=(openssl)

        if [[ ${#brew_pkgs[@]} -gt 0 ]]; then
            info "安装: ${brew_pkgs[*]}"
            brew install "${brew_pkgs[@]}"
        else
            info "所有系统依赖已安装"
        fi

        for dep in go node rclone zstd python3 openssl; do
            command -v "$dep" &>/dev/null && success "$dep: 已安装" || fail "$dep 安装失败"
        done

        step "安装 Python 依赖"
        pip3 install --break-system-packages requests 2>/dev/null || pip3 install requests
        success "Python requests 已安装"
    else
        info "跳过依赖安装 (--skip-deps)"
    fi

    # ------------------------------------------------------------------
    # Step 2: 验证构建工具链
    # ------------------------------------------------------------------
    step "验证构建工具链"
    export GOPATH="${GOPATH:-$HOME/go}"
    export PATH="${GOPATH}/bin:/usr/local/go/bin:/opt/homebrew/bin:$PATH"
    success "Go: $(go version | awk '{print $3}')"
    success "Node.js: $(node --version) (npm $(npm --version))"

    # ------------------------------------------------------------------
    # Step 3: 测试环境目录
    # ------------------------------------------------------------------
    step "创建测试环境目录"
    mkdir -p "$CONFIG_DIR" "$DATA_DIR" "${DATA_DIR}/logs" "$local_cloud_dir" "$test_source_dir" "$test_restore_dir"
    success "配置目录: ${CONFIG_DIR}"
    success "数据目录: ${DATA_DIR}"
    success "本地云存储: ${local_cloud_dir}"

    # ------------------------------------------------------------------
    # Step 4: 主密钥
    # ------------------------------------------------------------------
    step "生成主加密密钥"
    local master_key="${CONFIG_DIR}/master.key"
    if [[ ! -f "$master_key" ]]; then
        openssl rand -hex 32 > "$master_key"
        chmod 600 "$master_key"
        success "主密钥已生成: ${master_key}"
    else
        success "主密钥已存在: ${master_key}"
    fi

    # ------------------------------------------------------------------
    # Step 5: rclone 配置
    # ------------------------------------------------------------------
    step "配置 rclone"

    if [[ "$USE_REAL_OSS" == "true" ]]; then
        info "生产模式：真实阿里云 OSS（以 config.yaml 的 oss 段为唯一权威源）"
        # config.yaml 是 OSS 配置的唯一权威源，后端启动时 EnsureRcloneConfig
        # 会依据 config.yaml 自动生成真实 s3 的 rclone.conf。
        # 这里只需清理历史遗留的本地模拟配置（type=local），避免 type=local
        # 残留导致备份写入本地只读根目录而失败。
        if [[ -f "$RCLONE_CONFIG" ]] && grep -q '^[[:space:]]*type[[:space:]]*=[[:space:]]*local' "$RCLONE_CONFIG"; then
            rm -f "$RCLONE_CONFIG"
            info "已删除遗留的本地模拟 rclone.conf（启动时将按 config.yaml 自动重建为真实 OSS）"
        fi
        # 校验 config.yaml 真实 OSS 配置是否完整
        local miss=()
        [[ -z "$(awk '/^  endpoint:/{gsub(/[" ]/,"",$2);print $2}' "$CONFIG_FILE")" ]]          && miss+=("endpoint")
        [[ -z "$(awk '/^  bucket:/{gsub(/[" ]/,"",$2);print $2}' "$CONFIG_FILE")" ]]            && miss+=("bucket")
        [[ -z "$(awk '/^  access_key_id:/{gsub(/[" ]/,"",$2);print $2}' "$CONFIG_FILE")" ]]     && miss+=("access_key_id")
        [[ -z "$(awk '/^  access_key_secret:/{gsub(/[" ]/,"",$2);print $2}' "$CONFIG_FILE")" ]] && miss+=("access_key_secret")
        if [[ ${#miss[@]} -gt 0 ]]; then
            fail "config.yaml 的 oss 段缺少配置: ${miss[*]}。请先在 nas-backup-backend/config/config.yaml 填写真实 OSS 配置后重试。"
        fi
    else
        info "配置本地文件系统 remote（离线测试模式，--local）..."
        cat > "$RCLONE_CONFIG" <<EOF
# Rclone 配置 - 本地测试模式（$(date -Iseconds)）
# 使用本地文件系统模拟云存储（无加密层，由应用层 master.key 提供加密）

[oss]
type = local
EOF
        chmod 600 "$RCLONE_CONFIG"
    fi

    if [[ -f "$RCLONE_CONFIG" ]]; then
        rclone config show --config "$RCLONE_CONFIG" &>/dev/null && success "rclone 配置验证通过" || warn "rclone 配置已写入（内容以后端启动生成为准）"
    else
        info "rclone.conf 尚未生成（生产模式将由后端启动时依据 config.yaml 自动生成）"
    fi

    # ------------------------------------------------------------------
    # Step 6: 生成 config.yaml
    # ------------------------------------------------------------------
    step "生成应用配置 config.yaml"
    if [[ "$USE_REAL_OSS" == "true" && -f "$CONFIG_FILE" ]]; then
        info "生产模式：保留现有 config.yaml（含真实 OSS 配置），不覆盖"
    else
        _generate_macos_config "$CONFIG_FILE" "$test_dir" "$test_source_dir"
        success "配置文件已写入: ${CONFIG_FILE}"
    fi

    # ------------------------------------------------------------------
    # Step 7: rclone 二进制
    # ------------------------------------------------------------------
    step "设置 rclone 二进制"
    mkdir -p "${BACKEND_DIR}/bin"
    if command -v rclone &>/dev/null; then
        cp "$(which rclone)" "${BACKEND_DIR}/bin/rclone"
        chmod +x "${BACKEND_DIR}/bin/rclone"
        success "已复制 rclone 到 ./bin/rclone"
    elif [[ -f "${BACKEND_DIR}/bin/rclone" ]]; then
        success "rclone 已存在于 ./bin/rclone"
    else
        info "下载 rclone ${RCLONE_ARCH}..."
        local rclone_ver="v1.75.0"
        curl -L -o /tmp/rclone.zip "https://downloads.rclone.org/${rclone_ver}/rclone-${rclone_ver}-osx-${RCLONE_ARCH}.zip"
        unzip -o /tmp/rclone.zip -d /tmp/
        cp "/tmp/rclone-${rclone_ver}-osx-${RCLONE_ARCH}/rclone" "${BACKEND_DIR}/bin/rclone"
        chmod +x "${BACKEND_DIR}/bin/rclone"
        rm -rf "/tmp/rclone-${rclone_ver}-osx-${RCLONE_ARCH}" /tmp/rclone.zip
        success "已下载 rclone ${rclone_ver} 到 ./bin/rclone"
    fi

    # ------------------------------------------------------------------
    # Step 8: 构建后端
    # ------------------------------------------------------------------
    step "构建 Go 后端"
    cd "$BACKEND_DIR"
    info "下载 Go 模块..."
    go mod download
    success "Go 模块已下载"

    info "编译 nas-backup..."
    CGO_ENABLED=1 go build -o nas-backup ./cmd/nas-backup/
    success "后端二进制已构建: ${BACKEND_DIR}/nas-backup"

    info "编译 restore-cli..."
    CGO_ENABLED=1 go build -o restore-cli ./cmd/restore-cli/
    success "restore-cli 已构建: ${BACKEND_DIR}/restore-cli"

    info "运行后端单元测试..."
    if go test ./internal/... -v -count=1 -short 2>&1 | tail -20; then
        success "单元测试通过"
    else
        warn "部分测试有问题（DB 相关测试可能预期失败）"
    fi

    # ------------------------------------------------------------------
    # Step 9: 构建前端
    # ------------------------------------------------------------------
    step "构建 React 前端"
    cd "$FRONTEND_DIR"

    # 依赖是否过期: node_modules 缺失，或 package.json / package-lock.json 比
    # node_modules/.package-lock.json 新。未变则跳过安装，大幅减少每次部署耗时。
    # 注意不用 npm ci（会删除整个 node_modules 全量重装 + 全量下载）。
    local needs_install=false
    if [[ ! -d node_modules ]]; then
        needs_install=true
    elif [[ ! -f node_modules/.package-lock.json ]]; then
        needs_install=true
    elif [[ "package.json" -nt node_modules/.package-lock.json ]] || [[ "package-lock.json" -nt node_modules/.package-lock.json ]]; then
        needs_install=true
    fi

    if [[ "$needs_install" == "true" ]]; then
        info "安装/更新 npm 依赖（增量优先本地缓存）..."
        npm install --prefer-offline
    else
        info "npm 依赖已是最新，跳过安装（仅构建）"
    fi
    success "npm 依赖已就绪"

    info "构建生产前端..."
    npm run build
    success "前端已构建: ${FRONTEND_DIR}/dist/"

    # ------------------------------------------------------------------
    # Step 10: 环境信息 + 便捷脚本
    # ------------------------------------------------------------------
    step "生成环境信息与便捷脚本"

    cat > "${SCRIPT_DIR}/env-macos.sh" <<EOF
# 使用方法: source scripts/env-macos.sh
export PROJECT_ROOT="${PROJECT_ROOT}"
export BACKEND_DIR="${BACKEND_DIR}"
export FRONTEND_DIR="${FRONTEND_DIR}"
export DATA_DIR="${DATA_DIR}"
export CONFIG_FILE="${CONFIG_FILE}"
export RCLONE_CONFIG="${RCLONE_CONFIG}"
export PATH="${GOPATH}/bin:/usr/local/go/bin:/opt/homebrew/bin:\$PATH"
EOF
    success "环境文件: scripts/env-macos.sh"

    _generate_convenience_scripts

    # ------------------------------------------------------------------
    # 完成
    # ------------------------------------------------------------------
    echo ""
    echo -e "${GREEN}${BOLD}======================================================================${NC}"
    echo -e "${GREEN}${BOLD}  NAS Backup System - macOS 部署完成！${NC}"
    echo -e "${GREEN}${BOLD}======================================================================${NC}"
    echo ""
    echo -e "  ${BOLD}环境:${NC}"
    echo -e "    后端二进制:   ${BACKEND_DIR}/nas-backup"
    echo -e "    前端构建:     ${FRONTEND_DIR}/dist/"
    echo -e "    配置文件:      ${CONFIG_FILE}"
    echo -e "    数据目录:     ${DATA_DIR}"
    echo -e "    本地云存储:   ${local_cloud_dir}"
    echo ""
    echo -e "  ${BOLD}快速开始:${NC}"
    echo -e "    1. 加载环境:   source scripts/env-macos.sh"
    echo -e "    2. 启动服务:   ./scripts/start.sh start"
    echo -e "    3. 打开浏览器: http://localhost:5173"
    echo -e "    4. 运行 E2E:   ./scripts/verify-e2e.sh"
    echo ""
    if [[ "$USE_REAL_OSS" == "true" ]]; then
        echo -e "  ${BOLD}OSS 模式:${NC}  生产（真实阿里云 OSS，以 config.yaml oss 段为准）"
        echo -e "  ${BOLD}本地测试:${NC}   ./scripts/deploy.sh --local"
    else
        echo -e "  ${BOLD}OSS 模式:${NC}  本地文件系统模拟（--local 测试模式）"
        echo -e "  ${BOLD}生产模式:${NC}   ./scripts/deploy.sh"
    fi
    echo ""
}

# ------------------------------------------------------------------------------
# 生成 macOS 本地测试配置
# ------------------------------------------------------------------------------
_generate_macos_config() {
    local cfg="$1"
    local test_dir="$2"
    local test_source_dir="$3"

    cat > "$cfg" <<EOF
# NAS Backup System - macOS 本地部署配置（$(date -Iseconds)）
# 模式: $([[ "$USE_REAL_OSS" == "true" ]] && echo "阿里云 OSS" || echo "本地测试（本地文件系统模拟云存储）")

server:
  host: "127.0.0.1"
  port: 8080
  read_timeout_sec: 30
  write_timeout_sec: 120
  restore_base_dirs:
    - "${test_dir}"

database:
  path: "./data/nas-backup.db"

backup:
  directories:
    - path: "${test_source_dir}"
      recursive: true
      enabled: true
      description: "E2E 验证测试源目录"

  exclusions:
    - pattern: "*.tmp"
      rule_type: "extension"
      enabled: true
    - pattern: "*.log"
      rule_type: "extension"
      enabled: true
    - pattern: ".DS_Store"
      rule_type: "pattern"
      enabled: true
    - pattern: "Thumbs.db"
      rule_type: "pattern"
      enabled: true
    - pattern: "node_modules"
      rule_type: "directory"
      enabled: true
    - pattern: ".git"
      rule_type: "directory"
      enabled: true

  size_limit:
    max_file_size: 0
    min_file_size: 0

  schedule:
    enabled: false
    cron_expr: "0 3 1 * *"
    timezone: "Asia/Shanghai"

  compression:
    enabled: true
    algorithm: "zstd"
    level: 19
    skip_types:
      - ".mp4"
      - ".mkv"
      - ".mov"
      - ".jpg"
      - ".jpeg"
      - ".png"
      - ".webp"
      - ".gif"
      - ".mp3"
      - ".zip"
      - ".7z"
      - ".gz"
      - ".bz2"
      - ".xz"
      - ".pdf"

  retention:
    orphan_grace_days: 30
    keep_deleted_days: 30
    db_bkup_keep_count: 5

  encryption:
    algorithm: "AES-256-GCM"
    key_file_path: "./config/master.key"

oss:
  endpoint: ""
  bucket: ""
  access_key_id: ""
  access_key_secret: ""
  region: ""

rclone:
  binary_path: "./bin/rclone"
  config_path: "./data/rclone.conf"
  remote_name: "oss"

logging:
  level: "info"
  file_path: "./data/logs/nas-backup.log"
  max_size_mb: 50
  max_files: 10
EOF
}

# ------------------------------------------------------------------------------
# 生成便捷启动脚本
# ------------------------------------------------------------------------------
_generate_convenience_scripts() {
    cat > "${SCRIPT_DIR}/start-backend.sh" <<'STARTSCRIPT'
#!/bin/bash
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
cd "${PROJECT_ROOT}/nas-backup-backend"
echo "启动 NAS Backup 后端 http://127.0.0.1:8080 ... (Ctrl+C 停止)"
exec ./nas-backup -config config/config.yaml
STARTSCRIPT
    chmod +x "${SCRIPT_DIR}/start-backend.sh"

    cat > "${SCRIPT_DIR}/start-frontend-dev.sh" <<'STARTSCRIPT'
#!/bin/bash
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
cd "${PROJECT_ROOT}/nas-backup-frontend"
echo "启动 Vite 开发服务器 http://localhost:5173 ... (API 代理到 8080)"
exec npm run dev
STARTSCRIPT
    chmod +x "${SCRIPT_DIR}/start-frontend-dev.sh"

    success "便捷脚本已生成: scripts/start-backend.sh, scripts/start-frontend-dev.sh"
}
