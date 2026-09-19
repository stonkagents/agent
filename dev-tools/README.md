# Dev Tools

Testing utilities for StonkAgents. Fully isolated — deleting this folder leaves the project unchanged.

## Quick Reference

| Tool               | What it catches                 | Run command                                                         |
| ------------------ | ------------------------------- | ------------------------------------------------------------------- |
| **Fuzz Tests**     | Parsing crashes, roundtrip bugs | `cd dev-tools/fuzz && go test -fuzz=FuzzCIDRoundtrip -fuzztime=30s` |
| **Golden Files**   | API response shape regressions  | `cd dev-tools/golden && go test -v`                                 |
| **Mock Generator** | Stale mocks vs interfaces       | `bash dev-tools/mockgen/generate.sh`                                |
| **Race Detector**  | Goroutine data races            | `bash dev-tools/race/run.sh`                                        |

---

## Fuzz Tests

Location: `dev-tools/fuzz/`

Go native fuzz tests for security-critical parsing functions. Each test feeds random inputs to functions that process untrusted data (CIDs, signatures, protobuf manifests, embeddings).

### Setup (first time)

```bash
cd dev-tools/fuzz
go mod tidy
```

### Run a specific fuzz target

```bash
cd dev-tools/fuzz
go test -fuzz=FuzzCIDRoundtrip -fuzztime=30s
```

### Available fuzz targets

**CID (cid_fuzz_test.go):**

- `FuzzCIDRoundtrip` — GenerateCID -> CIDToBytes -> CIDFromBytes consistency
- `FuzzVerifyCID` — CID verification with matching/non-matching data
- `FuzzCIDFromBytes` — Arbitrary bytes -> CID parsing (crash detection)
- `FuzzCIDToBytes` — Arbitrary strings -> CID parsing (crash detection)

**Signatures (signature_fuzz_test.go):**

- `FuzzVerifySignature` — Malformed keys/signatures don't panic
- `FuzzSignDataRoundtrip` — Sign -> Verify always succeeds
- `FuzzSignDataTamper` — Tampered data always fails verification

**Manifests (manifest_fuzz_test.go):**

- `FuzzDecodeAtRawManifest` — Arbitrary bytes -> protobuf decode (crash detection)
- `FuzzDecodeAtVecManifest` — Same for vector manifests
- `FuzzValidateAtRawManifest` — Validation with fuzzed field values
- `FuzzValidateAtVecManifest` — Same for vector manifests

**Spot-checks (spotcheck_fuzz_test.go):**

- `FuzzCosineSimilarity` — Random vectors don't crash or produce out-of-range values
- `FuzzBytesToFloat32Slice` — Arbitrary bytes -> float32 conversion
- `FuzzCheckEmbeddingAnomaly` — Anomaly detection with various dimensions

### Run all fuzz targets (10s each, smoke test)

```bash
cd dev-tools/fuzz
for target in $(go test -list 'Fuzz.*' . 2>/dev/null | grep '^Fuzz'); do
  echo "--- $target ---"
  go test -fuzz="$target" -fuzztime=10s -run='^$' .
done
```

### Interpreting results

- **PASS** — No crashes found in the time budget
- **FAIL with crash** — A crashing input was found. The failing input is saved to `testdata/fuzz/<FuncName>/` for reproducibility. Fix the code and re-run.

---

## Golden File Tests

Location: `dev-tools/golden/`

Snapshot tests for daemon API response shapes. Captures JSON responses and compares against committed `.golden` files to detect unintentional API changes.

### Setup (first time)

```bash
cd dev-tools/golden
go mod tidy
```

### Generate golden files (first time or after intentional changes)

```bash
cd dev-tools/golden
GOLDEN_UPDATE=1 go test -v
```

This creates `testdata/*.golden` files. Review the contents and commit them.

### Run comparison tests

```bash
cd dev-tools/golden
go test -v
```

### When tests fail

A failure means the API response shape has changed since the golden files were last updated. Either:

