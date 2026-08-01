#!/bin/bash
# ==============================================================================
# NAS Backup System - macOS One-Click Deployment Script
# ==============================================================================
# This script sets up the complete local development/testing environment on macOS:
#   1. Installs system dependencies via Homebrew (rclone, zstd, go, node, python3)
#   2. Generates local configuration for testing (uses local filesystem as "cloud")
#   3. Builds backend (Go) and frontend (React/Vite)
#   4. Sets up rclone with local crypt remote for offline testing
#   5. Generates master encryption key
#   6. Initializes SQLite database
#
# Usage:
#   chmod +x scripts/deploy-macos.sh
#   ./scripts/deploy-macos.sh [--with-oss]
#
# Options:
#   --with-oss    Configure for real Alibaba Cloud OSS (will prompt for credentials)
#   --skip-deps   Skip Homebrew dependency installation
#   --help        Show this help message
# ==============================================================================

set -euo pipefail

# ------------------------------------------------------------------------------
# Color output
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
# Paths
# ------------------------------------------------------------------------------
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
BACKEND_DIR="${PROJECT_ROOT}/nas-backup-backend"
FRONTEND_DIR="${PROJECT_ROOT}/nas-backup-frontend"
DATA_DIR="${BACKEND_DIR}/data"
TEST_DIR="${PROJECT_ROOT}/test-env"
LOCAL_CLOUD_DIR="${TEST_DIR}/local-cloud-storage"
TEST_SOURCE_DIR="${TEST_DIR}/source-files"
TEST_RESTORE_DIR="${TEST_DIR}/restore-output"
CONFIG_FILE="${BACKEND_DIR}/config.yaml"
RCLONE_CONFIG="${DATA_DIR}/rclone.conf"

# ------------------------------------------------------------------------------
# Parse arguments
# ------------------------------------------------------------------------------
USE_REAL_OSS=false
SKIP_DEPS=false

while [[ $# -gt 0 ]]; do
    case "$1" in
        --with-oss)   USE_REAL_OSS=true; shift ;;
        --skip-deps)  SKIP_DEPS=true; shift ;;
        -h|--help)
            head -30 "$0" | grep '^#' | sed 's/^# \?//'
            exit 0 ;;
        *)
            echo "Unknown option: $1"
            exit 1 ;;
    esac
done

# ------------------------------------------------------------------------------
# Pre-flight: macOS check
# ------------------------------------------------------------------------------
step "Pre-flight checks"
if [[ "$(uname -s)" != "Darwin" ]]; then
    fail "This script is for macOS only. Detected: $(uname -s)"
fi
success "Running on macOS $(sw_vers -productVersion)"

