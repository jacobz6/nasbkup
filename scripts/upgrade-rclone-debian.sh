#!/bin/bash
# ==============================================================================
# NAS Backup System - rclone Upgrade Script for Debian NAS
# ==============================================================================
# Safely upgrades rclone on Debian-based NAS (UGREEN/极空间/群晖 etc) WITHOUT
# modifying system packages. Downloads the latest official binary and replaces
# the project-bundled copy plus optionally upgrades the system rclone.
#
# What it does:
#   1. Detects current rclone version (both system and project-bundled)
#   2. Obtains rclone binary (local zip preferred, download as fallback)
#   3. Replaces project-bundled rclone (bin/rclone) — always safe
#   4. Optionally replaces system rclone (/usr/local/bin/rclone) — with backup
#   5. Verifies the new version works
#
# Usage:
#   chmod +x scripts/upgrade-rclone-debian.sh
#   sudo ./scripts/upgrade-rclone-debian.sh [OPTIONS]
#
# Options:
#   --local-file PATH      Use a local rclone zip file instead of downloading
#   --project-dir DIR      Project root directory (auto-detected by default)
#   --skip-system          Skip upgrading system rclone (only update project-bundled)
#   --only-project         Same as --skip-system
#   --help                 Show this help message
#
# Local file auto-detection:
#   The script automatically looks for rclone zip files in the scripts/ directory
#   before attempting to download. Supported filenames:
#     - rclone-linux-<arch>.zip
#     - rclone-v*-linux-<arch>.zip  (e.g. rclone-v1.75.0-linux-amd64.zip)
#   Use --local-file to specify a custom path.
#
# SAFETY NOTICE:
#   - NEVER runs apt upgrade / apt dist-upgrade
#   - Downloads official binaries only (or uses pre-downloaded local files)
#   - Backs up existing binaries before replacing
#   - Does NOT modify NAS firmware or vendor packages
# ==============================================================================

set -euo pipefail

# Add common binary paths for when running via sudo
COMMON_USER_PATHS=(
    "/usr/local/sbin" "/usr/local/bin"
    "/usr/sbin" "/usr/bin" "/sbin" "/bin"
    "$HOME/go/bin" "$HOME/.local/bin" "$HOME/bin"
)
if [[ -n "${SUDO_USER:-}" ]]; then
    REAL_USER_HOME="$(eval echo "~${SUDO_USER}")"
    COMMON_USER_PATHS+=(
        "${REAL_USER_HOME}/go/bin"
        "${REAL_USER_HOME}/.local/bin"
        "${REAL_USER_HOME}/bin"
    )
fi
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
SKIP_SYSTEM=false
PROJECT_DIR=""
LOCAL_FILE=""

while [[ $# -gt 0 ]]; do
    case "$1" in
        --project-dir) PROJECT_DIR="$2"; shift 2 ;;
        --skip-system) SKIP_SYSTEM=true; shift ;;
        --only-project) SKIP_SYSTEM=true; shift ;;
        --local-file)  LOCAL_FILE="$2"; shift 2 ;;
        -h|--help)
            awk 'NR>=2 && NR<=42 {sub(/^# ?/,""); print}' "$0"
            exit 0 ;;
        *)
            echo "Unknown option: $1"
            echo "Use -h for help"
            exit 1 ;;
    esac
done

# ------------------------------------------------------------------------------
# Detect project directory
# ------------------------------------------------------------------------------
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DETECTED_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

if [[ -z "$PROJECT_DIR" ]]; then
    if [[ -d "${DETECTED_ROOT}/nas-backup-backend" ]]; then
        PROJECT_DIR="$DETECTED_ROOT"
    else
        PROJECT_DIR="/opt/nas-backup"
    fi
fi

BACKEND_DIR="${PROJECT_DIR}/nas-backup-backend"
PROJECT_RCLONE="${BACKEND_DIR}/bin/rclone"

if [[ ! -d "$BACKEND_DIR" ]]; then
    warn "Backend directory not found: ${BACKEND_DIR}"
    warn "This script should be run from the project root or with --project-dir"
    warn "Attempting to continue anyway..."
fi

# ------------------------------------------------------------------------------
# Pre-flight checks
# ------------------------------------------------------------------------------
step "Pre-flight checks"

IS_ROOT=false
if [[ $EUID -eq 0 ]]; then
    IS_ROOT=true
    info "Running as root"
else
    warn "Not running as root — will update project-bundled rclone only"
    warn "Run with sudo to also upgrade system rclone"
    SKIP_SYSTEM=true
fi

# Detect architecture
ARCH="$(dpkg --print-architecture 2>/dev/null || uname -m)"
case "$ARCH" in
    amd64|x86_64)  RCLONE_ARCH="amd64" ;;
    arm64|aarch64) RCLONE_ARCH="arm64" ;;
    armhf|armv7l)  RCLONE_ARCH="arm-v7" ;;
    *)             fail "Unsupported architecture: $ARCH" ;;
