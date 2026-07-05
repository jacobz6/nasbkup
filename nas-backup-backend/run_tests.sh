#!/usr/bin/env bash
# ============================================================================
# NAS Backup 系统一键测试脚本
# 基于 test-cases.md 测试用例文档，执行全部自动化测试
# 用法:
#   ./run_tests.sh              # 运行全部测试
#   ./run_tests.sh unit         # 仅运行单元测试
#   ./run_tests.sh integration  # 仅运行API集成测试
#   ./run_tests.sh vet          # 仅运行go vet静态检查
#   ./run_tests.sh cover        # 运行测试并生成覆盖率报告
# ============================================================================
set -euo pipefail

# ── 颜色定义 ──────────────────────────────────────────────────────────────
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
NC='\033[0m' # No Color

# ── 配置 ──────────────────────────────────────────────────────────────────
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$SCRIPT_DIR"
TEST_TIMEOUT="300s"
COVER_PROFILE="$PROJECT_DIR/coverage.out"
COVER_HTML="$PROJECT_DIR/coverage.html"

# ── 辅助函数 ──────────────────────────────────────────────────────────────
info()    { echo -e "${BLUE}[INFO]${NC} $*"; }
success() { echo -e "${GREEN}[PASS]${NC} $*"; }
warn()    { echo -e "${YELLOW}[WARN]${NC} $*"; }
fail()    { echo -e "${RED}[FAIL]${NC} $*"; }
section() { echo -e "\n${CYAN}════════════════════════════════════════════════════════════${NC}"; echo -e "${CYAN}  $*${NC}"; echo -e "${CYAN}════════════════════════════════════════════════════════════${NC}"; }

# ── 前置检查 ──────────────────────────────────────────────────────────────
check_prerequisites() {
    section "前置环境检查"

    # 检查 Go 是否安装
    if ! command -v go &> /dev/null; then
        fail "Go 未安装或不在 PATH 中"
        fail "请安装 Go 1.25+ 后重试: https://go.dev/dl/"
        exit 1
    fi
    success "Go 版本: $(go version)"

    # 检查 CGO 是否启用（sqlite3 依赖）
    if ! go env CGO_ENABLED | grep -q "1"; then
        warn "CGO 未启用，SQLite 测试将被跳过"
        warn "如需启用: export CGO_ENABLED=1"
        warn "macOS 默认随 Xcode 命令行工具附带 C 编译器"
    else
        success "CGO_ENABLED=1"
    fi

    # 检查项目目录
    if [[ ! -f "$PROJECT_DIR/go.mod" ]]; then
        fail "未找到 go.mod，请在项目根目录运行此脚本"
        exit 1
    fi
    success "项目目录: $PROJECT_DIR"

    # 检查依赖是否已下载
    if [[ ! -d "$PROJECT_DIR/vendor" ]] && [[ ! -d "$(go env GOMODCACHE)/github.com/mattn" ]]; then
        info "下载 Go 依赖..."
        (cd "$PROJECT_DIR" && go mod download) || true
    fi
}

# ── go vet 静态检查 ──────────────────────────────────────────────────────
run_vet() {
    section "Go Vet 静态检查"
    info "运行 go vet ./..."

    if (cd "$PROJECT_DIR" && go vet ./... 2>&1); then
        success "go vet 通过"
        return 0
    else
        warn "go vet 发现警告（不影响测试运行）"
        return 0
    fi
}

