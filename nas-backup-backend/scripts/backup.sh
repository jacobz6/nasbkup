#!/bin/bash
# backup.sh — Manual backup wrapper script
# Usage: ./backup.sh [--full|--incremental] [--config CONFIG_PATH]
#
# This script provides a convenient CLI for triggering backups
# without the HTTP API.

set -euo pipefail

# ---------------------------------------------------------------------------
# Defaults
# ---------------------------------------------------------------------------
BACKUP_TYPE="incremental"
CONFIG_PATH=""
NAS_BACKUP_BIN=""

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"

# ---------------------------------------------------------------------------
# Parse arguments
# ---------------------------------------------------------------------------
while [[ $# -gt 0 ]]; do
    case "$1" in
        --full)         BACKUP_TYPE="full";        shift ;;
        --incremental)  BACKUP_TYPE="incremental"; shift ;;
        --config)       CONFIG_PATH="$2";          shift 2 ;;
        -h|--help)
            echo "Usage: $0 [--full|--incremental] [--config CONFIG_PATH]"
            echo ""
            echo "Options:"
            echo "  --full          Run a full backup (default: incremental)"
            echo "  --incremental   Run an incremental backup"
            echo "  --config PATH   Path to config.yaml"
            exit 0 ;;
        *)
            echo "Unknown option: $1" >&2
            exit 1 ;;
    esac
done

# Default config path
if [[ -z "$CONFIG_PATH" ]]; then
    CONFIG_PATH="${PROJECT_DIR}/config.yaml"
fi

# ---------------------------------------------------------------------------
# Find nas-backup binary
# ---------------------------------------------------------------------------
if [[ -z "$NAS_BACKUP_BIN" ]]; then
    # Check common locations
    candidates=(
        "${PROJECT_DIR}/nas-backup"
        "${PROJECT_DIR}/bin/nas-backup"
        "$(command -v nas-backup 2>/dev/null || true)"
    )
    for candidate in "${candidates[@]}"; do
        if [[ -x "$candidate" ]]; then
            NAS_BACKUP_BIN="$candidate"
            break
        fi
    done
fi

if [[ -z "$NAS_BACKUP_BIN" ]]; then
    echo "ERROR: nas-backup binary not found." >&2
    echo "Build it first: cd ${PROJECT_DIR} && go build -o nas-backup ./cmd/nas-backup" >&2
    exit 1
fi

# ---------------------------------------------------------------------------
# Trigger backup via API
# ---------------------------------------------------------------------------
# If the server is running, use the API
API_URL="http://localhost:8080/api/backup/trigger"

echo "Triggering ${BACKUP_TYPE} backup..."
echo "  Config: ${CONFIG_PATH}"

# Try API first (if server is running)
if curl -sf --max-time 5 "http://localhost:8080/api/backup/status" >/dev/null 2>&1; then
    echo "  Server is running, triggering via API..."
    
    RESPONSE=$(curl -sf -X POST "$API_URL" \
        -H "Content-Type: application/json" \
        -d "{\"type\": \"${BACKUP_TYPE}\"}" 2>&1) || {
        echo "ERROR: Failed to trigger backup via API" >&2
        echo "  $RESPONSE" >&2
        exit 1
    }
    
    echo "✓ Backup triggered successfully"
    echo "  Response: $RESPONSE"
else
    echo "  Server is not running. Starting backup directly..."
    
    # Run the binary directly with a one-shot command
    # This requires the binary to support a --backup flag
    cd "${PROJECT_DIR}"
    "$NAS_BACKUP_BIN" --config "$CONFIG_PATH" --backup "$BACKUP_TYPE" 2>&1 || {
        echo "ERROR: Backup failed" >&2
        exit 1
    }
    
    echo "✓ Backup completed"
fi
