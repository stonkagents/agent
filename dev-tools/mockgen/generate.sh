#!/usr/bin/env bash
# Mock Generation Script for Claw Sync
# Generates Go mocks using mockgen for all key interfaces.
#
# Usage:
#   bash dev-tools/mockgen/generate.sh
#
# Prerequisites:
#   go install go.uber.org/mock/mockgen@latest
#
# Output:
#   dev-tools/mocks/*.go

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
MOCKS_DIR="$SCRIPT_DIR/../mocks"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

echo "=== Claw Sync Mock Generator ==="
echo ""

# Check mockgen is installed
if ! command -v mockgen &> /dev/null; then
    echo -e "${YELLOW}mockgen not found. Installing...${NC}"
    go install go.uber.org/mock/mockgen@latest
    echo -e "${GREEN}mockgen installed.${NC}"
fi

# Create output directory
mkdir -p "$MOCKS_DIR"

echo "Generating mocks in $MOCKS_DIR"
echo ""

# Track success/failure
FAILED=0

generate_mock() {
    local source_pkg="$1"
    local interface_name="$2"
    local output_file="$3"

    echo -n "  Generating mock for $interface_name... "
    if mockgen \
        -source="$PROJECT_ROOT/$source_pkg" \
        -destination="$MOCKS_DIR/$output_file" \
        -package=mocks \
        2>/dev/null; then
        echo -e "${GREEN}OK${NC}"
    else
        # Fallback: try reflect mode
        local pkg_path
        pkg_path="github.com/stonkagents/agent/$(dirname "$source_pkg")"
        if (cd "$PROJECT_ROOT" && mockgen \
            -destination="$MOCKS_DIR/$output_file" \
            -package=mocks \
            "$pkg_path" \
            "$interface_name") 2>/dev/null; then
            echo -e "${GREEN}OK (reflect mode)${NC}"
        else
            echo -e "${RED}FAILED${NC}"
            FAILED=$((FAILED + 1))
        fi
    fi
}

echo "--- pkg/protocol ---"
generate_mock "pkg/protocol/interfaces.go" "BlockExchangeService" "mock_block_exchange.go"
generate_mock "pkg/protocol/interfaces.go" "ChunkProvider" "mock_chunk_provider.go"

echo ""
echo "--- internal/daemon/storage ---"
generate_mock "internal/daemon/storage/interfaces.go" "ChunkStore" "mock_chunk_store.go"
generate_mock "internal/daemon/storage/interfaces.go" "Chunker" "mock_chunker.go"
generate_mock "internal/daemon/storage/interfaces.go" "DownloadRepository" "mock_download_repository.go"

echo ""
echo "--- internal/daemon/tracker ---"
generate_mock "internal/daemon/tracker/interfaces.go" "TrackerClient" "mock_tracker_client.go"

echo ""
if [ $FAILED -eq 0 ]; then
    echo -e "${GREEN}All mocks generated successfully.${NC}"
    echo ""
    echo "Generated files:"
    ls -la "$MOCKS_DIR"/*.go 2>/dev/null || echo "  (no files)"
else
    echo -e "${RED}$FAILED mock(s) failed to generate.${NC}"
    echo "Check that all interface files compile and mockgen is up to date."
    exit 1
fi