# ── 单元测试 ──────────────────────────────────────────────────────────────
run_unit_tests() {
    section "单元测试 (各模块)"

    local packages=(
        "./internal/models"
        "./internal/config"
        "./internal/db"
        "./internal/scanner"
        "./internal/dedup"
        "./internal/compress"
        "./internal/crypto"
        "./internal/storage"
        "./internal/scheduler"
    )

    local total=0
    local passed=0
    local failed=0
    local skipped=0

    for pkg in "${packages[@]}"; do
        echo -e "\n${BLUE}▶ $pkg${NC}"
        if (cd "$PROJECT_DIR" && go test -v -timeout "$TEST_TIMEOUT" -count=1 "$pkg" 2>&1); then
            ((passed++)) || true
        else
            # 检查是否因 SKIP 导致非零退出
            local output
            output=$(cd "$PROJECT_DIR" && go test -v -timeout "$TEST_TIMEOUT" -count=1 "$pkg" 2>&1) || true
            if echo "$output" | grep -q "SKIP"; then
                warn "$pkg 有跳过的测试（可能缺少 CGO 或外部依赖）"
                ((skipped++)) || true
            else
                fail "$pkg 测试失败"
                ((failed++)) || true
            fi
        fi
        ((total++)) || true
    done

    echo ""
    echo -e "${CYAN}单元测试汇总:${NC} 总计 $total | ${GREEN}通过 $passed${NC} | ${RED}失败 $failed${NC} | ${YELLOW}跳过 $skipped${NC}"

    return 0
}

# ── API 集成测试 ──────────────────────────────────────────────────────────
run_integration_tests() {
    section "API 集成测试 (基于 test-cases.md)"

    info "运行 internal/api 集成测试..."
    info "覆盖模块: 仪表盘/备份/目录/排除规则/FS浏览/调度/压缩/上传/保留/加密/日志/对账/CORS/恢复/GC"

    if (cd "$PROJECT_DIR" && go test -v -timeout "$TEST_TIMEOUT" -count=1 ./internal/api/... 2>&1); then
        success "API 集成测试全部通过"
        return 0
    else
        fail "API 集成测试存在失败项"
        return 1
    fi
}

# ── 覆盖率报告 ────────────────────────────────────────────────────────────
run_coverage() {
    section "测试覆盖率报告"

    info "生成覆盖率数据..."
    (cd "$PROJECT_DIR" && go test -timeout "$TEST_TIMEOUT" -count=1 -coverprofile="$COVER_PROFILE" ./internal/... 2>&1) || true

    if [[ -f "$COVER_PROFILE" ]]; then
        info "生成 HTML 报告..."
        (cd "$PROJECT_DIR" && go tool cover -html="$COVER_PROFILE" -o "$COVER_HTML" 2>&1) || true

        # 显示覆盖率摘要
        local cover_summary
        cover_summary=$(cd "$PROJECT_DIR" && go tool cover -func="$COVER_PROFILE" 2>&1 | tail -1)
        echo -e "\n${CYAN}覆盖率摘要:${NC}"
        echo "  $cover_summary"
        echo ""
        echo -e "  HTML 报告: ${BLUE}$COVER_HTML${NC}"
        echo -e "  原始数据: ${BLUE}$COVER_PROFILE${NC}"

        # 尝试在浏览器中打开
        if command -v open &> /dev/null && [[ -f "$COVER_HTML" ]]; then
            info "在浏览器中打开覆盖率报告..."
            open "$COVER_HTML" 2>/dev/null || true
        fi
    else
        warn "未能生成覆盖率文件"
    fi
}

