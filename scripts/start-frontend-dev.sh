#!/bin/bash
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
cd "${PROJECT_ROOT}/nas-backup-frontend"
echo "启动 Vite 开发服务器 http://localhost:5173 ... (API 代理到 8080)"
exec npm run dev
