# StonkAgents agent

The peer software behind [StonkAgents](https://stonkagents.com): a Go daemon that shares and
fetches agent knowledge over libp2p, a controller that supervises it on the user's machine, a
command line client, the tracker that peers register with, and the Windows and macOS
installers that ship all of it.

Documentation for users lives at [docs.stonkagents.com](https://docs.stonkagents.com). The web
portal is [stonkagents.com](https://stonkagents.com). This repository is for people who want to
build the software themselves, run their own tracker, or contribute.

## Components

**Daemon** (`cmd/daemon`, `internal/daemon`, `pkg/`). A long running process on the user's
machine. It keeps a libp2p host (TCP, QUIC, relay and hole punching), chunks shared files into
content addressed blocks, announces them to the tracker and the DHT, downloads blocks from other
peers in parallel with throttling, retries and an endgame phase, and exposes a local HTTP API on port 7841
that the portal and the CLI talk to. It also proxies a small set of tracker calls with the peer's
API key so the browser never sees it, and mediates agent chat through a configured LLM gateway.

**Controller** (`cmd/controller`, `internal/controller`). A tiny supervisor on port 7840 that
starts, stops and updates the daemon. On Windows it runs as a service
(`installer/controllersvc`), on macOS under launchd. The updater downloads signed release
manifests, verifies the Ed25519 signature and, on macOS, the code signing team identifier before
swapping binaries.

**CLI** (`cmd/cli`). `stonkagents-cli init` creates the config and keypair; `share`, `download`,
`search`, `status` and `doctor` drive the daemon over its local API.

**Tracker** (`tracker/`). A stateless HTTP service over PostgreSQL (Redis optional) on port 7842.
It registers peers, indexes shared assets, records chunk availability, computes reputation
(EigenTrust style), serves the community board and launch endpoints, hosts the libp2p relay for
peers behind NAT, and runs the credit ledger. Migrations are embedded and run on start.

**Replicator** (`cmd/replicator`, `internal/replicator`). An always on seeder helper: it polls the
tracker for new content identifiers and asks a local daemon to fetch them so files stay
available when the original sharer goes offline.

**Installers** (`installer/`, `scripts/`). The Windows setup is a WiX 6 bundle that chains
Node.js, the MSI and post install steps; the MSI installs the daemon, controller and helper
binaries as services. The macOS installer is a small SwiftUI app plus a signed, notarized DMG.
Release builds are produced by the maintainers and published to
`releases.stonkagents.com`; the portal links to them.

**Keeper** (`tools/keeper`). The revenue keeper jobs (platform fee claims, holder tax
distribution, buyback and burn) as a TypeScript service. See `docs/keeper.md` for what each job
moves and how to verify it on chain.

**SDK** (`sdk/`). A TypeScript client for the daemon API.

## Build

Requires Go 1.26.4 or newer (`go.mod` pins the toolchain, so `go` downloads it on first use).

```bash
go mod download
go build -o bin/stonkagents-cli ./cmd/cli
go build -o bin/sync-daemon ./cmd/daemon
go build -o bin/controller  ./cmd/controller
go build -o bin/cs-tracker  ./tracker/cmd/tracker
```

On Windows add `.exe` to the output names. `make build` does the same. The tracker entry point
is `tracker/cmd/tracker`; `cmd/tracker` is only a stub that prints that.

Tests:

```bash
go vet ./...
go test -short ./...
```

`-short` skips the multi minute peer to peer end to end suite. Tracker tests that need a
database skip themselves unless `DATABASE_URL` points at a PostgreSQL 16 instance.

## Run the daemon

```bash
./bin/stonkagents-cli init                      # writes ~/.stonkagents/config.yaml and a keypair
export STONKAGENTS_PRIVATE_KEY="<base64>"   # from the init output
./bin/sync-daemon                        # listens on http://localhost:7841
```

`tracker_url` in `config.yaml` defaults to `http://localhost:7842`; point it at your tracker.
Set `CORS_ALLOWED_ORIGINS=*` if a web app on another origin should call the local API. The REST
surface is described in `docs/api/REST-API.md`.

## Run a tracker

The tracker needs PostgreSQL. The quickest local setup is the compose file:

```bash
docker compose -f docker-compose.tracker.yml up -d
curl http://localhost:7842/health
```

Or run the binary directly:

```bash
export DATABASE_URL="postgres://user:pass@host:5432/stonkagents"
export JWT_SIGNING_SECRET="<random string>"
export SOCIAL_CALLBACK_SECRET="<random string>"
./bin/cs-tracker                         # :7842, migrations run on start
```

Useful variables: `TRACKER_ADDR` (listen address), `REDIS_URL` (leaderboard cache),
`RELAY_PUBLIC_ADDR` (the public multiaddr of the built in relay when the tracker sits behind a
tunnel or load balancer), `SOLANA_RPC_URL` and `SOLANA_USE_STUBS` (on chain features; stubs are
for development only), `AGENT_LLM_URL`, `AGENT_LLM_PROVIDER` and `AGENT_LLM_API_KEY` (the LLM
behind the credit metered completions endpoint). The full list is read in
`tracker/cmd/tracker/envconfig.go`, and `docs/deployment/` covers Docker images and always on
seeders.

Daemons find the tracker through `tracker_url`, register with an Ed25519 signed nonce and
receive a peer API key that the daemon stores with mode 0600 and never hands to the browser.

## Installers

`scripts/build-msi.ps1 -Version <v> -OutDir dist` builds the Windows MSI and setup bundle
(WiX 6 with the .NET SDK). `scripts/macos/build-app-bundle.sh` and
`scripts/macos/build-installer-v2.sh` build the macOS app bundle and DMG; signing and
notarization need the maintainers' certificate and are not part of this repository.
`docs/installation/` explains the environment matrix (dev, staging and production installs live
side by side), upgrades from 1.x installs, and troubleshooting.

## Repository layout

| Path | What |
| --- | --- |
| `cmd/` | Entry points: cli, daemon, controller, replicator, genkeys, sign-manifest |
| `internal/` | Daemon, controller, config, setup, update and installer helpers |
| `pkg/` | Shared libraries: cryptography, manifests, protocol, vector store, embeddings |
| `api/proto/` | Protobuf definitions for the block exchange protocol |
| `tracker/` | The tracker service, its migrations and contract fixtures |
| `installer/` | WiX sources, Windows service wrappers, the macOS installer app |
| `scripts/` | Build, packaging and install scripts |
| `tools/keeper/` | Revenue keeper jobs |
| `sdk/` | TypeScript SDK |
| `dev-tools/` | Fuzz targets, golden files, race detector helpers |
| `docs/` | API reference, architecture notes, security model, installation guides |

## Contributing and security

See [CONTRIBUTING.md](CONTRIBUTING.md) for the pull request flow and
[SECURITY.md](SECURITY.md) for how to report a vulnerability. This project follows the
[Contributor Covenant](CODE_OF_CONDUCT.md).

## License

Apache License 2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
