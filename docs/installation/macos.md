# macOS Installation

Install StonkAgents on macOS via the signed, notarized DMG.

## Install from DMG (recommended)

1. Download `StonkAgents-<version>.dmg` (or build with `./scripts/macos/sign-release.sh <version>`)
2. Open the DMG and double-click **Install StonkAgents**
3. The installer copies binaries, generates keys, and starts services automatically

The installer will:

- Copy `stonkagents`, `genkeys`, and `stonkagents-controller` to `~/.local/bin/`
- Create `~/.stonkagents/` with `config.yaml` and `secrets.env` (generates Ed25519 keys if missing)
- Register **two launchd services**: daemon (`com.stonkagents.daemon`, `:7841`) and controller (`com.stonkagents.controller`, `:7840`)
- Set `chmod 700` on config and data directories

### Upgrade from previous version

Opening a new DMG while the daemon is already running will:
- Compare bundled binaries against installed ones
- Copy only changed binaries (via `cmp -s` comparison)
- Restart services if binaries were updated
- Show "Already running" notification if no update needed


## Build DMG locally

Requires macOS with Xcode Command Line Tools, Go, Swift, and a Developer ID certificate.

```bash
cd agent
./scripts/macos/sign-release.sh 0.4.0           # production build
./scripts/macos/sign-release.sh 0.4.0-dev        # dev/staging build
```

Output: `dist/macos/prod/StonkAgents-0.4.0.dmg` (or `dist/macos/dev/` for dev builds).

See the release runbook in `docs/development/release-runbook.md` for the full build, sign, and notarize process.

---

## Paths

| Path | Description |
|------|-------------|
| `~/.local/bin/stonkagents` | Daemon binary |
| `~/.local/bin/stonkagents-controller` | Controller binary |
| `/Applications/StonkAgents.app` | App bundle (launcher) |
| `~/Library/LaunchAgents/com.stonkagents.daemon.plist` | Daemon launchd agent |
| `~/Library/LaunchAgents/com.stonkagents.controller.plist` | Controller launchd agent |
| `~/.local/bin/genkeys` | Key generation tool |
| `~/.stonkagents/config.yaml` | Configuration |
| `~/.stonkagents/secrets.env` | Secrets (private key, `chmod 600`) |
| `~/.stonkagents/data/` | Data directory (downloads, chunks) |
| `~/.stonkagents/logs/` | Log files |

---

## Service commands

| Action | Command |
|--------|---------|
| Status | `launchctl list \| grep stonkagents` |
| Start daemon | `launchctl load ~/Library/LaunchAgents/com.stonkagents.daemon.plist` |
| Stop daemon | `launchctl unload ~/Library/LaunchAgents/com.stonkagents.daemon.plist` |
| Start controller | `launchctl load ~/Library/LaunchAgents/com.stonkagents.controller.plist` |
| Stop controller | `launchctl unload ~/Library/LaunchAgents/com.stonkagents.controller.plist` |
| Daemon health | `curl http://localhost:7841/health` |
| Daemon status | `curl http://localhost:7841/api/v1/status` |
| Controller status | `curl http://localhost:7840/api/v1/controller/update/status` |
| Daemon logs | `tail -f ~/.stonkagents/logs/daemon.stderr.log` |
| Controller logs | `tail -f ~/.stonkagents/logs/controller.stderr.log` |

---

## Uninstall

```bash
launchctl unload ~/Library/LaunchAgents/com.stonkagents.daemon.plist
launchctl unload ~/Library/LaunchAgents/com.stonkagents.controller.plist
rm -f ~/Library/LaunchAgents/com.stonkagents.daemon.plist
rm -f ~/Library/LaunchAgents/com.stonkagents.controller.plist
rm -f ~/.local/bin/stonkagents ~/.local/bin/stonkagents-controller ~/.local/bin/genkeys
rm -rf /Applications/StonkAgents.app
rm -rf ~/.stonkagents
```


---

## Troubleshooting

- **Daemon won't start:** Check `~/.stonkagents/logs/daemon.stderr.log`. Verify `secrets.env` has `STONKAGENTS_PRIVATE_KEY`.
- **Health not responding:** Ensure port 7841 is free. Wait a few seconds after `launchctl load`.
- **Controller not responding:** Check port 7840. Controller manages daemon start/stop.
- **Gatekeeper warning:** DMG must be notarized. Build with `sign-release.sh` (not `build-installer.sh` alone).
- **Universal binary:** DMG ships a single binary for both Intel and Apple Silicon via `lipo`.