esac
success "Architecture: $ARCH (rclone arch: $RCLONE_ARCH)"

# Check download tools (not needed when using local file)
if [[ -z "$LOCAL_FILE" ]] && [[ ! -f "${SCRIPT_DIR}/rclone-linux-${ARCH_SUFFIX}.zip" ]]; then
    # We may need curl/wget if no local archive is found later
    if ! command -v curl &>/dev/null && ! command -v wget &>/dev/null; then
        warn "Neither curl nor wget found. If no local rclone archive exists in scripts/, the download step will fail."
        warn "You can place a rclone-*-linux-${ARCH_SUFFIX}.zip file in scripts/ first."
    else
        success "Download tools available"
    fi
fi

# ------------------------------------------------------------------------------
# Step 1: Check current rclone versions
# ------------------------------------------------------------------------------
step "Checking current rclone versions"

# System rclone
SYS_RCLONE=""
if command -v rclone &>/dev/null; then
    SYS_RCLONE="$(which rclone)"
    SYS_VERSION="$(rclone version 2>/dev/null | head -1)"
    success "System rclone: ${SYS_RCLONE} (${SYS_VERSION:-unknown})"
else
    info "System rclone: not found"
fi

# Project-bundled rclone
if [[ -f "$PROJECT_RCLONE" ]]; then
    PROJ_VERSION="$("$PROJECT_RCLONE" version 2>/dev/null | head -1)"
    success "Project rclone: ${PROJECT_RCLONE} (${PROJ_VERSION:-unknown})"
else
    warn "Project-bundled rclone not found at ${PROJECT_RCLONE}"
fi

# ------------------------------------------------------------------------------
# Step 2: Obtain rclone binary (local file preferred, download as fallback)
# ------------------------------------------------------------------------------
step "Obtaining rclone binary"

TMP_DIR="/tmp/rclone-upgrade-$$"
mkdir -p "$TMP_DIR"

# Resolve version from local file name or auto-detect
RCLONE_LATEST=""
RCLONE_ZIP=""

# Determine target architecture suffix for local file matching
case "$RCLONE_ARCH" in
    amd64)  ARCH_SUFFIX="amd64" ;;
    arm64)  ARCH_SUFFIX="arm64" ;;
    arm-v7) ARCH_SUFFIX="arm-v7" ;;
    *)      ARCH_SUFFIX="amd64" ;;
esac

# === Source A: explicit --local-file ===
if [[ -n "$LOCAL_FILE" ]]; then
    if [[ ! -f "$LOCAL_FILE" ]]; then
        fail "Specified local file not found: $LOCAL_FILE"
    fi
    info "Using specified local file: $LOCAL_FILE"
    cp "$LOCAL_FILE" "${TMP_DIR}/rclone.zip"
    # Extract version from filename: rclone-v1.75.0-linux-amd64.zip -> v1.75.0
    RCLONE_LATEST=$(basename "$LOCAL_FILE" | grep -oP 'v\d+\.\d+\.\d+' || echo "")
    RCLONE_ZIP="$(basename "$LOCAL_FILE")"
    if [[ -z "$RCLONE_LATEST" ]]; then
        RCLONE_LATEST="v1.75.0"
        warn "Could not parse version from filename, defaulting to ${RCLONE_LATEST}"
    else
        info "Parsed version from filename: ${RCLONE_LATEST}"
    fi

# === Source B: auto-detect in scripts/ directory ===
elif [[ -f "${SCRIPT_DIR}/rclone-linux-${ARCH_SUFFIX}.zip" ]]; then
    LOCAL_FILE="${SCRIPT_DIR}/rclone-linux-${ARCH_SUFFIX}.zip"
    info "Found local archive in scripts/: $LOCAL_FILE"
    cp "$LOCAL_FILE" "${TMP_DIR}/rclone.zip"
    RCLONE_LATEST=$(basename "$LOCAL_FILE" | grep -oP 'v\d+\.\d+\.\d+' || echo "")
    RCLONE_ZIP="$(basename "$LOCAL_FILE")"
    if [[ -z "$RCLONE_LATEST" ]]; then
        RCLONE_LATEST="v1.75.0"
        warn "Could not parse version from filename, defaulting to ${RCLONE_LATEST}"
    fi