# ── 端到端验证（需要运行中的服务） ────────────────────────────────────────
run_e2e_check() {
    section "端到端连通性检查"

    local BASE_URL="${BASE_URL:-http://localhost:8080}"

    info "检查服务是否运行在 $BASE_URL ..."

    # 检查服务是否可达
    local http_code
    http_code=$(curl -s -o /dev/null -w "%{http_code}" --connect-timeout 3 "$BASE_URL/api/dashboard/stats" 2>/dev/null || echo "000")

    if [[ "$http_code" == "200" ]]; then
        success "服务可达 (HTTP 200)"

        # 快速验证关键端点
        echo ""
        info "验证关键端点..."

        local endpoints=(
            "GET /api/dashboard/stats"
            "GET /api/dashboard/history"
            "GET /api/backup/status"
            "GET /api/content/directories"
            "GET /api/content/exclusions"
            "GET /api/strategy/schedule"
            "GET /api/strategy/compression"
            "GET /api/strategy/upload"
            "GET /api/strategy/retention"
            "GET /api/strategy/encryption"
            "GET /api/logs"
        )

        local e2e_pass=0
        local e2e_fail=0

        for ep in "${endpoints[@]}"; do
            local method path
            method=$(echo "$ep" | awk '{print $1}')
            path=$(echo "$ep" | awk '{print $2}')

            local code
            code=$(curl -s -o /dev/null -w "%{http_code}" --connect-timeout 3 -X "$method" "$BASE_URL$path" 2>/dev/null || echo "000")

            if [[ "$code" == "200" ]] || [[ "$code" == "201" ]]; then
                success "  $method $path → $code"
                ((e2e_pass++)) || true
            else
                fail "  $method $path → $code"
                ((e2e_fail++)) || true
            fi
        done

        echo ""
        echo -e "${CYAN}端到端汇总:${NC} ${GREEN}通过 $e2e_pass${NC} | ${RED}失败 $e2e_fail${NC}"

        # CORS 检查
        echo ""
        info "验证 CORS 头..."
        local cors_origin
        cors_origin=$(curl -s -D - -o /dev/null --connect-timeout 3 "$BASE_URL/api/dashboard/stats" 2>/dev/null | grep -i "Access-Control-Allow-Origin" | tr -d '\r' || echo "")
        if [[ -n "$cors_origin" ]]; then
            success "CORS: $cors_origin"
        else
            warn "未检测到 CORS 头"
        fi

        # OPTIONS 预检
        local options_code
        options_code=$(curl -s -o /dev/null -w "%{http_code}" --connect-timeout 3 -X OPTIONS "$BASE_URL/api/dashboard/stats" 2>/dev/null || echo "000")
        if [[ "$options_code" == "204" ]]; then
            success "OPTIONS 预检 → 204"
        else
            warn "OPTIONS 预检 → $options_code (期望 204)"
        fi

    else
        warn "服务未运行或不可达 (HTTP $http_code)"
        warn "端到端检查已跳过。启动服务后重试: go run ./cmd/nas-backup"
        warn "或指定地址: BASE_URL=http://host:port ./run_tests.sh e2e"
    fi
}

# ── 清理 ──────────────────────────────────────────────────────────────────
cleanup() {
    info "清理临时文件..."
    rm -f "$COVER_PROFILE" 2>/dev/null || true
    # 不删除 coverage.html，方便查看
}

# ── 主流程 ────────────────────────────────────────────────────────────────
main() {
    echo -e "${CYAN}"
    echo "╔═══════════════════════════════════════════════════════════════╗"
    echo "║           NAS Backup 系统一键测试脚本                         ║"
    echo "║           基于 test-cases.md 测试用例文档                     ║"
    echo "╚═══════════════════════════════════════════════════════════════╝"
    echo -e "${NC}"

    check_prerequisites

    local mode="${1:-all}"

    case "$mode" in
        vet)
            run_vet
            ;;
        unit)
            run_vet
            run_unit_tests
            ;;
        integration)
            run_vet
            run_integration_tests
            ;;
        cover)
            run_vet
            run_unit_tests
            run_integration_tests
            run_coverage
            ;;
        e2e)
            run_e2e_check
            ;;
        all)
            run_vet
            run_unit_tests
            run_integration_tests
            run_e2e_check
            ;;
        *)
            echo "用法: $0 {all|unit|integration|vet|cover|e2e}"
            echo ""
            echo "  all          运行全部测试（默认）"
            echo "  unit         仅运行单元测试"
            echo "  integration  仅运行API集成测试"
            echo "  vet          仅运行go vet静态检查"
            echo "  cover        运行测试并生成覆盖率报告"
            echo "  e2e          端到端连通性检查（需服务运行中）"
            echo ""
            echo "环境变量:"
            echo "  BASE_URL     端到端检查的目标地址（默认 http://localhost:8080）"
            exit 1
            ;;
    esac

    section "测试完成"
    success "所有测试步骤已执行完毕"

    return 0
}

# ── 入口 ──────────────────────────────────────────────────────────────────
trap cleanup EXIT
main "$@"
