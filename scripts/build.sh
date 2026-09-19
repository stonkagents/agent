#!/bin/bash
set -e

VERSION="${1:-dev}"
LDFLAGS="-X main.Version=${VERSION}"

echo "Building StonkAgents binaries (version: ${VERSION})..."
go build -ldflags="$LDFLAGS" -o bin/stonkagents-cli cmd/cli/main.go
go build -ldflags="$LDFLAGS" -o bin/sync-daemon cmd/daemon/main.go
go build -ldflags="$LDFLAGS" -o bin/cs-tracker tracker/cmd/tracker/main.go
echo "Build complete!"
