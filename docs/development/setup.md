# Development Setup

## Prerequisites

- Go 1.25.6+
- Protocol Buffers compiler (protoc)
- golangci-lint

## Quick Start

```bash
# Install dependencies
go mod download

# Generate Protocol Buffer code
make proto-gen

# Build binaries
make build

# Run tests
make test
```

## Project Structure

See MVP Design document for full architecture.
