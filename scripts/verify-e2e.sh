#!/bin/bash
# ==============================================================================
# NAS Backup System - Complete E2E Verification Script
# ==============================================================================
# This script:
#   1. Ensures the backend is built (runs deploy.sh if needed)
#   2. Starts the backend server
#   3. Runs the Python E2E verification suite
#   4. Generates an HTML acceptance report
#   5. Stops the backend
#
# Usage:
#   chmod +x scripts/verify-e2e.sh
#   ./scripts/verify-e2e.sh [--skip-build]
# ==============================================================================

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
BACKEND_DIR="${PROJECT_ROOT}/nas-backup-backend"
BACKEND_BIN="${BACKEND_DIR}/nas-backup"
CONFIG_FILE="${BACKEND_DIR}/config/config.yaml"
VENV_PYTHON=""

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
BOLD='\033[1m'
NC='\033[0m'

info()    { echo -e "${BLUE}[INFO]${NC}  $*"; }
success() { echo -e "${GREEN}[OK]${NC}    $*"; }
warn()    { echo -e "${YELLOW}[WARN]${NC}  $*"; }
fail()    { echo -e "${RED}[FAIL]${NC}  $*"; }
step()    { echo -e "\n${BOLD}>>> $*${NC}"; }

SKIP_BUILD=false
while [[ $# -gt 0 ]]; do
    case "$1" in
        --skip-build) SKIP_BUILD=true; shift ;;
        -h|--help)
            head -20 "$0" | grep '^#' | sed 's/^# \?//'
            exit 0 ;;
        *) echo "Unknown option: $1"; exit 1 ;;
    esac
done

# ------------------------------------------------------------------------------
# Step 0: Build if needed
# ------------------------------------------------------------------------------
if [[ "$SKIP_BUILD" == "false" ]]; then
    if [[ ! -f "$BACKEND_BIN" ]] || [[ ! -f "$CONFIG_FILE" ]]; then
        step "Backend not built or not configured. Running deploy script..."
        bash "${SCRIPT_DIR}/deploy.sh" --skip-deps
    else
        success "Backend binary and config found, skipping build (use --skip-build to skip this check)"
    fi
fi

# ------------------------------------------------------------------------------
# Step 1: Kill any existing backend
# ------------------------------------------------------------------------------
step "Preparing environment"
pkill -f "nas-backup" 2>/dev/null || true
sleep 1

# Clean test environment
rm -f "${BACKEND_DIR}/data/nas-backup.db"
rm -rf "${PROJECT_ROOT}/test-env/restore-output"/*
mkdir -p "${PROJECT_ROOT}/test-env/restore-output"
success "Cleaned test environment"

# ------------------------------------------------------------------------------
# Step 2: Start backend
# ------------------------------------------------------------------------------
step "Starting backend server"
cd "$BACKEND_DIR"
LOG_FILE="${PROJECT_ROOT}/test-env/backend.log"
./nas-backup > "$LOG_FILE" 2>&1 &
BACKEND_PID=$!
success "Backend started (PID: $BACKEND_PID)"

# Wait for backend to be ready
info "Waiting for backend to be ready..."
MAX_RETRIES=30
RETRY=0
while [[ $RETRY -lt $MAX_RETRIES ]]; do
    if curl -s http://127.0.0.1:8080/api/dashboard/stats > /dev/null 2>&1; then
        success "Backend is ready on http://127.0.0.1:8080"
        break
    fi
    RETRY=$((RETRY + 1))
    sleep 1
done

if [[ $RETRY -eq $MAX_RETRIES ]]; then
    fail "Backend failed to start within ${MAX_RETRIES}s"
    echo "Last 20 lines of log:"
    tail -20 "$LOG_FILE"
    kill $BACKEND_PID 2>/dev/null || true
    exit 1
fi

# ------------------------------------------------------------------------------
# Step 3: Run E2E tests
# ------------------------------------------------------------------------------
step "Running E2E verification tests"
E2E_EXIT_CODE=0
python3 "${SCRIPT_DIR}/verify_e2e.py" || E2E_EXIT_CODE=$?

# ------------------------------------------------------------------------------
# Step 4: Generate HTML report
# ------------------------------------------------------------------------------
step "Generating acceptance report"
python3 "${SCRIPT_DIR}/generate_report.py" || warn "Report generation had issues"

# ------------------------------------------------------------------------------
# Step 5: Stop backend
# ------------------------------------------------------------------------------
step "Stopping backend"
kill $BACKEND_PID 2>/dev/null || true
sleep 1
# Ensure it's dead
pkill -f "nas-backup" 2>/dev/null || true
success "Backend stopped"

# ------------------------------------------------------------------------------
# Summary
# ------------------------------------------------------------------------------
echo ""
echo -e "${BOLD}======================================================================${NC}"
if [[ $E2E_EXIT_CODE -eq 0 ]]; then
    echo -e "${GREEN}${BOLD}  VERIFICATION PASSED - ALL CORE FUNCTIONS WORKING${NC}"
elif [[ $E2E_EXIT_CODE -eq 2 ]]; then
    echo -e "${YELLOW}${BOLD}  VERIFICATION COMPLETED WITH NON-CRITICAL ISSUES${NC}"
else
    echo -e "${RED}${BOLD}  VERIFICATION FAILED - CRITICAL ISSUES FOUND${NC}"
fi
echo -e "${BOLD}======================================================================${NC}"
echo ""
echo "Artifacts:"
echo "  Backend log:  $LOG_FILE"
echo "  JSON report:  ${PROJECT_ROOT}/test-env/e2e-report.json"
echo "  HTML report:  ${PROJECT_ROOT}/test-env/acceptance-report.html"
echo ""

exit $E2E_EXIT_CODE
