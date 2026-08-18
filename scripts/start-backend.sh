#!/bin/bash
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
cd "${PROJECT_ROOT}/nas-backup-backend"
echo "启动 NAS Backup 后端 http://127.0.0.1:8080 ... (Ctrl+C 停止)"
exec ./nas-backup -config config.yaml