# === Source C: auto-detect any rclone-*.zip in scripts/ ===
else
    for candidate in "${SCRIPT_DIR}"/rclone-*-linux-${ARCH_SUFFIX}.zip "${SCRIPT_DIR}"/rclone-*.zip; do
        if [[ -f "$candidate" ]]; then
            LOCAL_FILE="$candidate"
            info "Found local archive: $LOCAL_FILE"
            cp "$LOCAL_FILE" "${TMP_DIR}/rclone.zip"
            RCLONE_LATEST=$(basename "$LOCAL_FILE" | grep -oP 'v\d+\.\d+\.\d+' || echo "")
            RCLONE_ZIP="$(basename "$LOCAL_FILE")"
            if [[ -z "$RCLONE_LATEST" ]]; then
                RCLONE_LATEST="v1.75.0"
                warn "Could not parse version from filename, defaulting to ${RCLONE_LATEST}"
            fi
            break
        fi
    done
fi

# === Source D: download from internet ===
if [[ ! -f "${TMP_DIR}/rclone.zip" ]]; then
    info "No local rclone archive found, downloading from internet..."

    # Try to get the latest version from the official download page
    if command -v curl &>/dev/null; then
        RCLONE_LATEST=$(curl -sL --connect-timeout 30 "https://downloads.rclone.org/" 2>/dev/null | \
            grep -oP 'v\d+\.\d+\.\d+' | sort -Vu | tail -1 || true)
    fi

    if [[ -z "$RCLONE_LATEST" ]]; then
        RCLONE_LATEST="v1.75.0"
        warn "Could not resolve latest version, defaulting to ${RCLONE_LATEST}"
    else
        info "Latest rclone version: ${RCLONE_LATEST}"
    fi

    RCLONE_ZIP="rclone-${RCLONE_LATEST}-linux-${ARCH_SUFFIX}.zip"
    DOWNLOAD_URL="https://downloads.rclone.org/${RCLONE_LATEST}/${RCLONE_ZIP}"

    info "Downloading ${DOWNLOAD_URL} ..."
    if command -v curl &>/dev/null; then
        curl -fSL --connect-timeout 60 --max-time 120 "$DOWNLOAD_URL" -o "${TMP_DIR}/rclone.zip" || \
            { warn "curl download failed, trying wget..."; \
              wget -q --timeout=60 --tries=2 -O "${TMP_DIR}/rclone.zip" "$DOWNLOAD_URL" || \
              fail "Failed to download rclone from ${DOWNLOAD_URL}"; }
    else
        wget -q --timeout=60 --tries=2 -O "${TMP_DIR}/rclone.zip" "$DOWNLOAD_URL" || \
            fail "Failed to download rclone from ${DOWNLOAD_URL}"
    fi
fi

success "rclone archive ready: ${RCLONE_ZIP} (version: ${RCLONE_LATEST})"

# Extract
info "Extracting..."
unzip -q -o "${TMP_DIR}/rclone.zip" -d "$TMP_DIR"

# Locate the extracted rclone binary — try the standard naming pattern first,
# then fall back to a filesystem search inside the temp dir.
RCLONE_EXTRACTED=""
# Pattern 1: rclone-v1.75.0-linux-amd64/rclone (arch suffix from official naming)
if [[ -f "${TMP_DIR}/rclone-${RCLONE_LATEST}-linux-${RCLONE_ARCH}/rclone" ]]; then
    RCLONE_EXTRACTED="${TMP_DIR}/rclone-${RCLONE_LATEST}-linux-${RCLONE_ARCH}/rclone"
fi
# Pattern 2: rclone-v1.75.0-linux-amd64/rclone (using ARCH_SUFFIX)
if [[ -z "$RCLONE_EXTRACTED" ]] && [[ -f "${TMP_DIR}/rclone-${RCLONE_LATEST}-linux-${ARCH_SUFFIX}/rclone" ]]; then
    RCLONE_EXTRACTED="${TMP_DIR}/rclone-${RCLONE_LATEST}-linux-${ARCH_SUFFIX}/rclone"
fi
# Pattern 3: filesystem search fallback
if [[ -z "$RCLONE_EXTRACTED" ]]; then
    RCLONE_EXTRACTED="$(find "$TMP_DIR" -name rclone -type f -executable 2>/dev/null | head -1)"
fi
if [[ -z "$RCLONE_EXTRACTED" ]] || [[ ! -f "$RCLONE_EXTRACTED" ]]; then
    fail "Could not find extracted rclone binary in ${TMP_DIR}. Contents:"
    find "$TMP_DIR" -maxdepth 3 -ls 2>/dev/null
fi

# Verify the downloaded binary works
NEW_VERSION="$("$RCLONE_EXTRACTED" version 2>/dev/null | head -1)"
if [[ -z "$NEW_VERSION" ]]; then
    fail "Downloaded rclone binary does not work: ${RCLONE_EXTRACTED}"
fi
success "New rclone binary verified: ${NEW_VERSION}"

# Check if --no-clobber is supported (rclone >= 1.65)
if "$RCLONE_EXTRACTED" help copyto 2>/dev/null | grep -q '\-\-no-clobber'; then
    success "New rclone supports --no-clobber flag (>= v1.65)"
