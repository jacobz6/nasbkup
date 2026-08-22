#!/bin/bash
# backup.sh — Manual backup trigger wrapper
# Usage: ./backup.sh [--config CONFIG_PATH]
#
# Every backup is a standalone session (no full/incremental distinction),
# so this simply triggers a backup through the running backend HTTP API.
#
# Prerequisite: the backend service must be running on localhost:8080.

set -euo pipefail

# ---------------------------------------------------------------------------
# Defaults & arg parsing
# ---------------------------------------------------------------------------
CONFIG_PATH=""
API_URL="http://localhost:8080/api/backup/trigger"
STATUS_URL="http://localhost:8080/api/backup/status"

PROJECT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

while [[ $# -gt 0 ]]; do
    case "$1" in
        --config)       CONFIG_PATH="$2"; shift 2 ;;
        -h|--help)
            echo "Usage: $0 [--config CONFIG_PATH]"
            echo ""
            echo "Options:"
            echo "  --config PATH   Path to config.yaml (informational only)"
            exit 0 ;;
        *)
            echo "Unknown option: $1" >&2
            exit 1 ;;
    esac
done

if [[ -z "$CONFIG_PATH" ]]; then
    CONFIG_PATH="${PROJECT_DIR}/config/config.yaml"
fi
echo "  Config: ${CONFIG_PATH}"

# ---------------------------------------------------------------------------
# Trigger backup via API
# ---------------------------------------------------------------------------
if ! curl -sf --max-time 5 "$STATUS_URL" >/dev/null 2>&1; then
    echo "ERROR: Backend server is not reachable at ${STATUS_URL}" >&2
    echo "Start the backend first: ${PROJECT_DIR}/nas-backup -config ${CONFIG_PATH}" >&2
    exit 1
fi

RESPONSE=$(curl -sf -X POST "$API_URL" \
    -H "Content-Type: application/json" \
    -d '{}' 2>&1) || {
    echo "ERROR: Failed to trigger backup via API" >&2
    echo "  $RESPONSE" >&2
    exit 1
}

echo "✓ Backup triggered successfully"
echo "  Response: $RESPONSE"