1. **Unintentional change** — Fix the code to restore the original response shape.
2. **Intentional change** — Re-run with `GOLDEN_UPDATE=1`, review the diff, and commit the updated golden files.

### Covered endpoints

- `GET /health` — Health check response
- `GET /api/v1/status` — Daemon status response
- `GET /api/v1/search` — Empty search results
- `POST /api/v1/share` — Share success response
- `POST /api/v1/download` — Download queued response
- Error responses: unauthorized, forbidden, validation errors

---

## Mock Generator

Location: `dev-tools/mockgen/`

Generates Go mocks for all key interfaces using `mockgen` (from `go.uber.org/mock`). Mocks are output to `dev-tools/mocks/` — not into the main source tree.

### Prerequisites

```bash
go install go.uber.org/mock/mockgen@latest
```

### Run

```bash
bash dev-tools/mockgen/generate.sh
```

### Generated mocks

| Interface              | Source                                  | Output                              |
| ---------------------- | --------------------------------------- | ----------------------------------- |
| `BlockExchangeService` | `pkg/protocol/interfaces.go`            | `mocks/mock_block_exchange.go`      |
| `ChunkProvider`        | `pkg/protocol/interfaces.go`            | `mocks/mock_chunk_provider.go`      |
| `ChunkStore`           | `internal/daemon/storage/interfaces.go` | `mocks/mock_chunk_store.go`         |
| `Chunker`              | `internal/daemon/storage/interfaces.go` | `mocks/mock_chunker.go`             |
| `DownloadRepository`   | `internal/daemon/storage/interfaces.go` | `mocks/mock_download_repository.go` |
| `TrackerClient`        | `internal/daemon/tracker/interfaces.go` | `mocks/mock_tracker_client.go`      |

### When to re-run

Run the generator whenever you change an interface signature. If a mock is stale, tests using it will fail to compile.

---

## Race Detector

Location: `dev-tools/race/`

Runs all Go tests with `-race` flag to detect goroutine data races. Critical for P2P code with concurrent goroutines, channels, and shared state.

### Run all tests

```bash
bash dev-tools/race/run.sh
```

### Run specific packages

```bash
bash dev-tools/race/run.sh ./internal/daemon/...
```

### Skip E2E tests (faster)

```bash
bash dev-tools/race/run.sh -short
```

### Interpreting results

- **NO DATA RACES DETECTED** — All clear.
- **DATA RACE(S) DETECTED: N** — The output shows the exact goroutine stacks involved. Fix the race before merging.

Race detection adds ~2-10x overhead to test execution time. Use `-short` for faster feedback during development.

---

## CI Integration

File: `.github/workflows/dev-tools-ci.yml`

A separate GitHub Actions workflow that runs on pull requests only. It does NOT modify or depend on the existing `ci.yml`.

**Jobs:**

- `race-detection` — Runs `go test -race -short ./...`
- `fuzz-smoke` — Runs each fuzz target for 10 seconds as a smoke test

To disable: delete `.github/workflows/dev-tools-ci.yml`. No other files need to change.

---

## Architecture

```
dev-tools/                  # Fully isolated, deletable
├── README.md               # This file
├── fuzz/                   # Go fuzz tests (separate go.mod)
│   ├── go.mod              # replace directive → ../../
│   ├── cid_fuzz_test.go
│   ├── manifest_fuzz_test.go
│   ├── signature_fuzz_test.go
│   └── spotcheck_fuzz_test.go
├── golden/                 # API snapshot tests (separate go.mod)
│   ├── go.mod              # replace directive → ../../
│   ├── api_golden_test.go
│   └── testdata/           # .golden files (committed)
├── mockgen/
│   └── generate.sh         # Mock generation script
├── mocks/                  # Generated mocks (gitignored or committed)
└── race/
    └── run.sh              # Race detection runner

.github/workflows/
└── dev-tools-ci.yml        # Separate CI workflow (deletable)
```

Each `go.mod` uses a `replace` directive to import from the parent project without publishing:

```
replace github.com/stonkagents/agent => ../..
```
