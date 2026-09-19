#!/usr/bin/env bash
# Race Detection Runner for StonkAgents
# Runs all Go tests with the -race flag to detect data races in concurrent code.
#
# Usage:
#   bash dev-tools/race/run.sh                        # Run all tests with race detection
#   bash dev-tools/race/run.sh ./pkg/cryptography/... # Run specific package(s)
#   bash dev-tools/race/run.sh --short                # Run short tests only (skip E2E)
#   bash dev-tools/race/run.sh --short ./pkg/...      # Combine flags and packages
#
# Exit codes:
#   0 — No races detected
#   1 — Race(s) detected or test failure
#   2 — Build/setup error

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

echo "=== StonkAgents Race Detector ==="
echo ""
echo -e "${CYAN}Project:${NC} $PROJECT_ROOT"
echo -e "${CYAN}Go:${NC}      $(go version)"
echo ""

# Parse arguments: separate flags from package paths
SHORT_FLAG=""
PACKAGES=""

for arg in "$@"; do
    case "$arg" in
        -short|--short)
            SHORT_FLAG="-short"
            echo -e "${YELLOW}Running in short mode (E2E tests skipped)${NC}"
            ;;
        *)
            PACKAGES="$arg"
            ;;
    esac
done

# Default packages if none specified
PACKAGES="${PACKAGES:-./...}"

echo -e "Running: ${CYAN}go test -race $SHORT_FLAG -count=1 -timeout=300s $PACKAGES${NC}"
echo ""

cd "$PROJECT_ROOT"

# Capture output and exit code
set +e
OUTPUT=$(go test -race $SHORT_FLAG -count=1 -timeout=300s $PACKAGES 2>&1)
EXIT_CODE=$?
set -e

echo "$OUTPUT"
echo ""

# Check for race detection warnings
if echo "$OUTPUT" | grep -q "WARNING: DATA RACE"; then
    RACE_COUNT=$(echo "$OUTPUT" | grep -c "WARNING: DATA RACE" || true)
    echo ""
    echo -e "${RED}========================================${NC}"
    echo -e "${RED}  DATA RACE(S) DETECTED: $RACE_COUNT   ${NC}"
    echo -e "${RED}========================================${NC}"
    echo ""
    echo "Fix the races above before merging."
    exit 1
elif [ $EXIT_CODE -ne 0 ]; then
    echo ""
    echo -e "${RED}Tests failed (exit code $EXIT_CODE) but no races detected.${NC}"
    exit $EXIT_CODE
else
    echo ""
    echo -e "${GREEN}========================================${NC}"
    echo -e "${GREEN}  NO DATA RACES DETECTED               ${NC}"
    echo -e "${GREEN}========================================${NC}"
    exit 0
fi
