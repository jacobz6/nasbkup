#!/bin/bash
# ==============================================================================
# NAS Backup System - Debian 部署模块（被 deploy.sh source 引入）
# ==============================================================================
# 提供函数：deploy_main
# 依赖全局变量（由 common.sh / deploy.sh 设置）：
#   SCRIPT_DIR / SKIP_DEPS / SKIP_FRONTEND / NO_NGINX / INSTALL_DIR
#   GOARCH / RCLONE_ARCH
# ==============================================================================

deploy_main() {
    # ------------------------------------------------------------------
    # 路径检测（Debian 模式支持 --install-dir 覆盖）
    # ------------------------------------------------------------------
    if [[ -z "$INSTALL_DIR" ]]; then
        local detected_root
        detected_root="$(cd "${SCRIPT_DIR}/.." && pwd)"
        if [[ -d "${detected_root}/nas-backup-backend" && -d "${detected_root}/nas-backup-frontend" ]]; then
            INSTALL_DIR="$detected_root"
        else
            INSTALL_DIR="/opt/nas-backup"
        fi
    fi

    BACKEND_DIR="${INSTALL_DIR}/nas-backup-backend"
    FRONTEND_DIR="${INSTALL_DIR}/nas-backup-frontend"
    DATA_DIR="${BACKEND_DIR}/data"
    CONFIG_DIR="${BACKEND_DIR}/config"
    CONFIG_FILE="${CONFIG_DIR}/config.yaml"

    # ------------------------------------------------------------------
    # Pre-flight
    # ------------------------------------------------------------------
    step "Pre-flight 检查"

    local is_root=false
    if [[ $EUID -eq 0 ]]; then
        is_root=true
        if [[ -n "${SUDO_USER:-}" ]]; then
            info "以 root 运行（来自 sudo 用户: ${SUDO_USER}）"
        else
            info "以 root 运行"
        fi
    else
        warn "非 root 运行 - 仅编译构建，跳过 systemd / Nginx 安装"
        warn "如需安装系统服务，请使用 sudo 运行"
    fi

    success "架构: ${ARCH} (Go: ${GOARCH}, rclone: ${RCLONE_ARCH})"

    if [[ -f /etc/os-release ]]; then
        . /etc/os-release 2>/dev/null
        if [[ "${ID:-}" != "debian" ]] && [[ "${ID_LIKE:-}" != *debian* ]]; then
            warn "此脚本为 Debian 12 设计，检测到: ${PRETTY_NAME:-未知}"
        else
            success "操作系统: ${PRETTY_NAME:-Debian}"
        fi
    fi

    [[ -d "$BACKEND_DIR" ]] || fail "后端目录不存在: ${BACKEND_DIR}\n请确认项目已正确克隆。"
    success "后端目录: ${BACKEND_DIR}"

    if [[ "$SKIP_FRONTEND" == "false" && ! -d "$FRONTEND_DIR" ]]; then
        warn "前端目录不存在: ${FRONTEND_DIR}，将跳过前端构建"
        SKIP_FRONTEND=true
    fi
    [[ "$SKIP_FRONTEND" == "false" ]] && success "前端目录: ${FRONTEND_DIR}"

    local config_exists=false
    if [[ -f "$CONFIG_FILE" ]]; then
        success "配置文件: ${CONFIG_FILE}"
        config_exists=true
    else
        warn "config.yaml 不存在: ${CONFIG_FILE}，将使用默认配置"
    fi

    # ------------------------------------------------------------------
    # Step 1: 系统依赖（安全模式 - 绝不 upgrade）
    # ------------------------------------------------------------------
    if [[ "$SKIP_DEPS" == "false" ]]; then
        step "检查系统依赖（安全模式 - 不升级系统）"

        if [[ "$is_root" == "true" ]]; then
            info "刷新包列表..."
            apt update -qq 2>/dev/null || warn "apt update 有问题，继续..."

            # 关键包（必装）
            local critical_pkgs=(ca-certificates openssl unzip)
            local nice_pkgs=(curl wget gcc libc6-dev make file)
            local optional_pkgs=(sqlite3 git)

            local missing_critical=() missing_nice=()
            for pkg in "${critical_pkgs[@]}"; do
                dpkg -s "$pkg" &>/dev/null || missing_critical+=("$pkg")
            done
            for pkg in "${nice_pkgs[@]}"; do
                dpkg -s "$pkg" &>/dev/null || missing_nice+=("$pkg")
            done

            if [[ ${#missing_critical[@]} -gt 0 ]]; then
                info "安装关键包（--no-upgrade）: ${missing_critical[*]}"
                DEBIAN_FRONTEND=noninteractive apt install -y -qq --no-upgrade \
                    -o Dpkg::Options::="--force-confdef" -o Dpkg::Options::="--force-confold" \
                    "${missing_critical[@]}" 2>&1 || warn "部分包安装失败，尝试绕行..."
            fi

            if [[ ${#missing_nice[@]} -gt 0 ]]; then
                info "检查工具: ${missing_nice[*]}"
                DEBIAN_FRONTEND=noninteractive apt install -y -qq --no-upgrade \
                    -o Dpkg::Options::="--force-confdef" -o Dpkg::Options::="--force-confold" \
                    "${missing_nice[@]}" 2>&1 || warn "部分工具无法通过 apt 安装，尝试二进制兜底"
            fi

            for bin in curl wget openssl; do
                command -v "$bin" &>/dev/null && success "$bin: 可用" || warn "$bin 未找到"
            done

            # CGO 需要 gcc
            if command -v gcc &>/dev/null; then
                success "gcc: $(gcc --version 2>/dev/null | head -1)"
            else
                warn "gcc 未找到！CGO（go-sqlite3）需要 gcc"
                if [[ "$is_root" == "true" ]]; then
                    DEBIAN_FRONTEND=noninteractive apt install -y -qq --no-upgrade gcc 2>&1 || true
                fi
                command -v gcc &>/dev/null || fail "gcc 必需但无法安装，请手动通过 NAS 包管理器安装 build-essential/gcc"
            fi

            success "系统依赖检查完成（未执行任何系统升级）"
        else
            info "非 root，跳过 apt 安装"
            for bin in curl wget openssl gcc; do
                command -v "$bin" &>/dev/null && success "$bin: 可用" || warn "$bin 未找到 - 构建可能失败"
            done
        fi
    else
        info "跳过依赖检查 (--skip-deps)"
    fi

    # ------------------------------------------------------------------
    # Step 2: Go（二进制下载，不依赖 apt）
    # ------------------------------------------------------------------
    _ensure_go "$is_root"

    # ------------------------------------------------------------------
    # Step 3: Node.js（二进制下载，不依赖 apt / NodeSource）
    # ------------------------------------------------------------------
    [[ "$SKIP_FRONTEND" == "false" ]] && _ensure_node "$is_root"

    # ------------------------------------------------------------------
    # Step 4: rclone（二进制下载）
    # ------------------------------------------------------------------
    _ensure_rclone "$is_root"

    # 确保 rclone 在 backend/bin/
    mkdir -p "${BACKEND_DIR}/bin"
    if [[ ! -f "${BACKEND_DIR}/bin/rclone" ]] || [[ "${RCLONE_BIN:-}" -nt "${BACKEND_DIR}/bin/rclone" ]]; then
        cp "${RCLONE_BIN}" "${BACKEND_DIR}/bin/rclone"
        chmod +x "${BACKEND_DIR}/bin/rclone"
        success "已复制 rclone 到 ${BACKEND_DIR}/bin/rclone"
    fi

    # ------------------------------------------------------------------
    # Step 5: 数据目录 + 主密钥
    # ------------------------------------------------------------------
    step "设置数据目录"
    mkdir -p "${CONFIG_DIR}" "${DATA_DIR}/logs"
    chmod 700 "${DATA_DIR}"
    success "数据目录: ${DATA_DIR}"

    local master_key="${CONFIG_DIR}/master.key"
    if [[ ! -f "$master_key" ]]; then
        info "生成主加密密钥..."
        openssl rand -hex 32 > "$master_key"
        chmod 600 "$master_key"
        success "主密钥已生成: ${master_key}"
    else
        success "主密钥已存在: ${master_key}"
    fi

    # ------------------------------------------------------------------
    # Step 6: 修正 config.yaml 路径
    # ------------------------------------------------------------------
    if [[ "$config_exists" == "true" ]]; then
        step "检查并修正配置"
        _fix_config_paths "$CONFIG_FILE"
    else
        warn "config.yaml 不存在。后端将以默认值启动，但必须先配置！"
    fi

    # ------------------------------------------------------------------
    # Step 7: 构建后端
    # ------------------------------------------------------------------
    step "构建 Go 后端"
    cd "$BACKEND_DIR"

    # 中国镜像链
    export GOPROXY="https://goproxy.cn,https://mirrors.aliyun.com/goproxy/,https://goproxy.io,direct"
    export GOSUMDB="sum.golang.google.cn"
    export GO111MODULE=on
    info "Go proxy: ${GOPROXY}"

    info "下载 Go 模块..."
    go mod download
    success "Go 模块已下载"

    info "编译 nas-backup (linux/${GOARCH})..."
    CGO_ENABLED=1 GOOS=linux GOARCH="$GOARCH" go build -buildvcs=false -ldflags="-s -w" -o nas-backup ./cmd/nas-backup/
    success "后端二进制已构建: ${BACKEND_DIR}/nas-backup"

    info "编译 restore-cli..."
    CGO_ENABLED=1 GOOS=linux GOARCH="$GOARCH" go build -buildvcs=false -ldflags="-s -w" -o restore-cli ./cmd/restore-cli/
    success "restore-cli 已构建: ${BACKEND_DIR}/restore-cli"

    info "验证后端二进制..."
    ./nas-backup --help &>/dev/null && success "后端二进制运行正常" || fail "后端二进制验证失败"

    # ------------------------------------------------------------------
    # Step 8: 构建前端
    # ------------------------------------------------------------------
    if [[ "$SKIP_FRONTEND" == "false" ]]; then
        step "构建 React 前端"
        cd "$FRONTEND_DIR"

        # 淘宝镜像
        export npm_config_registry="https://registry.npmmirror.com"
        info "npm registry: ${npm_config_registry}"

        # 依赖是否过期: node_modules 缺失，或 package.json / package-lock.json 比
        # node_modules/.package-lock.json 新。未变则跳过安装，大幅减少每次部署耗时。
        # 不用 npm ci（会删除整个 node_modules 全量重装 + 全量下载）。
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
            npm install --production=false --prefer-offline --registry=https://registry.npmmirror.com || \
            npm install --production=false --registry=https://registry.npmmirror.com
        else
            info "npm 依赖已是最新，跳过安装（仅构建）"
        fi
        success "npm 依赖已就绪"

        info "构建生产前端..."
        # 使用本地 vite（避免 npx 下载 vite@8 要求 node>=20.19 的坑）
        if [[ -x ./node_modules/.bin/vite ]]; then
            info "使用本地 vite ($(./node_modules/.bin/vite --version))"
            ./node_modules/.bin/vite build
        else
            warn "本地 vite 未找到，回退到 npm run build..."
            npm run build || ./node_modules/.bin/vite build || fail "前端构建失败"
        fi
        success "前端构建完成"

        [[ -d "${FRONTEND_DIR}/dist" ]] || fail "前端构建失败 - dist/ 不存在"
        success "前端已构建: ${FRONTEND_DIR}/dist/"
    fi

    # ------------------------------------------------------------------
    # Step 9: systemd 服务（需 root）
    # ------------------------------------------------------------------
    if [[ "$is_root" == "true" ]]; then
        step "配置 systemd 服务"
        _setup_systemd "$BACKEND_DIR" "$CONFIG_FILE"
    else
        step "跳过 systemd 服务安装（需 root）"
        info "手动启动: cd ${BACKEND_DIR} && ./nas-backup -config ${CONFIG_FILE}"
    fi

    # ------------------------------------------------------------------
    # Step 10: Nginx（需 root）
    # ------------------------------------------------------------------
    if [[ "$NO_NGINX" == "false" && "$is_root" == "true" ]]; then
        step "配置 Nginx 反向代理"
        _setup_nginx "$FRONTEND_DIR" "$SKIP_FRONTEND"
    elif [[ "$NO_NGINX" == "false" ]]; then
        step "跳过 Nginx 配置（需 root）"
    fi

    # ------------------------------------------------------------------
    # Step 11: 启动后端服务（root + systemd）
    # ------------------------------------------------------------------
    if [[ "$is_root" == "true" ]]; then
        step "启动 NAS Backup 服务"
        systemctl restart nas-backup
        sleep 3
        if systemctl is-active --quiet nas-backup; then
            success "nas-backup 服务已启动"
        else
            warn "nas-backup 服务启动失败，查看日志: journalctl -u nas-backup -n 50"
            warn "手动调试: cd ${BACKEND_DIR} && ./nas-backup -config ${CONFIG_FILE}"
        fi
    else
        step "跳过服务启动（需 root 启动 systemd）"
    fi

    # ------------------------------------------------------------------
    # Step 12: 验证
    # ------------------------------------------------------------------
    step "验证构建"
    success "后端二进制: ${BACKEND_DIR}/nas-backup"
    [[ "$SKIP_FRONTEND" == "false" && -d "${FRONTEND_DIR}/dist" ]] && success "前端构建: ${FRONTEND_DIR}/dist/"
    success "配置目录: ${CONFIG_DIR}"
    success "数据目录: ${DATA_DIR}"
    [[ -f "${CONFIG_DIR}/master.key" ]] && success "主密钥: ${CONFIG_DIR}/master.key"
    success "rclone: ${BACKEND_DIR}/bin/rclone"

    if [[ "$is_root" == "true" ]] && systemctl is-active --quiet nas-backup 2>/dev/null; then
        step "验证 API 健康"
        _verify_health
    fi

    # ------------------------------------------------------------------
    # Step 13: 辅助脚本
    # ------------------------------------------------------------------
    step "创建辅助脚本"
    _create_helper_scripts "$INSTALL_DIR" "$BACKEND_DIR" "$CONFIG_FILE" "$DATA_DIR" "$is_root"

    # ------------------------------------------------------------------
    # 完成
    # ------------------------------------------------------------------
    local server_ip
    server_ip=$(get_server_ip)

    echo ""
    echo -e "${GREEN}${BOLD}======================================================================${NC}"
    if [[ "$is_root" == "true" ]]; then
        echo -e "${GREEN}${BOLD}  NAS Backup System - Debian 部署完成！${NC}"
    else
        echo -e "${GREEN}${BOLD}  NAS Backup System - 构建完成！${NC}"
    fi
    echo -e "${GREEN}${BOLD}======================================================================${NC}"
    echo ""
    echo -e "  ${BOLD}安装路径:${NC}"
    echo -e "    项目根:       ${INSTALL_DIR}"
    echo -e "    后端二进制:   ${BACKEND_DIR}/nas-backup"
    echo -e "    配置文件:     ${CONFIG_FILE}"
    echo -e "    数据目录:     ${DATA_DIR}"
    echo -e "    日志:         ${DATA_DIR}/logs/nas-backup.log"
    [[ "$SKIP_FRONTEND" == "false" ]] && echo -e "    前端构建:     ${FRONTEND_DIR}/dist/"
    echo ""
    if [[ "$is_root" == "true" ]]; then
        echo -e "  ${BOLD}服务管理:${NC}"
        echo -e "    启动:   sudo ./scripts/start.sh start"
        echo -e "    停止:   sudo ./scripts/start.sh stop"
        echo -e "    重启:   sudo ./scripts/start.sh restart"
        echo -e "    状态:   sudo ./scripts/start.sh status"
        echo -e "    日志:   sudo ./scripts/start.sh logs"
        echo ""
        echo -e "  ${BOLD}访问:${NC}"
        echo -e "    后端 API:  http://127.0.0.1:8080/api/"
        [[ "$NO_NGINX" == "false" ]] && echo -e "    Web UI:    http://${server_ip}:9000/"
    else
        echo -e "  ${BOLD}手动启动:${NC}"
        echo -e "    cd ${BACKEND_DIR} && sudo ./nas-backup -config ${CONFIG_FILE}"
        echo ""
        echo -e "  ${BOLD}安装 systemd 服务:${NC}"
        echo -e "    sudo bash $0 --skip-deps"
    fi
    echo ""
    echo -e "  ${BOLD}部署后必做:${NC}"
    echo -e "    1. 确认 config.yaml 中 OSS 凭证和备份目录正确"
    echo -e "    2. 通过 Web UI 添加备份目录或直接编辑 config.yaml"
    echo -e "    3. 修改 config.yaml 后重启: sudo ./scripts/start.sh restart"
    echo -e "    4. 检查存储健康: curl http://127.0.0.1:8080/api/storage/health"
    echo ""
}

# ------------------------------------------------------------------------------
# 安装/验证 Go
# ------------------------------------------------------------------------------
_ensure_go() {
    local is_root="$1"
    step "检查 Go 安装"

    local req_major=1 req_minor=25
    local go_installed=false go_bin=""

    if command -v go &>/dev/null; then
        go_bin="$(which go)"
        local ver
        ver="$(go version | awk '{print $3}' | sed 's/go//')"
        local major minor
        major="$(echo "$ver" | cut -d. -f1)"
        minor="$(echo "$ver" | cut -d. -f2)"
        if (( major > req_major )) || { (( major == req_major )) && (( minor >= req_minor )); }; then
            success "Go ${ver} 已安装 (${go_bin})"
            go_installed=true
        else
            warn "Go ${ver} 版本过低（需 >= ${req_major}.${req_minor}）"
        fi
    elif [[ -x /usr/local/go/bin/go ]]; then
        go_bin="/usr/local/go/bin/go"
        go_installed=true
        success "Go 已安装 (${go_bin})"
    fi

    if [[ "$go_installed" == "false" ]]; then
        local go_ver="1.26.2"
        if [[ "$is_root" == "true" ]]; then
            info "下载 Go ${go_ver} linux/${GOARCH} 到 /usr/local/go（系统级）..."
            rm -rf /usr/local/go
            curl -fSL --connect-timeout 60 "https://go.dev/dl/go${go_ver}.linux-${GOARCH}.tar.gz" -o /tmp/go.tar.gz 2>/dev/null || \
            wget -q --timeout=60 "https://go.dev/dl/go${go_ver}.linux-${GOARCH}.tar.gz" -O /tmp/go.tar.gz
            tar -C /usr/local -xzf /tmp/go.tar.gz
            rm -f /tmp/go.tar.gz /usr/local/bin/go
            grep -q '/usr/local/go/bin' /etc/profile.d/go.sh 2>/dev/null || \
                echo 'export PATH=$PATH:/usr/local/go/bin' > /etc/profile.d/go.sh
            chmod +x /etc/profile.d/go.sh
            export PATH="/usr/local/go/bin:$PATH"
        else
            local go_dir="$HOME/.local/go"
            info "下载 Go ${go_ver} linux/${GOARCH} 到 ${go_dir}（用户级）..."
            mkdir -p "$HOME/.local"
            rm -rf "$go_dir"
            curl -fSL --connect-timeout 60 "https://go.dev/dl/go${go_ver}.linux-${GOARCH}.tar.gz" -o /tmp/go.tar.gz 2>/dev/null || \
            wget -q --timeout=60 "https://go.dev/dl/go${go_ver}.linux-${GOARCH}.tar.gz" -O /tmp/go.tar.gz
            tar -C "$HOME/.local" -xzf /tmp/go.tar.gz
            rm -f /tmp/go.tar.gz
            export PATH="$HOME/.local/go/bin:$PATH"
            for rc in "$HOME/.bashrc" "$HOME/.profile"; do
                [[ -f "$rc" ]] && grep -q '.local/go/bin' "$rc" 2>/dev/null || \
                    echo 'export PATH="$HOME/.local/go/bin:$PATH"' >> "$rc"
            done
        fi
        success "Go ${go_ver} 已安装"
    fi

    # 确保 Go 在 PATH
    [[ -x /usr/local/go/bin/go ]] && export PATH="/usr/local/go/bin:$PATH"
    [[ -x "$HOME/.local/go/bin/go" ]] && export PATH="$HOME/.local/go/bin:$PATH"
    go version
}

# ------------------------------------------------------------------------------
# 安装/验证 Node.js
# ------------------------------------------------------------------------------
_ensure_node() {
    local is_root="$1"
    step "检查 Node.js 安装"

    local node_installed=false
    if command -v node &>/dev/null; then
        local major
        major="$(node -v | sed 's/v//' | cut -d. -f1)"
        if (( major >= 20 )); then
            success "Node.js $(node -v) 已安装 ($(which node))"
            node_installed=true
        else
            warn "Node.js $(node -v) 版本过低（需 >= 20）"
        fi
    fi

    if [[ "$node_installed" == "false" ]]; then
        local node_ver="v20.18.0"
        local node_arch="$GOARCH"
        [[ "$node_arch" == "amd64" ]] && node_arch="x64"
        [[ "$node_arch" == "arm64" ]] && node_arch="arm64"
        [[ "$node_arch" == "arm" ]]   && node_arch="armv7l"

        local node_base node_bin_dir
        if [[ "$is_root" == "true" ]]; then
            node_base="/usr/local/lib/nodejs"
            node_bin_dir="/usr/local/bin"
            info "下载 Node.js ${node_ver} linux-${node_arch} 到 ${node_base}（系统级）..."
        else
            node_base="$HOME/.local/lib/nodejs"
            node_bin_dir="$HOME/.local/bin"
            info "下载 Node.js ${node_ver} linux-${node_arch} 到 ${node_base}（用户级）..."
        fi

        local url="https://nodejs.org/dist/${node_ver}/node-${node_ver}-linux-${node_arch}.tar.xz"
        mkdir -p "$node_base" "$node_bin_dir"
        curl -fSL --connect-timeout 60 "$url" -o /tmp/node.tar.xz 2>/dev/null || \
        wget -q --timeout=60 "$url" -O /tmp/node.tar.xz
        tar -xJf /tmp/node.tar.xz -C "$node_base"
        rm -f /tmp/node.tar.xz

        ln -sf "$node_base/node-${node_ver}-linux-${node_arch}/bin/node" "$node_bin_dir/node"
        ln -sf "$node_base/node-${node_ver}-linux-${node_arch}/bin/npm"  "$node_bin_dir/npm"
        ln -sf "$node_base/node-${node_ver}-linux-${node_arch}/bin/npx"  "$node_bin_dir/npx"
        export PATH="$node_bin_dir:$PATH"
        success "Node.js $(node -v) 已安装"
    fi
    node --version
    npm --version
}

# ------------------------------------------------------------------------------
# 安装/验证 rclone
# ------------------------------------------------------------------------------
_ensure_rclone() {
    local is_root="$1"
    step "检查 rclone 安装"

    if command -v rclone &>/dev/null; then
        RCLONE_BIN="$(which rclone)"
        success "rclone 已安装: ${RCLONE_BIN} ($(rclone version 2>/dev/null | head -1))"
        return
    fi

    local rclone_ver="v1.75.0"
    if [[ "$is_root" == "true" ]]; then
        RCLONE_BIN="/usr/local/bin/rclone"
        info "下载 rclone ${rclone_ver} linux-${RCLONE_ARCH} 到 /usr/local/bin..."
    else
        mkdir -p "$HOME/.local/bin"
        RCLONE_BIN="$HOME/.local/bin/rclone"
        info "下载 rclone ${rclone_ver} linux-${RCLONE_ARCH} 到 ~/.local/bin..."
    fi

    local zip_arch
    case "$RCLONE_ARCH" in
        amd64)  zip_arch="amd64" ;;
        arm64)  zip_arch="arm64" ;;
        arm-v7) zip_arch="arm-v7" ;;
        *)      zip_arch="amd64" ;;
    esac

    local url="https://downloads.rclone.org/${rclone_ver}/rclone-${rclone_ver}-linux-${zip_arch}.zip"
    curl -fSL --connect-timeout 60 "$url" -o /tmp/rclone.zip 2>/dev/null || \
    wget -q --timeout=60 "$url" -O /tmp/rclone.zip
    mkdir -p /tmp/rclone-extract
    unzip -q -o /tmp/rclone.zip -d /tmp/rclone-extract
    cp "/tmp/rclone-extract/rclone-${rclone_ver}-linux-${zip_arch}/rclone" "$RCLONE_BIN"
    chmod +x "$RCLONE_BIN"
    rm -rf /tmp/rclone-extract /tmp/rclone.zip
    success "rclone 已安装: ${RCLONE_BIN}"
    export RCLONE_BIN
}

# ------------------------------------------------------------------------------
# 修正 config.yaml 中的绝对路径（从 macOS 迁移到 Debian 时）
# ------------------------------------------------------------------------------
_fix_config_paths() {
    local cfg="$1"
    local bak="${cfg}.bak.$(date +%Y%m%d%H%M%S)"
    cp "$cfg" "$bak"
    info "已备份原始配置: ${bak}"

    # rclone binary_path
    local rclone_path
    rclone_path="$(awk -F'binary_path:' '/binary_path:/{print $2; exit}' "$cfg" | awk -F'"' '{print $2}' | xargs)"
    if [[ -n "$rclone_path" ]] && [[ "$rclone_path" == /* ]] && [[ ! -f "$rclone_path" ]]; then
        local line
        line=$(grep -n 'binary_path:' "$cfg" | head -1 | cut -d: -f1)
        sed -i "${line}s|.*|  binary_path: \"./bin/rclone\"|" "$cfg"
        success "已修正 rclone binary_path 为 ./bin/rclone"
    fi

    # key_file_path
    local key_path
    key_path="$(awk -F'key_file_path:' '/key_file_path:/{print $2; exit}' "$cfg" | awk -F'"' '{print $2}' | xargs)"
    if [[ -n "$key_path" ]] && [[ "$key_path" == /* ]] && [[ ! -f "$key_path" ]]; then
        local line
        line=$(grep -n 'key_file_path:' "$cfg" | head -1 | cut -d: -f1)
        sed -i "${line}s|.*|  key_file_path: \"./config/master.key\"|" "$cfg"
        success "已修正 key_file_path 为 ./config/master.key"
    fi

    # database path
    local db_path
    db_path="$(awk '/^[[:space:]]+path:/{print; exit}' "$cfg" | awk -F'"' '{print $2}' | xargs)"
    if [[ -n "$db_path" ]] && [[ "$db_path" == /* ]] && [[ ! -d "$(dirname "$db_path")" ]]; then
        local line
        line=$(grep -nE '^[[:space:]]+path:' "$cfg" | head -1 | cut -d: -f1)
        sed -i "${line}s|.*|  path: \"./data/nas-backup.db\"|" "$cfg"
        success "已修正 database path 为 ./data/nas-backup.db"
    fi

    # log file_path
    local log_path
    log_path="$(awk '/^[[:space:]]+file_path:/{print; exit}' "$cfg" | awk -F'"' '{print $2}' | xargs)"
    if [[ -n "$log_path" ]] && [[ "$log_path" == /* ]] && [[ ! -d "$(dirname "$log_path")" ]]; then
        local line
        line=$(grep -nE '^[[:space:]]+file_path:' "$cfg" | grep -v key_file | head -1 | cut -d: -f1)
        [[ -n "$line" ]] && sed -i "${line}s|.*|  file_path: \"./data/logs/nas-backup.log\"|" "$cfg"
        success "已修正 log file_path"
    fi
}

# ------------------------------------------------------------------------------
# 配置 systemd 服务
# ------------------------------------------------------------------------------
_setup_systemd() {
    local backend_dir="$1"
    local config_file="$2"

    local service_file="/etc/systemd/system/nas-backup.service"
    cat > "$service_file" <<EOF
[Unit]
Description=NAS Backup Service
After=network.target

[Service]
Type=simple
WorkingDirectory=${backend_dir}
ExecStart=${backend_dir}/nas-backup -config ${config_file}
Restart=always
RestartSec=5
User=root

LimitNOFILE=65536
MemoryMax=512M

NoNewPrivileges=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
EOF

    success "systemd 服务文件: ${service_file}"
    systemctl daemon-reload
    systemctl enable nas-backup
    success "nas-backup 服务已启用开机自启"
}

# ------------------------------------------------------------------------------
# 配置 Nginx（自动检测 NAS 的 include 目录风格）
# ------------------------------------------------------------------------------
_setup_nginx() {
    local frontend_dir="$1"
    local skip_frontend="$2"

    if ! command -v nginx &>/dev/null; then
        warn "nginx 未安装，跳过 Nginx 配置"
        warn "后端 API 仍可直接访问: http://<server-ip>:8080"
        return
    fi

    local nginx_conf_dir="/etc/nginx"
    local include_dir=""

    # 检测 nginx.conf 实际 include 的目录（不同 NAS 厂商不同）
    if grep -qE 'include\s+/etc/nginx/server\.d/\*\.conf' "${nginx_conf_dir}/nginx.conf" 2>/dev/null \
       && [[ -d "${nginx_conf_dir}/server.d" ]]; then
        include_dir="${nginx_conf_dir}/server.d"
        info "检测到 UGREEN 风格 include 目录: ${include_dir}"
    elif grep -qE 'include\s+/etc/nginx/sites-enabled/\*' "${nginx_conf_dir}/nginx.conf" 2>/dev/null \
         && [[ -d "${nginx_conf_dir}/sites-enabled" ]]; then
        include_dir="${nginx_conf_dir}/sites-enabled"
        info "检测到 Debian 风格 include 目录: ${include_dir}"
    elif grep -qE 'include\s+/etc/nginx/conf\.d/\*\.conf' "${nginx_conf_dir}/nginx.conf" 2>/dev/null; then
        mkdir -p "${nginx_conf_dir}/conf.d"
        include_dir="${nginx_conf_dir}/conf.d"
        info "检测到 conf.d 风格 include 目录: ${include_dir}"
    else
        mkdir -p "${nginx_conf_dir}/sites-available" "${nginx_conf_dir}/sites-enabled" "${nginx_conf_dir}/conf.d"
        include_dir="${nginx_conf_dir}/conf.d"
        warn "无法自动检测 nginx include 目录，使用: ${include_dir}"
        warn "若 nginx 未加载配置，请在 nginx.conf 的 http {} 块内添加:"
        warn "    include /etc/nginx/conf.d/*.conf;"
    fi

    local nginx_src nginx_link=""
    if [[ "$include_dir" == "${nginx_conf_dir}/sites-enabled" ]]; then
        nginx_src="${nginx_conf_dir}/sites-available/nas-backup"
        nginx_link="${nginx_conf_dir}/sites-enabled/nas-backup"
    else
        nginx_src="${include_dir}/nas-backup.conf"
    fi

    # 清理旧配置
    rm -f "${nginx_conf_dir}/sites-available/nas-backup" 2>/dev/null || true
    rm -f "${nginx_conf_dir}/sites-enabled/nas-backup" 2>/dev/null || true
    rm -f "${nginx_conf_dir}/conf.d/nas-backup.conf" 2>/dev/null || true

    # 生成 Nginx 配置
    cat > "$nginx_src" <<'NGINXEOF'
server {
    listen 9000;
    listen [::]:9000;
    server_name _;
NGINXEOF

    if [[ -d "${frontend_dir}/dist" && -f "${frontend_dir}/dist/index.html" ]]; then
        cat >> "$nginx_src" <<EOF

    location / {
        root ${frontend_dir}/dist;
        index index.html;
        try_files \$uri \$uri/ /index.html;
    }
EOF
    elif [[ "$skip_frontend" == "false" ]]; then
        warn "前端 dist/ 不存在（构建可能失败），提供 API 占位"
        cat >> "$nginx_src" <<'EOF'

    location / {
        return 200 'NAS Backup API server running. Frontend build missing — API is available at /api/';
        default_type text/plain;
    }
EOF
    else
        cat >> "$nginx_src" <<'EOF'

    location / {
        return 200 'NAS Backup API server running. API is available at /api/';
        default_type text/plain;
    }
EOF
    fi

    cat >> "$nginx_src" <<'NGINXEOF'

    location /api/ {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;

        proxy_connect_timeout 60s;
        proxy_send_timeout 600s;
        proxy_read_timeout 600s;
    }

    location /api/backup/progress/stream {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Connection "";
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_buffering off;
        proxy_cache off;
        proxy_read_timeout 3600s;
        chunked_transfer_encoding on;
    }

    location /api/restore/progress/stream {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Connection "";
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_buffering off;
        proxy_cache off;
        proxy_read_timeout 3600s;
        chunked_transfer_encoding on;
    }

    gzip on;
    gzip_vary on;
    gzip_proxied any;
    gzip_comp_level 6;
    gzip_types text/plain text/css application/json application/javascript text/xml application/xml application/xml+rss text/javascript image/svg+xml;
    gzip_min_length 1000;

    location ~* \.(js|css|png|jpg|jpeg|gif|ico|svg|woff|woff2|ttf|eot)$ {
        expires 30d;
        add_header Cache-Control "public, immutable";
NGINXEOF

    [[ "$skip_frontend" == "false" ]] && echo "        root ${frontend_dir}/dist;" >> "$nginx_src"

    cat >> "$nginx_src" <<'NGINXEOF'
    }

    client_max_body_size 0;
}
NGINXEOF

    success "Nginx 配置已写入: ${nginx_src}"

    # 启用站点（仅 sites-available/sites-enabled 模式需要符号链接）
    if [[ -n "$nginx_link" ]]; then
        ln -sf "$nginx_src" "$nginx_link"
        rm -f "${nginx_conf_dir}/sites-enabled/default" 2>/dev/null || true
        success "已创建符号链接: ${nginx_link} -> ${nginx_src}"
    fi

    # 测试并重载
    if nginx -t 2>&1; then
        success "Nginx 配置有效"
        systemctl enable nginx 2>/dev/null || true
        # UGREEN NAS restart 会触发 reset_nginx_config 钩子清掉自定义配置，优先 reload
        if ! systemctl restart nginx 2>/dev/null && ! service nginx restart 2>/dev/null; then
            warn "systemctl restart 失败，尝试 nginx -s reload..."
            nginx -s reload 2>&1 || warn "nginx reload 也失败"
        fi
        sleep 2
        if port_listening 9000; then
            success "Nginx 端口 9000 已监听"
        else
            warn "端口 9000 未监听"
            warn "可能原因: UGREEN reset_nginx_config 钩子 / 端口被占用 / include 路径未检测"
            warn "排查: sudo nginx -T 2>/dev/null | grep -A 20 'listen.*9000'"
        fi
    else
        warn "Nginx 配置测试失败 - 未启动。后端 API 仍可用: http://127.0.0.1:8080"
        warn "配置文件: ${nginx_src}"
    fi
}

# ------------------------------------------------------------------------------
# 验证 API 健康
# ------------------------------------------------------------------------------
_verify_health() {
    sleep 2
    local backend_ok=false
    for i in {1..10}; do
        if curl -s --connect-timeout 2 http://127.0.0.1:8080/api/dashboard/stats >/dev/null 2>&1; then
            backend_ok=true
            break
        fi
        sleep 1
    done

    if [[ "$backend_ok" == "true" ]]; then
        success "后端 API 响应正常 (http://127.0.0.1:8080)"
        curl -s http://127.0.0.1:8080/api/dashboard/stats | head -c 200; echo ""
    else
        warn "后端 API 未响应，查看日志: journalctl -u nas-backup -f"
    fi

    local storage_resp
    storage_resp=$(curl -s http://127.0.0.1:8080/api/storage/health 2>/dev/null || echo "{}")
    if echo "$storage_resp" | grep -q '"status":"ok"'; then
        success "存储/OSS 连接健康"
    else
        warn "存储健康检查: ${storage_resp}"
        warn "如使用 OSS，请确认 config.yaml 中的凭证"
    fi

    if [[ "$NO_NGINX" == "false" ]] && port_listening 9000; then
        if curl -s --connect-timeout 2 http://127.0.0.1:9000/api/dashboard/stats >/dev/null 2>&1; then
            success "Nginx 反向代理正常 (端口 9000)"
        else
            warn "Nginx 代理检查失败"
        fi
    fi
}

# ------------------------------------------------------------------------------
# 创建辅助脚本（DB 备份 + 管理脚本）
# ------------------------------------------------------------------------------
_create_helper_scripts() {
    local install_dir="$1"
    local backend_dir="$2"
    local config_file="$3"
    local data_dir="$4"
    local is_root="$5"

    # 确定实际 DB 路径
    local db_path="${data_dir}/nas-backup.db"
    if [[ -f "$config_file" ]]; then
        local parsed
        parsed="$(grep -E '^\s+path:' "$config_file" | head -1 | sed 's/.*path:\s*["'\'']\?\(.*\)["'\'']\?/\1/' | xargs || echo "./data/nas-backup.db")"
        [[ "$parsed" != /* ]] && parsed="${backend_dir}/${parsed#./}"
        db_path="$parsed"
    fi

    # DB 备份脚本
    local db_backup="${install_dir}/backup-db.sh"
    cat > "$db_backup" <<EOF
#!/bin/bash
# NAS Backup - 数据库备份脚本
BACKUP_DIR="\$(cd "\$(dirname "\$0")" && pwd)/db-backups"
DB_PATH="${db_path}"
DATE=\$(date +%Y%m%d_%H%M%S)
KEEP=30

mkdir -p "\$BACKUP_DIR"
if [[ -f "\$DB_PATH" ]]; then
    sqlite3 "\$DB_PATH" ".backup '\${BACKUP_DIR}/nas-backup_\${DATE}.db'"
    ls -t "\$BACKUP_DIR"/nas-backup_*.db 2>/dev/null | tail -n +\$((KEEP+1)) | xargs -r rm
    echo "数据库已备份: \${BACKUP_DIR}/nas-backup_\${DATE}.db"
else
    echo "数据库文件不存在: \$DB_PATH（首次运行前可能不存在）"
fi
EOF
    chmod +x "$db_backup"
    success "数据库备份脚本: ${db_backup}"

    # 添加到 crontab（root 才能操作系统 cron）
    if [[ "$is_root" == "true" ]]; then
        if ! crontab -l 2>/dev/null | grep -q "backup-db.sh"; then
            (crontab -l 2>/dev/null; echo "0 2 * * * ${db_backup} >> ${data_dir}/logs/db-backup.log 2>&1") | crontab -
            success "已添加每日数据库备份 cron 任务（2:00 AM）"
        fi
    fi

    # 管理脚本（指向统一的 start.sh）
    local manage="${install_dir}/manage.sh"
    cat > "$manage" <<EOF
#!/bin/bash
# NAS Backup - 管理脚本（转发到 start.sh）
set -euo pipefail
case "\${1:-status}" in
    start|stop|stop-all|restart|reload|status|health)
        exec ${install_dir}/scripts/start.sh "\$@"
        ;;
    logs)
        exec ${install_dir}/scripts/start.sh "\$@"
        ;;
    db-backup)
        ${db_backup}
        ;;
    run)
        cd ${backend_dir} && ./nas-backup -config ${config_file}
        ;;
    *)
        echo "用法: \$0 {start|stop|restart|status|health|logs|db-backup|run}"
        exit 1 ;;
esac
EOF
    chmod +x "$manage"
    success "管理脚本: ${manage}"
}
