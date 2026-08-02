#!/bin/bash
# ==============================================================================
# NAS Backup System - Debian NAS (Green联/极空间/群晖等) Safe Deployment Script
# ==============================================================================
# This script safely deploys NAS Backup to a Debian-based NAS WITHOUT modifying
# system packages or upgrading the OS (which can brick customized NAS firmware).
#
# What it does:
#   1. Checks for existing tools (NEVER runs apt upgrade / apt full-upgrade)
#   2. Installs ONLY missing packages via apt with --no-upgrade flag (safe)
#   3. Installs Go, Node.js, rclone via OFFICIAL BINARY downloads (no apt)
#   4. Generates master encryption key
#   5. Builds backend (Go) and frontend (React/Vite)
#   6. Sets up systemd service
#   7. Optionally configures Nginx
#   8. Starts services and verifies deployment
#
# Usage:
#   chmod +x scripts/deploy-debian.sh
#   sudo ./scripts/deploy-debian.sh [OPTIONS]
#
# Options:
#   --skip-deps      Skip apt dependency checks entirely
#   --skip-frontend  Skip frontend build (backend-only deployment)
#   --install-dir DIR Override installation directory (default: auto-detect)
#   --no-nginx       Skip Nginx configuration
#   --help           Show this help message
#
# SAFETY NOTICE: This script NEVER runs apt upgrade / apt dist-upgrade.
# It will not modify your NAS system firmware packages.
# ==============================================================================

set -euo pipefail

# Ensure standard binary paths are in PATH. Also add common user install locations
# (important when running via sudo, which resets PATH and hides user-installed tools).
# NOTE: We deliberately do NOT source ~/.profile or ~/.bashrc — those files often
# contain nvm/conda/interactive initializers that hang in non-interactive shells.
# Instead, we scan known installation directories directly.
COMMON_USER_PATHS=(
    "/usr/local/sbin" "/usr/local/bin"
    "/usr/sbin" "/usr/bin" "/sbin" "/bin"
    "$HOME/go/bin" "$HOME/.local/bin" "$HOME/bin"
    "/usr/local/go/bin"
)
# If running via sudo, also add the real user's common paths
if [[ -n "${SUDO_USER:-}" ]]; then
    REAL_USER_HOME="$(eval echo "~${SUDO_USER}")"
    COMMON_USER_PATHS+=(
        "${REAL_USER_HOME}/go/bin"
        "${REAL_USER_HOME}/.local/bin"
        "${REAL_USER_HOME}/bin"
        "${REAL_USER_HOME}/.gvm/gos/current/bin"
    )
    # Scan nvm directory for installed Node.js versions (non-blocking)
    if [[ -d "${REAL_USER_HOME}/.nvm/versions/node" ]]; then
        NVM_LATEST_NODE="$(ls -t "${REAL_USER_HOME}/.nvm/versions/node/" 2>/dev/null | head -1)"
        if [[ -n "$NVM_LATEST_NODE" ]]; then
            COMMON_USER_PATHS+=("${REAL_USER_HOME}/.nvm/versions/node/${NVM_LATEST_NODE}/bin")
        fi
    fi
fi
# Add unique paths to PATH
for p in "${COMMON_USER_PATHS[@]}"; do
    if [[ -d "$p" ]] && [[ ":$PATH:" != *":$p:"* ]]; then
        PATH="$p:$PATH"
    fi
done
export PATH

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
# Parse arguments
# ------------------------------------------------------------------------------
SKIP_DEPS=false
SKIP_FRONTEND=false
INSTALL_DIR=""
NO_NGINX=false

while [[ $# -gt 0 ]]; do
    case "$1" in
        --skip-deps)     SKIP_DEPS=true; shift ;;
        --skip-frontend) SKIP_FRONTEND=true; shift ;;
        --install-dir)   INSTALL_DIR="$2"; shift 2 ;;
        --no-nginx)      NO_NGINX=true; shift ;;
        -h|--help)
            awk 'NR>=2 && NR<=32 {sub(/^# ?/,""); print}' "$0"
            exit 0 ;;
        *)
            echo "Unknown option: $1"
            exit 1 ;;
    esac
done

# ------------------------------------------------------------------------------
# Detect installation directory
# ------------------------------------------------------------------------------
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DETECTED_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

if [[ -z "$INSTALL_DIR" ]]; then
    # Auto-detect: if script is run from project root scripts/, use that
    if [[ -d "${DETECTED_ROOT}/nas-backup-backend" && -d "${DETECTED_ROOT}/nas-backup-frontend" ]]; then
        INSTALL_DIR="$DETECTED_ROOT"
    else
        INSTALL_DIR="/opt/nas-backup"
    fi
fi

BACKEND_DIR="${INSTALL_DIR}/nas-backup-backend"
FRONTEND_DIR="${INSTALL_DIR}/nas-backup-frontend"
DATA_DIR="${BACKEND_DIR}/data"
CONFIG_FILE="${BACKEND_DIR}/config.yaml"

# ------------------------------------------------------------------------------
# Pre-flight checks
# ------------------------------------------------------------------------------
step "Pre-flight checks"

IS_ROOT=false
if [[ $EUID -eq 0 ]]; then
    IS_ROOT=true
    if [[ -n "${SUDO_USER:-}" ]]; then
        info "Running as root (via sudo from user: ${SUDO_USER})"
    else
        info "Running as root"
    fi
else
    warn "Not running as root - will compile and build only, skipping system service installation."
    warn "Run with sudo to install systemd service and configure Nginx."
fi

# Detect architecture
ARCH="$(dpkg --print-architecture 2>/dev/null || uname -m)"
case "$ARCH" in
    amd64|x86_64)  GOARCH="amd64"; RCLONE_ARCH="amd64" ;;
    arm64|aarch64) GOARCH="arm64"; RCLONE_ARCH="arm64" ;;
    armhf|armv7l)  GOARCH="arm";   RCLONE_ARCH="arm-v7" ;;
    *)             fail "Unsupported architecture: $ARCH" ;;
esac
success "Architecture: $ARCH (Go: $GOARCH, rclone: $RCLONE_ARCH)"

# Check if this is Debian
if [[ -f /etc/os-release ]]; then
    # shellcheck source=/dev/null
    . /etc/os-release
    if [[ "$ID" != "debian" ]]; then
        warn "This script is designed for Debian 12, detected: $PRETTY_NAME"
    else
        success "OS: $PRETTY_NAME"
    fi
else
    warn "Cannot detect OS version"
fi

# Check project directories exist
if [[ ! -d "$BACKEND_DIR" ]]; then
    fail "Backend directory not found: ${BACKEND_DIR}\nPlease ensure the project is cloned correctly."
fi
success "Backend directory: ${BACKEND_DIR}"

