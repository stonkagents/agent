#!/bin/bash
set -e

echo "Running tests..."
go test ./... -v -cover
echo "Tests complete!"
