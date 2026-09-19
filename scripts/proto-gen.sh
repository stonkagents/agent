#!/bin/bash
# From repo root. Requires protoc and protoc-gen-go (go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.11).
set -e
# Use module= so output goes to pkg/manifest/ and descriptor matches proto package (StonkAgents.v1)
protoc --go_out=. --go_opt=module=github.com/stonkagents/agent api/proto/asset_manifest.proto
protoc --go_out=. --go_opt=module=github.com/stonkagents/agent api/proto/block_exchange.proto
