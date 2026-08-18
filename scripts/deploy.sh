#!/bin/bash
# ==============================================================================
# NAS Backup System - 统一部署脚本（macOS + Debian 自动适配）
# ==============================================================================
# 自动检测操作系统，按平台执行对应部署流程：
#
#   macOS  (本地开发/测试):
#     - Homebrew 安装依赖（rclone/zstd/go/node/python3/openssl）
#     - 默认连接真实阿里云 OSS（生产模式；如需本地模拟测试加 --local）
#     - 构建后端 + 前端
#     - 生成启动便捷脚本
#
#   Debian (生产 NAS / 绿联/极空间/群晖等):
#     - 安全模式 apt（仅装缺失包，绝不 upgrade，避免破坏 NAS 固件）
#     - Go/Node.js/rclone 官方二进制下载（不依赖 apt 版本）
#     - systemd 服务 + Nginx 反向代理（需 root）
#     - 编译后端 + 构建前端
#
# 用法:
#   ./scripts/deploy.sh                       # 生产部署（macOS 默认真实 OSS；Debian 需 sudo）
#   ./scripts/deploy.sh --local               # macOS 本地模拟测试（离线，不碰真实 OSS）
#   ./scripts/deploy.sh --platform macos      # 强制 macOS 模式
#   ./scripts/deploy.sh --platform debian     # 强制 Debian 模式
#   sudo ./scripts/deploy.sh                  # Debian 生产部署（含 systemd + Nginx）
#
# 通用选项:
#   --local              本地文件系统模拟云存储（仅 macOS 测试；默认走真实 OSS）
#   --skip-deps           跳过系统依赖安装
#   --skip-frontend       跳过前端构建（Debian 模式，仅后端部署）
#   --no-nginx            跳过 Nginx 配置（Debian 模式）
#   --install-dir DIR     指定安装目录（Debian 模式，默认自动检测）
#   --platform macos|debian  强制指定平台（默认自动检测）
#   --help                显示帮助
# ==============================================================================

set -euo pipefail

# ------------------------------------------------------------------------------
# 加载公共函数库
# ------------------------------------------------------------------------------
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib/common.sh
source "${SCRIPT_DIR}/lib/common.sh"

enhance_path

# ------------------------------------------------------------------------------
# 解析参数
# ------------------------------------------------------------------------------
FORCE_PLATFORM=""
SKIP_DEPS=false
SKIP_FRONTEND=false
NO_NGINX=false
INSTALL_DIR=""
# 默认生产：真实阿里云 OSS。仅 macOS 测试用 --local 切换为本地模拟。
USE_REAL_OSS=true

while [[ $# -gt 0 ]]; do
    case "$1" in
        --platform)       FORCE_PLATFORM="$2"; shift 2 ;;
        --skip-deps)      SKIP_DEPS=true; shift ;;
        --skip-frontend)  SKIP_FRONTEND=true; shift ;;
        --no-nginx)       NO_NGINX=true; shift ;;
        --install-dir)    INSTALL_DIR="$2"; shift 2 ;;
        --local)          USE_REAL_OSS=false; shift ;;
        --with-oss)       USE_REAL_OSS=true; shift ;;   # 向后兼容，等价于默认
        -h|--help)
            awk 'NR>=2 && NR<=32 {sub(/^# ?/,""); print}' "$0"
            exit 0 ;;
        *)
            echo "未知选项: $1"
            exit 1 ;;
    esac
done

# ------------------------------------------------------------------------------
# 平台检测
# ------------------------------------------------------------------------------
detect_platform

if [[ -n "$FORCE_PLATFORM" ]]; then
    OS_TYPE="$FORCE_PLATFORM"
    info "强制指定平台: ${OS_TYPE}"
else
    success "检测到平台: ${OS_TYPE} (${ARCH})"
fi

# ------------------------------------------------------------------------------
# 按平台分发
# ------------------------------------------------------------------------------
case "$OS_TYPE" in
    macos)  source "${SCRIPT_DIR}/lib/deploy-macos.sh" ;;
    debian) source "${SCRIPT_DIR}/lib/deploy-debian.sh" ;;
    linux-other)
        warn "检测到非 Debian 的 Linux 系统，将按 Debian 流程部署（兼容模式）"
        OS_TYPE="debian"
        source "${SCRIPT_DIR}/lib/deploy-debian.sh"
        ;;
    *)
        fail "不支持的平台: ${OS_TYPE}"
        ;;
esac

# 调用平台特定的主部署函数
deploy_main