else
    info "New rclone does NOT support --no-clobber (pre-v1.65)"
fi

# ------------------------------------------------------------------------------
# Step 3: Update project-bundled rclone
# ------------------------------------------------------------------------------
step "Updating project-bundled rclone"

mkdir -p "$(dirname "$PROJECT_RCLONE")"

# Backup existing project rclone
if [[ -f "$PROJECT_RCLONE" ]]; then
    BACKUP_PATH="${PROJECT_RCLONE}.bak.$(date +%Y%m%d%H%M%S)"
    cp "$PROJECT_RCLONE" "$BACKUP_PATH"
    info "Backed up existing project rclone to: ${BACKUP_PATH}"
fi

cp "$RCLONE_EXTRACTED" "$PROJECT_RCLONE"
chmod +x "$PROJECT_RCLONE"
PROJ_NEW_VERSION="$("$PROJECT_RCLONE" version 2>/dev/null | head -1)"
success "Project rclone updated: ${PROJECT_RCLONE} (${PROJ_NEW_VERSION})"

# ------------------------------------------------------------------------------
# Step 4: Update system rclone (optional)
# ------------------------------------------------------------------------------
if [[ "$SKIP_SYSTEM" == "false" ]] && [[ "$IS_ROOT" == "true" ]]; then
    step "Updating system rclone"

    # Determine system rclone install path
    SYS_INSTALL="/usr/local/bin/rclone"
    if command -v rclone &>/dev/null; then
        SYS_INSTALL="$(which rclone)"
    fi

    # Backup existing system rclone
    if [[ -f "$SYS_INSTALL" ]]; then
        BACKUP_PATH="${SYS_INSTALL}.bak.$(date +%Y%m%d%H%M%S)"
        cp "$SYS_INSTALL" "$BACKUP_PATH"
        info "Backed up system rclone to: ${BACKUP_PATH}"
    fi

    cp "$RCLONE_EXTRACTED" "$SYS_INSTALL"
    chmod +x "$SYS_INSTALL"
    SYS_NEW_VERSION="$("$SYS_INSTALL" version 2>/dev/null | head -1)"
    success "System rclone updated: ${SYS_INSTALL} (${SYS_NEW_VERSION})"
else
    if [[ "$SKIP_SYSTEM" == "true" ]]; then
        info "Skipping system rclone update (--skip-system)"
    else
        info "Skipping system rclone update (not running as root)"
    fi
fi

# ------------------------------------------------------------------------------
# Step 5: Verify
# ------------------------------------------------------------------------------
step "Verification"

success "Project rclone: $("$PROJECT_RCLONE" version 2>/dev/null | head -1)"
if [[ "$SKIP_SYSTEM" == "false" ]] && [[ "$IS_ROOT" == "true" ]]; then
    success "System rclone: $(rclone version 2>/dev/null | head -1)"
fi

# Check that --no-clobber is now available
if "$PROJECT_RCLONE" help copyto 2>/dev/null | grep -q '\-\-no-clobber'; then
    success "--no-clobber flag is available in project rclone"
else
    warn "--no-clobber flag NOT available"
fi

# Quick smoke test: can rclone run basic commands?
if "$PROJECT_RCLONE" lsd &>/dev/null; then
    success "rclone basic functionality verified"
else
    warn "rclone smoke test had issues (may be normal without config)"
fi

# Cleanup
rm -rf "$TMP_DIR"
info "Cleaned up temporary files"

# ------------------------------------------------------------------------------
# Next steps
# ------------------------------------------------------------------------------
echo ""
echo -e "${GREEN}${BOLD}======================================================================${NC}"
echo -e "${GREEN}${BOLD}  rclone Upgrade Complete!${NC}"
echo -e "${GREEN}${BOLD}======================================================================${NC}"
echo ""
echo -e "  ${BOLD}Updated:${NC}"
echo -e "    Project rclone:  ${PROJECT_RCLONE}"
echo -e "    Version:          ${NEW_VERSION}"
echo ""
if [[ "$SKIP_SYSTEM" == "false" ]] && [[ "$IS_ROOT" == "true" ]]; then
echo -e "    System rclone:    $(which rclone 2>/dev/null || echo 'N/A')"
echo ""
fi
echo -e "  ${BOLD}Next steps:${NC}"
echo -e "    1. Ensure config.yaml has:  binary_path: \"./bin/rclone\""
echo -e "       (This ensures the project uses its bundled rclone, not the system one)"
echo -e "    2. Restart the service:   sudo systemctl restart nas-backup"
echo -e "    3. Test a backup to verify everything works"
echo ""
echo -e "  ${BOLD}To verify config.yaml uses the bundled rclone:${NC}"
echo -e "    grep 'binary_path' ${BACKEND_DIR}/config/config.yaml"
echo -e "    # Should show:  binary_path: \"./bin/rclone\""
echo ""