if [[ "$SKIP_FRONTEND" == "false" && ! -d "$FRONTEND_DIR" ]]; then
    warn "Frontend directory not found: ${FRONTEND_DIR}, will skip frontend build"
    SKIP_FRONTEND=true
fi
if [[ "$SKIP_FRONTEND" == "false" ]]; then
    success "Frontend directory: ${FRONTEND_DIR}"
fi

# Check config.yaml exists
if [[ ! -f "$CONFIG_FILE" ]]; then
    warn "config.yaml not found at ${CONFIG_FILE}"
    warn "Will use default configuration. Please edit config.yaml before starting service."
    CONFIG_EXISTS=false
else
    success "Config file found: ${CONFIG_FILE}"
    CONFIG_EXISTS=true
fi

# ------------------------------------------------------------------------------
# Step 1: Check system dependencies (SAFE MODE - NO system upgrade)
# ------------------------------------------------------------------------------
if [[ "$SKIP_DEPS" == "false" ]]; then
    step "Checking system dependencies (safe mode - no OS upgrade)"

    if [[ "$IS_ROOT" == "true" ]]; then
    # Refresh package lists only (does NOT upgrade anything)
    info "Refreshing package lists..."
    apt update -qq 2>/dev/null || warn "apt update had issues, continuing anyway"

    # Check for critical tools. For customized NAS (Green联/极空间/etc), we ONLY
    # try to install TRULY missing packages with --no-upgrade to avoid breaking
    # the vendor-modified system.
    #
    # IMPORTANT: We NEVER run apt upgrade / apt dist-upgrade / apt full-upgrade.

    # Packages truly needed at runtime for building/running:
    # - curl/wget: for downloading binaries
    # - ca-certificates: for HTTPS
    # - gcc/libc6-dev: needed for CGO (go-sqlite3)
    # - make: sometimes needed by npm builds
    # - sqlite3: for manual DB inspection (optional)
    # - openssl: for master key generation
    CRITICAL_PKGS=(ca-certificates openssl unzip)
    NICE_TO_HAVE=(curl wget gcc libc6-dev make file)
    OPTIONAL_PKGS=(sqlite3 git)

    # First check which are missing
    MISSING_CRITICAL=()
    MISSING_NICE=()
    MISSING_OPTIONAL=()

    for pkg in "${CRITICAL_PKGS[@]}"; do
        dpkg -s "$pkg" &>/dev/null || MISSING_CRITICAL+=("$pkg")
    done
    for pkg in "${NICE_TO_HAVE[@]}"; do
        dpkg -s "$pkg" &>/dev/null || MISSING_NICE+=("$pkg")
    done
    for pkg in "${OPTIONAL_PKGS[@]}"; do
        dpkg -s "$pkg" &>/dev/null || MISSING_OPTIONAL+=("$pkg")
    done

    # Install critical packages if missing (using --no-upgrade to prevent system breakage)
    if [[ ${#MISSING_CRITICAL[@]} -gt 0 ]]; then
        info "Installing missing critical packages (--no-upgrade for safety): ${MISSING_CRITICAL[*]}"
        DEBIAN_FRONTEND=noninteractive apt install -y -qq --no-upgrade -o Dpkg::Options::="--force-confdef" -o Dpkg::Options::="--force-confold" "${MISSING_CRITICAL[@]}" 2>&1 || {
            warn "Some critical packages failed to install, will try to work around..."
        }
    fi

    # Try nice-to-have packages but don't fail if they can't be installed
    if [[ ${#MISSING_NICE[@]} -gt 0 ]]; then
        info "Checking for missing tools: ${MISSING_NICE[*]}"
        DEBIAN_FRONTEND=noninteractive apt install -y -qq --no-upgrade -o Dpkg::Options::="--force-confdef" -o Dpkg::Options::="--force-confold" "${MISSING_NICE[@]}" 2>&1 || {
            warn "Some tools could not be installed via apt - will try binary fallback for core tools"
        }
    fi

    # Optional packages - don't even try if there are dependency issues
    if [[ ${#MISSING_OPTIONAL[@]} -gt 0 ]]; then
        info "Optional packages missing (skipping to avoid dependency issues): ${MISSING_OPTIONAL[*]}"
    fi

    # Verify what binaries we actually have available
    for bin in curl wget openssl; do
        if command -v "$bin" &>/dev/null; then
            success "$bin: available"
        else
            warn "$bin not found - some features may not work"
        fi
    done

    # Check for C compiler (needed for CGO=go-sqlite3)
    if command -v gcc &>/dev/null; then
        success "gcc: $(gcc --version 2>/dev/null | head -1)"
    else
        warn "gcc not found! CGO is required for go-sqlite3."
        if [[ "$IS_ROOT" == "true" ]]; then
            warn "Attempting to install gcc safely..."
            DEBIAN_FRONTEND=noninteractive apt install -y -qq --no-upgrade gcc 2>&1 || true
        fi
        if ! command -v gcc &>/dev/null; then
            fail "gcc is required but cannot be installed. Please install build-essential/gcc manually via your NAS package manager."
        fi
    fi

    success "System dependency check complete (no system upgrades performed)"
    else
        # Not root - skip apt, just verify essential build tools exist
        info "Not running as root - skipping apt package installation."
        info "Checking for essential tools..."
        for bin in curl wget openssl gcc; do
            if command -v "$bin" &>/dev/null; then
                success "$bin: available"
            else
                warn "$bin not found - build may fail"
            fi
        done
    fi
else
    info "Skipping dependency checks (--skip-deps)"
fi

# ------------------------------------------------------------------------------
# Step 2: Install/verify Go (binary download ONLY - no apt)
# ------------------------------------------------------------------------------
step "Checking Go installation"

REQUIRED_GO_MAJOR=1
REQUIRED_GO_MINOR=25
GO_INSTALLED=false
GO_BIN=""

# Check PATH first (this will find user-installed Go from SUDO_USER's environment)
if command -v go &>/dev/null; then
    GO_BIN="$(which go)"
    GO_VERSION="$(go version | awk '{print $3}' | sed 's/go//')"
elif [[ -x /usr/local/go/bin/go ]]; then
    GO_BIN="/usr/local/go/bin/go"
    GO_VERSION="$($GO_BIN version | awk '{print $3}' | sed 's/go//')"
else
    GO_VERSION=""
fi

if [[ -n "$GO_VERSION" ]]; then
    GO_MAJOR="$(echo "$GO_VERSION" | cut -d. -f1)"
    GO_MINOR="$(echo "$GO_VERSION" | cut -d. -f2)"
    if (( GO_MAJOR > REQUIRED_GO_MAJOR )) || { (( GO_MAJOR == REQUIRED_GO_MAJOR )) && (( GO_MINOR >= REQUIRED_GO_MINOR )); }; then
        success "Go ${GO_VERSION} found at ${GO_BIN} (>= ${REQUIRED_GO_MAJOR}.${REQUIRED_GO_MINOR} required)"
        GO_INSTALLED=true
    else
        warn "Go ${GO_VERSION} at ${GO_BIN} is too old (need >= ${REQUIRED_GO_MAJOR}.${REQUIRED_GO_MINOR})"
    fi
fi

if [[ "$GO_INSTALLED" == "false" ]]; then
    GO_VERSION="1.26.2"
    if [[ "$IS_ROOT" == "true" ]]; then
        GO_INSTALL_DIR="/usr/local/go"
        info "Downloading Go ${GO_VERSION} for linux/${GOARCH} to ${GO_INSTALL_DIR} (system-wide install)..."
        rm -rf /usr/local/go
        curl -fSL --connect-timeout 60 "https://go.dev/dl/go${GO_VERSION}.linux-${GOARCH}.tar.gz" -o /tmp/go.tar.gz 2>/dev/null || \
        wget -q --timeout=60 "https://go.dev/dl/go${GO_VERSION}.linux-${GOARCH}.tar.gz" -O /tmp/go.tar.gz
        tar -C /usr/local -xzf /tmp/go.tar.gz
        rm -f /tmp/go.tar.gz /usr/local/bin/go
        if ! grep -q '/usr/local/go/bin' /etc/profile.d/go.sh 2>/dev/null; then
            echo 'export PATH=$PATH:/usr/local/go/bin' > /etc/profile.d/go.sh
            chmod +x /etc/profile.d/go.sh
        fi
        export PATH="/usr/local/go/bin:$PATH"
        success "Go ${GO_VERSION} installed to ${GO_INSTALL_DIR}"
    else
        GO_INSTALL_DIR="$HOME/.local/go"
        info "Downloading Go ${GO_VERSION} for linux/${GOARCH} to ${GO_INSTALL_DIR} (user-local install)..."
        mkdir -p "$HOME/.local"
        rm -rf "$HOME/.local/go"
        curl -fSL --connect-timeout 60 "https://go.dev/dl/go${GO_VERSION}.linux-${GOARCH}.tar.gz" -o /tmp/go.tar.gz 2>/dev/null || \
        wget -q --timeout=60 "https://go.dev/dl/go${GO_VERSION}.linux-${GOARCH}.tar.gz" -O /tmp/go.tar.gz
        tar -C "$HOME/.local" -xzf /tmp/go.tar.gz
        rm -f /tmp/go.tar.gz
        export PATH="$HOME/.local/go/bin:$PATH"
        # Add to user's shell config
        for rc in "$HOME/.bashrc" "$HOME/.profile"; do
            if [[ -f "$rc" ]] && ! grep -q '.local/go/bin' "$rc" 2>/dev/null; then
                echo 'export PATH="$HOME/.local/go/bin:$PATH"' >> "$rc"
            fi
        done
        success "Go ${GO_VERSION} installed to ${GO_INSTALL_DIR}"
    fi
fi

# Ensure Go is in PATH for subsequent commands
if [[ -x /usr/local/go/bin/go ]]; then
    export PATH="/usr/local/go/bin:$PATH"
elif [[ -x "$HOME/.local/go/bin/go" ]]; then
    export PATH="$HOME/.local/go/bin:$PATH"
fi
go version

# ------------------------------------------------------------------------------
# Step 3: Install/verify Node.js (binary download ONLY - no apt, no NodeSource)
# ------------------------------------------------------------------------------
if [[ "$SKIP_FRONTEND" == "false" ]]; then
    step "Checking Node.js installation"

    NODE_INSTALLED=false
    if command -v node &>/dev/null; then
        NODE_MAJOR="$(node -v | sed 's/v//' | cut -d. -f1)"
        if (( NODE_MAJOR >= 20 )); then
            NODE_BIN="$(which node)"
            success "Node.js $(node -v) found at ${NODE_BIN} (>= 20 required)"
            NODE_INSTALLED=true
        else
            warn "Node.js $(node -v) is too old (need >= 20)"
        fi
    fi

    if [[ "$NODE_INSTALLED" == "false" ]]; then
        NODE_VERSION="v20.18.0"
        NODE_ARCH="$GOARCH"
        [[ "$NODE_ARCH" == "amd64" ]] && NODE_ARCH="x64"
        [[ "$NODE_ARCH" == "arm64" ]] && NODE_ARCH="arm64"
        [[ "$NODE_ARCH" == "arm" ]]   && NODE_ARCH="armv7l"

        if [[ "$IS_ROOT" == "true" ]]; then
            NODE_BASE="/usr/local/lib/nodejs"
            NODE_BIN_DIR="/usr/local/bin"
            info "Downloading Node.js ${NODE_VERSION} for linux-${NODE_ARCH} to ${NODE_BASE} (system-wide)..."
        else
            NODE_BASE="$HOME/.local/lib/nodejs"
            NODE_BIN_DIR="$HOME/.local/bin"
            info "Downloading Node.js ${NODE_VERSION} for linux-${NODE_ARCH} to ${NODE_BASE} (user-local)..."
        fi

        NODE_URL="https://nodejs.org/dist/${NODE_VERSION}/node-${NODE_VERSION}-linux-${NODE_ARCH}.tar.xz"
        mkdir -p "$NODE_BASE" "$NODE_BIN_DIR"

        curl -fSL --connect-timeout 60 "$NODE_URL" -o /tmp/node.tar.xz 2>/dev/null || \
        wget -q --timeout=60 "$NODE_URL" -O /tmp/node.tar.xz

        tar -xJf /tmp/node.tar.xz -C "$NODE_BASE"
        rm -f /tmp/node.tar.xz

        ln -sf "$NODE_BASE/node-${NODE_VERSION}-linux-${NODE_ARCH}/bin/node" "$NODE_BIN_DIR/node"
        ln -sf "$NODE_BASE/node-${NODE_VERSION}-linux-${NODE_ARCH}/bin/npm" "$NODE_BIN_DIR/npm"
        ln -sf "$NODE_BASE/node-${NODE_VERSION}-linux-${NODE_ARCH}/bin/npx" "$NODE_BIN_DIR/npx"

        export PATH="$NODE_BIN_DIR:$PATH"
        success "Node.js $(node -v) installed"
    fi
    node --version
    npm --version
fi

# ------------------------------------------------------------------------------
# Step 4: Install/verify rclone (binary download ONLY - no install.sh, no apt)
# ------------------------------------------------------------------------------
step "Checking rclone installation"

RCLONE_BIN=""
if command -v rclone &>/dev/null; then
    RCLONE_BIN="$(which rclone)"
    success "rclone found at: ${RCLONE_BIN} ($(rclone version 2>/dev/null | head -1))"
else
    RCLONE_VERSION="v1.75.0"
    if [[ "$IS_ROOT" == "true" ]]; then
        RCLONE_BIN="/usr/local/bin/rclone"
        info "rclone not found, downloading official binary ${RCLONE_VERSION} for linux-${RCLONE_ARCH} to /usr/local/bin..."
    else
        mkdir -p "$HOME/.local/bin"
        RCLONE_BIN="$HOME/.local/bin/rclone"
        info "rclone not found, downloading official binary ${RCLONE_VERSION} for linux-${RCLONE_ARCH} to ~/.local/bin..."
    fi

    case "$RCLONE_ARCH" in
        amd64) RCLONE_ZIP_ARCH="amd64" ;;
        arm64) RCLONE_ZIP_ARCH="arm64" ;;
        arm-v7) RCLONE_ZIP_ARCH="arm-v7" ;;
        *)     RCLONE_ZIP_ARCH="amd64" ;;
    esac

    RCLONE_URL="https://downloads.rclone.org/${RCLONE_VERSION}/rclone-${RCLONE_VERSION}-linux-${RCLONE_ZIP_ARCH}.zip"

    curl -fSL --connect-timeout 60 "$RCLONE_URL" -o /tmp/rclone.zip 2>/dev/null || \
    wget -q --timeout=60 "$RCLONE_URL" -O /tmp/rclone.zip

    mkdir -p /tmp/rclone-extract
    unzip -q -o /tmp/rclone.zip -d /tmp/rclone-extract
    cp "/tmp/rclone-extract/rclone-${RCLONE_VERSION}-linux-${RCLONE_ZIP_ARCH}/rclone" "$RCLONE_BIN"
    chmod +x "$RCLONE_BIN"
    rm -rf /tmp/rclone-extract /tmp/rclone.zip
    success "rclone installed to ${RCLONE_BIN} ($(rclone version | head -1))"
fi

# Ensure rclone binary exists in backend/bin/ for the service
mkdir -p "${BACKEND_DIR}/bin"
if [[ ! -f "${BACKEND_DIR}/bin/rclone" ]] || [[ "${RCLONE_BIN}" -nt "${BACKEND_DIR}/bin/rclone" ]]; then
    cp "$RCLONE_BIN" "${BACKEND_DIR}/bin/rclone"
    chmod +x "${BACKEND_DIR}/bin/rclone"
    success "Copied rclone to ${BACKEND_DIR}/bin/rclone"
fi

# ------------------------------------------------------------------------------
# Step 5: Setup data directory and master key
# ------------------------------------------------------------------------------
step "Setting up data directory"

mkdir -p "${DATA_DIR}/logs"
chmod 700 "${DATA_DIR}"
success "Data directory: ${DATA_DIR}"

# Generate master key if not exists
MASTER_KEY="${DATA_DIR}/master.key"
if [[ ! -f "$MASTER_KEY" ]]; then
    info "Generating master encryption key..."
    openssl rand -hex 32 > "$MASTER_KEY"
    chmod 600 "$MASTER_KEY"
    success "Master key generated: ${MASTER_KEY}"
else
    success "Master key already exists: ${MASTER_KEY}"
fi

# ------------------------------------------------------------------------------
# Step 6: Fix config.yaml paths if needed
# ------------------------------------------------------------------------------
step "Checking and fixing configuration"

CONFIG_BACKED_UP=false
if [[ "$CONFIG_EXISTS" == "true" ]]; then
    # Backup original config before making changes
    CONFIG_BAK="${CONFIG_FILE}.bak.$(date +%Y%m%d%H%M%S)"
    cp "$CONFIG_FILE" "$CONFIG_BAK"
    CONFIG_BACKED_UP=true
    info "Backed up original config to: ${CONFIG_BAK}"

    # Fix rclone binary_path: if it's a non-existent absolute path (e.g., from macOS),
    # update it to use the local bin/rclone (which we've already placed)
    RCLONE_BIN_CFG="$(awk -F'binary_path:' '/binary_path:/{print $2; exit}' "$CONFIG_FILE" | awk -F'"' '{print $2}' | sed 's/^ *//;s/ *$//')"
    RCLONE_FIXED=false
    if [[ -n "$RCLONE_BIN_CFG" ]]; then
        if [[ "$RCLONE_BIN_CFG" == /* ]] && [[ ! -f "$RCLONE_BIN_CFG" ]]; then
            info "rclone binary_path points to non-existent path: ${RCLONE_BIN_CFG}"
            NEW_RCLONE_PATH="./bin/rclone"
            RCLONE_PATH_LINE=$(grep -n 'binary_path:' "$CONFIG_FILE" | head -1 | cut -d: -f1)
            if [[ -n "$RCLONE_PATH_LINE" ]]; then
                sed -i "${RCLONE_PATH_LINE}s|.*|  binary_path: \"${NEW_RCLONE_PATH}\"|" "$CONFIG_FILE"
                success "Updated rclone binary_path to: ${NEW_RCLONE_PATH} (line ${RCLONE_PATH_LINE})"
            fi
            RCLONE_FIXED=true
        elif [[ "$RCLONE_BIN_CFG" == "./bin/rclone" ]] || [[ "$RCLONE_BIN_CFG" == "rclone" ]]; then
            success "rclone binary_path is valid: ${RCLONE_BIN_CFG}"
        else
            info "rclone binary_path: ${RCLONE_BIN_CFG}"
        fi
    fi

    # Ensure key_file_path is correct (use relative path from backend dir)
    KEY_PATH_CFG="$(awk -F'key_file_path:' '/key_file_path:/{print $2; exit}' "$CONFIG_FILE" | awk -F'"' '{print $2}' | sed 's/^ *//;s/ *$//')"
    if [[ -n "$KEY_PATH_CFG" ]]; then
        if [[ "$KEY_PATH_CFG" == /* ]] && [[ ! -f "$KEY_PATH_CFG" ]]; then
            info "key_file_path points to non-existent path: ${KEY_PATH_CFG}"
            KEY_PATH_LINE=$(grep -n 'key_file_path:' "$CONFIG_FILE" | head -1 | cut -d: -f1)
            if [[ -n "$KEY_PATH_LINE" ]]; then
                sed -i "${KEY_PATH_LINE}s|.*|  key_file_path: \"./data/master.key\"|" "$CONFIG_FILE"
                success "Updated key_file_path to: ./data/master.key (line ${KEY_PATH_LINE})"
            fi
        fi
    fi

    # Fix database path - ensure it's valid relative or absolute
    # Match only standalone "path:" (not key_file_path, file_path, etc.)
    DB_PATH_CFG="$(awk '/^[[:space:]]+path:/{print; exit}' "$CONFIG_FILE" | awk -F'"' '{print $2}' | sed 's/^ *//;s/ *$//')"
    if [[ -n "$DB_PATH_CFG" ]]; then
        if [[ "$DB_PATH_CFG" == /* ]] && [[ ! -d "$(dirname "$DB_PATH_CFG")" ]]; then
            info "database path directory doesn't exist: ${DB_PATH_CFG}"
            DB_PATH_LINE=$(grep -nE '^[[:space:]]+path:' "$CONFIG_FILE" | head -1 | cut -d: -f1)
            if [[ -n "$DB_PATH_LINE" ]]; then
                sed -i "${DB_PATH_LINE}s|.*|  path: \"./data/nas-backup.db\"|" "$CONFIG_FILE"
                success "Updated database path to: ./data/nas-backup.db (line ${DB_PATH_LINE})"
            else
                warn "Could not find database path line in config, skipping"
            fi
        fi
    fi

    # Fix log file path (exclude key_file_path lines to avoid corrupting them)
    LOG_PATH_CFG="$(awk '/^[[:space:]]+file_path:/{print; exit}' "$CONFIG_FILE" | awk -F'"' '{print $2}' | sed 's/^ *//;s/ *$//')"
    if [[ -n "$LOG_PATH_CFG" ]]; then
        if [[ "$LOG_PATH_CFG" == /* ]] && [[ ! -d "$(dirname "$LOG_PATH_CFG")" ]]; then
            info "log file_path directory doesn't exist: ${LOG_PATH_CFG}"
            LOG_PATH_LINE=$(grep -nE '^[[:space:]]+file_path:' "$CONFIG_FILE" | grep -v key_file | head -1 | cut -d: -f1)
            if [[ -n "$LOG_PATH_LINE" ]]; then
                sed -i "${LOG_PATH_LINE}s|.*|  file_path: \"./data/logs/nas-backup.log\"|" "$CONFIG_FILE"
                success "Updated log file_path to: ./data/logs/nas-backup.log (line ${LOG_PATH_LINE})"
            else
                warn "Could not find log file_path line in config, skipping"
            fi
        fi
    fi

    # Show server port (do NOT auto-change; backend stays on 8080 as deployed)
    SERVER_PORT_CFG="$(awk -F'port:' '/^[[:space:]]+port:/{print $2; exit}' "$CONFIG_FILE" | sed 's/[^0-9]*//')"
    if [[ -n "$SERVER_PORT_CFG" ]]; then
        success "Server port: ${SERVER_PORT_CFG}"
    fi

    if [[ "$RCLONE_FIXED" == "true" ]] || [[ "$CONFIG_BACKED_UP" == "true" ]]; then
        success "Configuration validation complete"
    fi
else
    warn "config.yaml not found. The backend will start with defaults but you MUST configure it!"
fi

# ------------------------------------------------------------------------------
# Step 7: Build Go backend
# ------------------------------------------------------------------------------
step "Building Go backend"

cd "$BACKEND_DIR"

# Configure Go module proxy for China (goproxy.cn - 七牛云)
# Fallback chain: direct for private repos
export GOPROXY="https://goproxy.cn,https://mirrors.aliyun.com/goproxy/,https://goproxy.io,direct"
export GOSUMDB="sum.golang.google.cn"
export GO111MODULE=on
info "Go proxy configured: ${GOPROXY}"

info "Downloading Go modules (this may take a moment)..."
go mod download
success "Go modules downloaded"

info "Compiling nas-backup server (linux/${GOARCH})..."
CGO_ENABLED=1 GOOS=linux GOARCH="$GOARCH" go build -buildvcs=false -ldflags="-s -w" -o nas-backup ./cmd/nas-backup/
success "Backend binary built: ${BACKEND_DIR}/nas-backup"

info "Compiling restore-cli tool..."
CGO_ENABLED=1 GOOS=linux GOARCH="$GOARCH" go build -buildvcs=false -ldflags="-s -w" -o restore-cli ./cmd/restore-cli/
success "restore-cli built: ${BACKEND_DIR}/restore-cli"

# Quick smoke test
info "Verifying backend binary..."
if ./nas-backup --help &>/dev/null; then
    success "Backend binary runs correctly"
else
    fail "Backend binary verification failed"
fi

# ------------------------------------------------------------------------------
# Step 8: Build frontend
# ------------------------------------------------------------------------------
if [[ "$SKIP_FRONTEND" == "false" ]]; then
    step "Building React frontend"

    cd "$FRONTEND_DIR"

    # Configure npm mirror for China (npmmirror - 淘宝镜像)
    export npm_config_registry="https://registry.npmmirror.com"
    info "npm registry configured: ${npm_config_registry}"

    if [[ ! -d node_modules ]]; then
        info "Installing npm dependencies..."
        npm ci --production=false --registry=https://registry.npmmirror.com || \
        npm install --production=false --registry=https://registry.npmmirror.com
    else
        info "npm dependencies already installed, running npm ci to ensure consistency..."
        npm ci --production=false --registry=https://registry.npmmirror.com || {
            warn "npm ci failed, continuing with existing node_modules"
        }
    fi
    success "npm dependencies ready"

    info "Building production frontend..."
    # Use the LOCALLY installed vite (from node_modules), NOT npx which downloads
    # the latest version (vite@8 requires node>=20.19, we have 20.18).
    # Also skip tsc type-checking (npm run build = "tsc -b && vite build") as it
    # is not needed for the production bundle and can fail on NAS environments.
    if [[ -x ./node_modules/.bin/vite ]]; then
        info "Using locally installed vite ($(./node_modules/.bin/vite --version))"
        ./node_modules/.bin/vite build
    else
        warn "Local vite not found, falling back to npm run build..."
        npm run build || {
            warn "npm run build failed. Trying vite directly..."
            ./node_modules/.bin/vite build || fail "Frontend build failed"
        }
    fi
    success "Frontend build completed"

    if [[ ! -d "${FRONTEND_DIR}/dist" ]]; then
        fail "Frontend build failed - dist/ directory not found"
    fi
    success "Frontend built: ${FRONTEND_DIR}/dist/"
fi

# ------------------------------------------------------------------------------
# Step 9: Setup systemd service (root only)
# ------------------------------------------------------------------------------
if [[ "$IS_ROOT" == "true" ]]; then
    step "Configuring systemd service"

    SERVICE_FILE="/etc/systemd/system/nas-backup.service"

    cat > "$SERVICE_FILE" <<EOF
[Unit]
Description=NAS Backup Service
After=network.target

[Service]
Type=simple
WorkingDirectory=${BACKEND_DIR}
ExecStart=${BACKEND_DIR}/nas-backup -config ${CONFIG_FILE}
Restart=always
RestartSec=5
User=root

# Resource limits
LimitNOFILE=65536
MemoryMax=512M

# Security hardening
NoNewPrivileges=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
EOF

    success "Systemd service file created: ${SERVICE_FILE}"

    systemctl daemon-reload
    systemctl enable nas-backup
    success "nas-backup service enabled on boot"
else
    step "Skipping systemd service setup (need root)"
    info "To start manually, run:"
    info "  cd ${BACKEND_DIR} && ./nas-backup -config ${CONFIG_FILE}"
fi

# ------------------------------------------------------------------------------
# Step 10: Configure Nginx (root only)
# ------------------------------------------------------------------------------
if [[ "$NO_NGINX" == "false" && "$IS_ROOT" == "true" ]]; then
    step "Configuring Nginx reverse proxy"

    if ! command -v nginx &>/dev/null; then
        warn "nginx not found, skipping Nginx configuration."
        warn "You can manually configure a reverse proxy or access backend directly at http://<server-ip>:8080"
        NO_NGINX=true
    else
        NGINX_CONF_DIR="/etc/nginx"

        # Detect which include directory nginx.conf actually reads.
        # Different NAS distros use different conventions:
        #   - Greenlon/UGREEN (DXP series): /etc/nginx/server.d/*.conf  (include /etc/nginx/server.d/*.conf)
        #   - Standard Debian:               /etc/nginx/sites-enabled/* (include /etc/nginx/sites-enabled/*)
        #   - Some distros:                  /etc/nginx/conf.d/*.conf   (include /etc/nginx/conf.d/*.conf)
        # We pick whichever directory is already included by nginx.conf so our
        # server block is actually loaded (writing to sites-enabled on UGREEN
        # does nothing because nginx.conf never includes it).
        NGINX_INCLUDE_DIR=""
        if grep -qE 'include\s+/etc/nginx/server\.d/\*\.conf' "${NGINX_CONF_DIR}/nginx.conf" 2>/dev/null \
           && [[ -d "${NGINX_CONF_DIR}/server.d" ]]; then
            NGINX_INCLUDE_DIR="${NGINX_CONF_DIR}/server.d"
            info "Detected UGREEN-style nginx include dir: ${NGINX_INCLUDE_DIR}"
        elif grep -qE 'include\s+/etc/nginx/sites-enabled/\*' "${NGINX_CONF_DIR}/nginx.conf" 2>/dev/null \
             && [[ -d "${NGINX_CONF_DIR}/sites-enabled" ]]; then
            NGINX_INCLUDE_DIR="${NGINX_CONF_DIR}/sites-enabled"
            info "Detected Debian-style nginx include dir: ${NGINX_INCLUDE_DIR}"
        elif grep -qE 'include\s+/etc/nginx/conf\.d/\*\.conf' "${NGINX_CONF_DIR}/nginx.conf" 2>/dev/null; then
            mkdir -p "${NGINX_CONF_DIR}/conf.d"
            NGINX_INCLUDE_DIR="${NGINX_CONF_DIR}/conf.d"
            info "Detected conf.d-style nginx include dir: ${NGINX_INCLUDE_DIR}"
        else
            # Fallback: try standard Debian layout, create dirs if missing
            mkdir -p "${NGINX_CONF_DIR}/sites-available" "${NGINX_CONF_DIR}/sites-enabled"
            # If nginx.conf doesn't include sites-enabled, also create conf.d as backup target
            NGINX_INCLUDE_DIR="${NGINX_CONF_DIR}/conf.d"
            mkdir -p "${NGINX_INCLUDE_DIR}"
            warn "Could not auto-detect nginx include directory, using: ${NGINX_INCLUDE_DIR}"
            warn "If nginx does not pick up the config, add this line to ${NGINX_CONF_DIR}/nginx.conf (inside http {} block):"
            warn "    include /etc/nginx/conf.d/*.conf;"
        fi

        # For sites-available/sites-enabled layout we still write the source file
        # to sites-available and symlink, but the INCLUDE dir above is what nginx
        # actually reads. For server.d/conf.d we write directly.
        if [[ "$NGINX_INCLUDE_DIR" == "${NGINX_CONF_DIR}/sites-enabled" ]]; then
            NGINX_SRC="${NGINX_CONF_DIR}/sites-available/nas-backup"
            NGINX_LINK="${NGINX_CONF_DIR}/sites-enabled/nas-backup"
        else
            NGINX_SRC="${NGINX_INCLUDE_DIR}/nas-backup.conf"
            NGINX_LINK=""
        fi

        # Clean up any stale config from previous layout (e.g. an old sites-available
        # file left over from before we detected server.d), to avoid confusion.
        [[ -f "${NGINX_CONF_DIR}/sites-available/nas-backup" ]] && rm -f "${NGINX_CONF_DIR}/sites-available/nas-backup" 2>/dev/null || true
        [[ -L "${NGINX_CONF_DIR}/sites-enabled/nas-backup" ]] && rm -f "${NGINX_CONF_DIR}/sites-enabled/nas-backup" 2>/dev/null || true
        [[ -f "${NGINX_CONF_DIR}/conf.d/nas-backup.conf" ]] && rm -f "${NGINX_CONF_DIR}/conf.d/nas-backup.conf" 2>/dev/null || true

        cat > "$NGINX_SRC" <<'NGINXEOF'
server {
    listen 9000;
    listen [::]:9000;
    server_name _;

    # Frontend static files
    location / {
NGINXEOF

        # Decide whether to serve static frontend or a placeholder.
        # IMPORTANT: --skip-frontend only skips the BUILD step; if a previously
        # built dist/ exists we still serve it. The placeholder is used ONLY when
        # there is genuinely no frontend bundle to serve.
        if [[ -d "${FRONTEND_DIR}/dist" && -f "${FRONTEND_DIR}/dist/index.html" ]]; then
            cat >> "$NGINX_SRC" <<EOF
        root ${FRONTEND_DIR}/dist;
        index index.html;
        try_files \$uri \$uri/ /index.html;
EOF
        elif [[ "$SKIP_FRONTEND" == "false" ]]; then
            # Build was attempted but dist/ is missing — treat as build failure.
            warn "Frontend dist/ not found at ${FRONTEND_DIR}/dist (build may have failed); serving placeholder."
            cat >> "$NGINX_SRC" <<'EOF'
        return 200 'NAS Backup API server running. Frontend build missing — API is available at /api/';
        default_type text/plain;
EOF
        else
            cat >> "$NGINX_SRC" <<'EOF'
        return 200 'NAS Backup API server running. API is available at /api/';
        default_type text/plain;
EOF
        fi

        cat >> "$NGINX_SRC" <<'NGINXEOF'
    }

    # API reverse proxy to backend
    location /api/ {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;

        # Extended timeouts for long backup/restore operations
        proxy_connect_timeout 60s;
        proxy_send_timeout 600s;
        proxy_read_timeout 600s;
    }

    # SSE (Server-Sent Events) for progress streaming
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

    # Gzip compression
    gzip on;
    gzip_vary on;
    gzip_proxied any;
    gzip_comp_level 6;
    gzip_types text/plain text/css application/json application/javascript text/xml application/xml application/xml+rss text/javascript image/svg+xml;
    gzip_min_length 1000;

    # Cache static assets
    location ~* \.(js|css|png|jpg|jpeg|gif|ico|svg|woff|woff2|ttf|eot)$ {
        expires 30d;
        add_header Cache-Control "public, immutable";
NGINXEOF

        if [[ "$SKIP_FRONTEND" == "false" ]]; then
            echo "        root ${FRONTEND_DIR}/dist;" >> "$NGINX_SRC"
        fi

        cat >> "$NGINX_SRC" <<'NGINXEOF'
    }

    # Max upload size for restores/imports
    client_max_body_size 0;
}
NGINXEOF

        success "Nginx config written: ${NGINX_SRC}"

        # Enable site (only if using sites-available/sites-enabled layout)
        if [[ -n "$NGINX_LINK" ]]; then
            ln -sf "$NGINX_SRC" "$NGINX_LINK"
            rm -f "${NGINX_CONF_DIR}/sites-enabled/default" 2>/dev/null || true
            success "Symlinked: ${NGINX_LINK} -> ${NGINX_SRC}"
        fi

        # Test nginx config
        if nginx -t 2>&1; then
            success "Nginx configuration valid"
            systemctl enable nginx 2>/dev/null || true
            # UGREEN NAS may wrap nginx with a reset_nginx_config hook on restart
            # that re-injects ugreen.conf. A plain `nginx -s reload` keeps our
            # server.d config intact while re-reading nginx.conf.
            if ! systemctl restart nginx 2>/dev/null && ! service nginx restart 2>/dev/null; then
                warn "systemctl restart failed, trying nginx -s reload..."
                nginx -s reload 2>&1 || warn "nginx reload also failed"
            fi
            # Verify 9000 is actually listening
            sleep 2
            if ss -tlnp 2>/dev/null | grep -q ':9000 ' || netstat -tlnp 2>/dev/null | grep -q ':9000 '; then
                success "Nginx is listening on port 9000"
            else
                warn "Port 9000 is NOT listening after nginx restart"
                warn "Possible causes:"
                warn "  1. UGREEN nginx reset_nginx_config hook wipes non-ugreen configs on restart"
                warn "  2. Another service already uses port 9000"
                warn "  3. nginx config include path not detected correctly"
                warn "Try: sudo nginx -s reload  (reload preserves existing configs)"
            fi
        else
            warn "Nginx configuration test failed - nginx not started. Backend API still available on port 8080."
            warn "The config file was written to: ${NGINX_SRC}"
            warn "Check that nginx.conf includes:  include ${NGINX_INCLUDE_DIR}/*.conf;"
        fi
    fi
elif [[ "$NO_NGINX" == "false" ]]; then
    step "Skipping Nginx configuration (need root)"
fi

# ------------------------------------------------------------------------------
# Step 11: Start backend service (root only, via systemd)
# ------------------------------------------------------------------------------
if [[ "$IS_ROOT" == "true" ]]; then
    step "Starting NAS Backup service"

    systemctl restart nas-backup
    sleep 3

    # Check service status
    if systemctl is-active --quiet nas-backup; then
        success "nas-backup service is running"
    else
        warn "nas-backup service failed to start. Check logs with: journalctl -u nas-backup -n 50"
        warn "You can also start manually for debugging:"
        warn "  cd ${BACKEND_DIR} && ./nas-backup -config ${CONFIG_FILE}"
    fi
else
    step "Skipping service start (need root for systemd)"
fi

# ------------------------------------------------------------------------------
# Step 12: Verify deployment (try to start temporarily if not root)
# ------------------------------------------------------------------------------
step "Verifying build"

success "Backend binary: ${BACKEND_DIR}/nas-backup ($(file "${BACKEND_DIR}/nas-backup" | cut -d: -f2 | xargs))"
if [[ "$SKIP_FRONTEND" == "false" && -d "${FRONTEND_DIR}/dist" ]]; then
    success "Frontend build: ${FRONTEND_DIR}/dist/"
fi
success "Data directory: ${DATA_DIR}"
if [[ -f "${DATA_DIR}/master.key" ]]; then
    success "Master key: ${DATA_DIR}/master.key"
fi
success "rclone bundled: ${BACKEND_DIR}/bin/rclone ($("${BACKEND_DIR}/bin/rclone" version 2>/dev/null | head -1))"

# If root and service is running, do API health check
if [[ "$IS_ROOT" == "true" ]] && systemctl is-active --quiet nas-backup 2>/dev/null; then
    step "Verifying API health"
    sleep 2

    BACKEND_OK=false
    for i in {1..10}; do
        if curl -s --connect-timeout 2 http://127.0.0.1:8080/api/dashboard/stats >/dev/null 2>&1; then
            BACKEND_OK=true
            break
        fi
        sleep 1
    done

    if [[ "$BACKEND_OK" == "true" ]]; then
        success "Backend API is responding on http://127.0.0.1:8080"
        curl -s http://127.0.0.1:8080/api/dashboard/stats | head -c 200
        echo ""
    else
        warn "Backend API not responding yet, check logs: journalctl -u nas-backup -f"
    fi

    # Test storage health
    STORAGE_HEALTH=$(curl -s http://127.0.0.1:8080/api/storage/health 2>/dev/null || echo "{}")
    if echo "$STORAGE_HEALTH" | grep -q '"status":"ok"'; then
        success "Storage/OSS connection healthy"
    else
        warn "Storage health check returned: ${STORAGE_HEALTH}"
        warn "If using OSS, verify your credentials in config.yaml"
    fi

    # Test Nginx
    if [[ "$NO_NGINX" == "false" ]]; then
        if curl -s --connect-timeout 2 http://127.0.0.1:9000/api/dashboard/stats >/dev/null 2>&1; then
            success "Nginx reverse proxy is working (port 9000)"
        else
            warn "Nginx proxy check failed"
        fi
    fi
fi

# Get server IP
SERVER_IP=$(hostname -I 2>/dev/null | awk '{print $1}' || echo "YOUR_SERVER_IP")

# ------------------------------------------------------------------------------
# Create database backup script & management script
# ------------------------------------------------------------------------------
step "Creating helper scripts"

DB_BACKUP_SCRIPT="${INSTALL_DIR}/backup-db.sh"
cat > "$DB_BACKUP_SCRIPT" <<'DBEOF'
#!/bin/bash
# NAS Backup - Database backup script
BACKUP_DIR="$(cd "$(dirname "$0")" && pwd)/db-backups"
DB_PATH="{{DB_PATH}}"
DATE=$(date +%Y%m%d_%H%M%S)
KEEP=30

mkdir -p "$BACKUP_DIR"
if [[ -f "$DB_PATH" ]]; then
    sqlite3 "$DB_PATH" ".backup '${BACKUP_DIR}/nas-backup_${DATE}.db'"
    ls -t "$BACKUP_DIR"/nas-backup_*.db 2>/dev/null | tail -n +$((KEEP+1)) | xargs -r rm
    echo "Database backed up to: ${BACKUP_DIR}/nas-backup_${DATE}.db"
else
    echo "Database file not found: $DB_PATH (may not exist until first run)"
fi
DBEOF

# Determine actual DB path from config
if [[ "$CONFIG_EXISTS" == "true" ]]; then
    ACTUAL_DB_PATH="$(grep -E '^\s+path:' "$CONFIG_FILE" | head -1 | sed 's/.*path:\s*["'\'']\?\(.*\)["'\'']\?/\1/' | xargs || echo "./data/nas-backup.db")"
    if [[ "$ACTUAL_DB_PATH" != /* ]]; then
        ACTUAL_DB_PATH="${BACKEND_DIR}/${ACTUAL_DB_PATH#./}"
    fi
else
    ACTUAL_DB_PATH="${DATA_DIR}/nas-backup.db"
fi

sed -i "s|{{DB_PATH}}|${ACTUAL_DB_PATH}|g" "$DB_BACKUP_SCRIPT"
chmod +x "$DB_BACKUP_SCRIPT"
success "Database backup script: ${DB_BACKUP_SCRIPT}"

# Add to crontab (root only)
if [[ "$IS_ROOT" == "true" ]]; then
    if ! crontab -l 2>/dev/null | grep -q "backup-db.sh"; then
        (crontab -l 2>/dev/null; echo "0 2 * * * ${DB_BACKUP_SCRIPT} >> ${DATA_DIR}/logs/db-backup.log 2>&1") | crontab -
        success "Added daily database backup cron job (2:00 AM)"
    fi
fi

# Management script
cat > "${INSTALL_DIR}/manage.sh" <<'MANAGE'
#!/bin/bash
# NAS Backup - Management script
set -euo pipefail
case "${1:-status}" in
    start)    sudo systemctl start nas-backup ;;
    stop)     sudo systemctl stop nas-backup ;;
    restart)  sudo systemctl restart nas-backup ;;
    status)   sudo systemctl status nas-backup ;;
    logs)     sudo journalctl -u nas-backup -f -n 100 ;;
    app-logs) tail -f {{DATA_DIR}}/logs/nas-backup.log ;;
    db-backup) {{INSTALL_DIR}}/backup-db.sh ;;
    run)      cd {{BACKEND_DIR}} && ./nas-backup -config {{CONFIG_FILE}} ;;
    *)
        echo "Usage: $0 {start|stop|restart|status|logs|app-logs|db-backup|run}"
        echo ""
        echo "  start/stop/restart/status: systemd service control (requires root)"
        echo "  logs:    follow systemd journal"
        echo "  app-logs: follow application log file"
        echo "  db-backup: run database backup now"
        echo "  run:     start backend directly in foreground (no systemd)"
        exit 1 ;;
esac
MANAGE
sed -i "s|{{DATA_DIR}}|${DATA_DIR}|g" "${INSTALL_DIR}/manage.sh"
sed -i "s|{{INSTALL_DIR}}|${INSTALL_DIR}|g" "${INSTALL_DIR}/manage.sh"
sed -i "s|{{BACKEND_DIR}}|${BACKEND_DIR}|g" "${INSTALL_DIR}/manage.sh"
sed -i "s|{{CONFIG_FILE}}|${CONFIG_FILE}|g" "${INSTALL_DIR}/manage.sh"
chmod +x "${INSTALL_DIR}/manage.sh"
success "Management script: ${INSTALL_DIR}/manage.sh"

# ------------------------------------------------------------------------------
# Deployment complete
# ------------------------------------------------------------------------------
echo ""
echo -e "${GREEN}${BOLD}======================================================================${NC}"
if [[ "$IS_ROOT" == "true" ]]; then
echo -e "${GREEN}${BOLD}  NAS Backup System - Debian Deployment Complete!${NC}"
else
echo -e "${GREEN}${BOLD}  NAS Backup System - Build Complete!${NC}"
fi
echo -e "${GREEN}${BOLD}======================================================================${NC}"
echo ""
echo -e "  ${BOLD}Installation paths:${NC}"
echo -e "    Project root:    ${INSTALL_DIR}"
echo -e "    Backend binary:  ${BACKEND_DIR}/nas-backup"
echo -e "    Config file:     ${CONFIG_FILE}"
echo -e "    Data directory:  ${DATA_DIR}"
echo -e "    Logs:            ${DATA_DIR}/logs/nas-backup.log"
if [[ "$SKIP_FRONTEND" == "false" ]]; then
echo -e "    Frontend build:  ${FRONTEND_DIR}/dist/"
fi
echo ""
if [[ "$IS_ROOT" == "true" ]]; then
echo -e "  ${BOLD}Service management:${NC}"
echo -e "    Start:           sudo systemctl start nas-backup"
echo -e "    Stop:            sudo systemctl stop nas-backup"
echo -e "    Restart:         sudo systemctl restart nas-backup"
echo -e "    Status:          sudo systemctl status nas-backup"
echo -e "    View logs:       sudo journalctl -u nas-backup -f"
echo -e "    Quick manage:    ${INSTALL_DIR}/manage.sh"
echo ""
echo -e "  ${BOLD}Access:${NC}"
echo -e "    Backend API:     http://127.0.0.1:8080/api/"
if [[ "$NO_NGINX" == "false" ]]; then
echo -e "    Web UI (LAN):    http://${SERVER_IP}:9000/"
fi
else
echo -e "  ${BOLD}To start the service (run as root):${NC}"
echo -e "    sudo systemctl start nas-backup   (if systemd service was installed)"
echo -e "    or manually:"
echo -e "    cd ${BACKEND_DIR} && sudo ./nas-backup -config ${CONFIG_FILE}"
echo -e ""
echo -e "  ${BOLD}To install systemd service, re-run with sudo:${NC}"
echo -e "    sudo bash $0 --skip-deps"
echo ""
echo -e "  ${BOLD}Access:${NC}"
echo -e "    Backend API:     http://127.0.0.1:8080/api/ (after starting)"
fi
echo ""
echo -e "  ${BOLD}Important post-install steps:${NC}"
echo -e "    1. Verify config.yaml has correct OSS credentials and backup directories"
echo -e "    2. Add backup directories via the Web UI or edit config.yaml directly"
echo -e "    3. If you changed config.yaml, restart: sudo systemctl restart nas-backup"
echo -e "    4. Check storage health: curl http://127.0.0.1:8080/api/storage/health"
echo ""