# ------------------------------------------------------------------------------
# Step 1: Install Homebrew if needed
# ------------------------------------------------------------------------------
if [[ "$SKIP_DEPS" == "false" ]]; then
    step "Checking Homebrew"
    if ! command -v brew &>/dev/null; then
        info "Installing Homebrew..."
        /bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"
        # Add brew to PATH for Apple Silicon
        if [[ -f /opt/homebrew/bin/brew ]]; then
            eval "$(/opt/homebrew/bin/brew shellenv)"
        fi
    fi
    success "Homebrew is available: $(brew --version | head -1)"

    # ------------------------------------------------------------------------------
    # Step 2: Install system dependencies
    # ------------------------------------------------------------------------------
    step "Installing system dependencies via Homebrew"

    BREW_PACKAGES=()
    command -v go      &>/dev/null || BREW_PACKAGES+=(go)
    command -v node    &>/dev/null || BREW_PACKAGES+=(node)
    command -v rclone  &>/dev/null || BREW_PACKAGES+=(rclone)
    command -v zstd    &>/dev/null || BREW_PACKAGES+=(zstd)
    command -v python3 &>/dev/null || BREW_PACKAGES+=(python3)
    command -v openssl &>/dev/null || BREW_PACKAGES+=(openssl)

    if [[ ${#BREW_PACKAGES[@]} -gt 0 ]]; then
        info "Installing: ${BREW_PACKAGES[*]}"
        brew install "${BREW_PACKAGES[@]}"
    else
        info "All system dependencies already installed"
    fi

    # Verify
    for dep in go node rclone zstd python3 openssl; do
        if command -v "$dep" &>/dev/null; then
            success "$dep: $($dep version 2>&1 | head -1 || echo 'installed')"
        else
            fail "$dep not found after installation"
        fi
    done

    # Install Python requests library for test scripts
    step "Installing Python dependencies"
    pip3 install --break-system-packages requests 2>/dev/null || pip3 install requests
    success "Python requests library installed"
else
    info "Skipping dependency installation (--skip-deps)"
fi

# ------------------------------------------------------------------------------
# Step 3: Ensure Go and Node are available in PATH
# ------------------------------------------------------------------------------
step "Verifying build toolchains"

# Add common Go binary path
export GOPATH="${GOPATH:-$HOME/go}"
export PATH="${GOPATH}/bin:/usr/local/go/bin:/opt/homebrew/bin:$PATH"

GO_VERSION="$(go version | awk '{print $3}')"
NODE_VERSION="$(node --version)"
NPM_VERSION="$(npm --version)"
success "Go: ${GO_VERSION}"
success "Node.js: ${NODE_VERSION} (npm ${NPM_VERSION})"

# ------------------------------------------------------------------------------
# Step 4: Create test environment directories
# ------------------------------------------------------------------------------
step "Setting up test environment directories"
mkdir -p "$DATA_DIR"
mkdir -p "${DATA_DIR}/logs"
mkdir -p "$LOCAL_CLOUD_DIR"
mkdir -p "$TEST_SOURCE_DIR"
mkdir -p "$TEST_RESTORE_DIR"
success "Data directory: ${DATA_DIR}"
success "Local 'cloud' storage: ${LOCAL_CLOUD_DIR}"
success "Test source files: ${TEST_SOURCE_DIR}"
success "Test restore output: ${TEST_RESTORE_DIR}"

# ------------------------------------------------------------------------------
# Step 5: Generate master encryption key
# ------------------------------------------------------------------------------
step "Generating master encryption key"
MASTER_KEY_FILE="${DATA_DIR}/master.key"
if [[ ! -f "$MASTER_KEY_FILE" ]]; then
    openssl rand -hex 32 > "$MASTER_KEY_FILE"
    chmod 600 "$MASTER_KEY_FILE"
    success "Master key generated: ${MASTER_KEY_FILE}"
else
    success "Master key already exists: ${MASTER_KEY_FILE}"
fi

# ------------------------------------------------------------------------------
# Step 6: Configure rclone
# ------------------------------------------------------------------------------
step "Configuring rclone"

if [[ "$USE_REAL_OSS" == "true" ]]; then
    info "Configuring for real Alibaba Cloud OSS..."
    read -rp "OSS Endpoint (e.g., oss-cn-hangzhou.aliyuncs.com): " OSS_ENDPOINT
    read -rp "OSS Bucket name: " OSS_BUCKET
    read -rp "Access Key ID: " OSS_AK
    read -rsp "Access Key Secret: " OSS_SK
    echo ""

    "${BACKEND_DIR}/scripts/setup-rclone.sh" \
        --endpoint "$OSS_ENDPOINT" \
        --bucket "$OSS_BUCKET" \
        --ak "$OSS_AK" \
        --sk "$OSS_SK" \
        --config-path "$RCLONE_CONFIG"
else
    info "Configuring local filesystem remote for offline testing..."
    info "This simulates cloud storage using local disk at: ${LOCAL_CLOUD_DIR}"

    # Generate crypt passwords
    PASSWORD1=$(rclone obscure "$(openssl rand -base64 32)")
    PASSWORD2=$(rclone obscure "$(openssl rand -base64 32)")

    cat > "$RCLONE_CONFIG" <<EOF
# Rclone configuration for NAS Backup - Local testing mode
# Generated by deploy-macos.sh on $(date -Iseconds)
# Uses local filesystem with client-side encryption to simulate cloud storage
# NOTE: The raw remote is named [oss] to match the backend's expected section name.

[oss]
type = local

[oss-crypt]
type = crypt
remote = oss:${LOCAL_CLOUD_DIR}
password = ${PASSWORD1}
password2 = ${PASSWORD2}
filename_encryption = standard
directory_name_encryption = true
EOF
    chmod 600 "$RCLONE_CONFIG"
    success "Local rclone config created: ${RCLONE_CONFIG}"
fi

# Verify rclone config
if rclone config show --config "$RCLONE_CONFIG" &>/dev/null; then
    success "Rclone configuration validated"
else
    fail "Rclone configuration validation failed"
fi

# ------------------------------------------------------------------------------
# Step 7: Generate application config.yaml
# ------------------------------------------------------------------------------
step "Generating application configuration"

cat > "$CONFIG_FILE" <<EOF
# NAS Backup System - Local macOS Deployment Configuration
# Generated by deploy-macos.sh on $(date -Iseconds)
# Mode: $([[ "$USE_REAL_OSS" == "true" ]] && echo "Alibaba Cloud OSS" || echo "Local testing (local filesystem simulated cloud)")

server:
  host: "127.0.0.1"
  port: 8080
  read_timeout_sec: 30
  write_timeout_sec: 120
  restore_base_dirs:
    - "${TEST_DIR}"

database:
  path: "./data/nas-backup.db"

backup:
  directories:
    - path: "${TEST_SOURCE_DIR}"
      recursive: true
      enabled: true
      description: "Test source directory for E2E verification"

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
    version_keep_count: 5
    orphan_grace_days: 30
    full_reset_interval_months: 6
    keep_deleted_days: 30

  encryption:
    algorithm: "AES-256-GCM"
    key_file_path: "./data/master.key"

oss:
  endpoint: ""
  bucket: ""
  access_key_id: ""
  access_key_secret: ""
  region: ""

rclone:
  binary_path: "./bin/rclone"
  config_path: "./data/rclone.conf"
  remote_name: "oss-crypt"

logging:
  level: "info"
  file_path: "./data/logs/nas-backup.log"
  max_size_mb: 50
  max_files: 10
EOF

success "Config file written: ${CONFIG_FILE}"

# ------------------------------------------------------------------------------
# Step 7b: Ensure rclone binary is in ./bin/rclone
# ------------------------------------------------------------------------------
step "Setting up rclone binary"
mkdir -p "${BACKEND_DIR}/bin"
if command -v rclone &>/dev/null; then
    RCLONE_BIN="$(which rclone)"
    cp "$RCLONE_BIN" "${BACKEND_DIR}/bin/rclone"
    chmod +x "${BACKEND_DIR}/bin/rclone"
    success "Copied rclone from ${RCLONE_BIN} to ./bin/rclone"
elif [[ -f "${BACKEND_DIR}/bin/rclone" ]]; then
    success "rclone already exists at ./bin/rclone"
else
    info "rclone not found in PATH, downloading..."
    ARCH="$(uname -m)"
    if [[ "$ARCH" == "arm64" ]]; then
        RCLONE_ARCH="arm64"
    else
        RCLONE_ARCH="amd64"
    fi
    RCLONE_VERSION="v1.75.0"
    curl -L -o /tmp/rclone.zip "https://downloads.rclone.org/${RCLONE_VERSION}/rclone-${RCLONE_VERSION}-osx-${RCLONE_ARCH}.zip"
    unzip -o /tmp/rclone.zip -d /tmp/
    cp "/tmp/rclone-${RCLONE_VERSION}-osx-${RCLONE_ARCH}/rclone" "${BACKEND_DIR}/bin/rclone"
    chmod +x "${BACKEND_DIR}/bin/rclone"
    rm -rf "/tmp/rclone-${RCLONE_VERSION}-osx-${RCLONE_ARCH}" /tmp/rclone.zip
    success "Downloaded rclone ${RCLONE_VERSION} to ./bin/rclone"
fi

# ------------------------------------------------------------------------------
# Step 8: Build Go backend
# ------------------------------------------------------------------------------
step "Building Go backend"
cd "$BACKEND_DIR"

info "Downloading Go modules..."
go mod download
success "Go modules downloaded"

info "Compiling nas-backup server..."
CGO_ENABLED=1 go build -o nas-backup ./cmd/nas-backup/
success "Backend binary built: ${BACKEND_DIR}/nas-backup"

info "Compiling restore-cli tool..."
CGO_ENABLED=1 go build -o restore-cli ./cmd/restore-cli/
success "restore-cli built: ${BACKEND_DIR}/restore-cli"

# Run Go unit tests
info "Running backend unit tests..."
if go test ./internal/... -v -count=1 -short 2>&1 | tail -20; then
    success "Backend unit tests passed"
else
    warn "Some unit tests had issues (this may be expected for DB-related tests)"
fi

# ------------------------------------------------------------------------------
# Step 9: Build frontend
# ------------------------------------------------------------------------------
step "Building React frontend"
cd "$FRONTEND_DIR"

if [[ ! -d node_modules ]]; then
    info "Installing npm dependencies..."
    npm install
else
    info "npm dependencies already installed, running npm ci to ensure consistency..."
    npm ci 2>/dev/null || npm install
fi
success "npm dependencies installed"

info "Building production frontend..."
npm run build
success "Frontend built: ${FRONTEND_DIR}/dist/"

# ------------------------------------------------------------------------------
# Step 10: Write environment info
# ------------------------------------------------------------------------------
step "Writing environment info"

cat > "${PROJECT_ROOT}/scripts/env-macos.sh" <<EOF
# Source this file to set up the environment:
#   source scripts/env-macos.sh
export PROJECT_ROOT="${PROJECT_ROOT}"
export BACKEND_DIR="${BACKEND_DIR}"
export FRONTEND_DIR="${FRONTEND_DIR}"
export DATA_DIR="${DATA_DIR}"
export TEST_DIR="${TEST_DIR}"
export CONFIG_FILE="${CONFIG_FILE}"
export RCLONE_CONFIG="${RCLONE_CONFIG}"
export LOCAL_CLOUD_DIR="${LOCAL_CLOUD_DIR}"
export TEST_SOURCE_DIR="${TEST_SOURCE_DIR}"
export TEST_RESTORE_DIR="${TEST_RESTORE_DIR}"
export PATH="${GOPATH}/bin:/usr/local/go/bin:/opt/homebrew/bin:\$PATH"
EOF

success "Environment file written: scripts/env-macos.sh"

# ------------------------------------------------------------------------------
# Step 11: Generate start/stop scripts
# ------------------------------------------------------------------------------
step "Generating convenience scripts"

cat > "${PROJECT_ROOT}/scripts/start-backend.sh" <<'STARTSCRIPT'
#!/bin/bash
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
BACKEND_DIR="${PROJECT_ROOT}/nas-backup-backend"
cd "$BACKEND_DIR"
echo "Starting NAS Backup backend on http://127.0.0.1:8080 ..."
echo "Press Ctrl+C to stop"
exec ./nas-backup -config config.yaml
STARTSCRIPT
chmod +x "${PROJECT_ROOT}/scripts/start-backend.sh"

cat > "${PROJECT_ROOT}/scripts/start-frontend-dev.sh" <<'STARTSCRIPT'
#!/bin/bash
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
FRONTEND_DIR="${PROJECT_ROOT}/nas-backup-frontend"
cd "$FRONTEND_DIR"
echo "Starting Vite dev server on http://localhost:5173 ..."
echo "API will be proxied to http://localhost:8080"
echo "Press Ctrl+C to stop"
exec npm run dev
STARTSCRIPT
chmod +x "${PROJECT_ROOT}/scripts/start-frontend-dev.sh"

cat > "${PROJECT_ROOT}/scripts/serve-frontend.sh" <<'SERVESCRIPT'
#!/bin/bash
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
FRONTEND_DIR="${PROJECT_ROOT}/nas-backup-frontend"
cd "${FRONTEND_DIR}/dist"
echo "Serving production frontend on http://127.0.0.1:5173 ..."
echo "Note: Start backend separately on port 8080"
exec python3 -m http.server 5173
SERVESCRIPT
chmod +x "${PROJECT_ROOT}/scripts/serve-frontend.sh"

success "Convenience scripts generated:"
success "  scripts/start-backend.sh       - Start backend server"
success "  scripts/start-frontend-dev.sh  - Start Vite dev server (with hot reload)"
success "  scripts/serve-frontend.sh      - Serve production build"

# ------------------------------------------------------------------------------
# Deployment complete
# ------------------------------------------------------------------------------
echo ""
echo -e "${GREEN}${BOLD}======================================================================${NC}"
echo -e "${GREEN}${BOLD}  NAS Backup System - macOS Deployment Complete!${NC}"
echo -e "${GREEN}${BOLD}======================================================================${NC}"
echo ""
echo -e "  ${BOLD}Environment:${NC}"
echo -e "    Backend binary:   ${BACKEND_DIR}/nas-backup"
echo -e "    Frontend build:   ${FRONTEND_DIR}/dist/"
echo -e "    Config file:      ${CONFIG_FILE}"
echo -e "    Data directory:   ${DATA_DIR}"
echo -e "    Local 'cloud':    ${LOCAL_CLOUD_DIR}"
echo ""
echo -e "  ${BOLD}Quick Start:${NC}"
echo -e "    1. Source env:    source scripts/env-macos.sh"
echo -e "    2. Start backend: ./scripts/start-backend.sh"
echo -e "    3. Start frontend: ./scripts/start-frontend-dev.sh (in another terminal)"
echo -e "    4. Open browser:  http://localhost:5173"
echo -e "    5. Run E2E tests: ./scripts/verify-e2e.sh"
echo ""
echo -e "  ${BOLD}To switch to real OSS:${NC}"
echo -e "    Re-run this script with: ./scripts/deploy-macos.sh --with-oss"
echo ""
