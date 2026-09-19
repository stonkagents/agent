# VM guardians (always-on seeders)

Deploy **sync-daemon** on one or more Linux VMs (bare metal, cloud VM, or a Linux guest in VMware/VirtualBox on Windows) so files stay available over **P2P** when the original uploader goes offline. The tracker does **not** serve object-store or signed HTTP fallbacks; availability depends on **peers**, including these seeders.

## Role

- **Seeder**: Guardian daemons register with the tracker, hold replicated content, and answer chunk requests via libp2p.
- **stonkagents-replicator**: Polls `GET /api/v1/tracker/assets/recent` and queues downloads on local daemon(s) so new CIDs are pulled quickly after announce.

## Builds

From repo root:

```bash
go build -o bin/sync-daemon cmd/daemon/main.go
go build -o bin/stonkagents-replicator cmd/replicator/main.go
go build -o bin/cs-tracker tracker/cmd/tracker/main.go
```

## Configuration

1. **Tracker** (same host or another VM): set `DATABASE_URL`, run `cs-tracker`. Do not set any `S3_*` replication env vars (not used).
2. **Each guardian `sync-daemon`**: set `tracker_url` in `~/.stonkagents/config.yaml` to the **reachable** tracker URL (HTTPS or LAN), **not** `localhost` from a different machine.
3. **stonkagents-replicator** (often on guardian #1): point daemon endpoints at the HTTP API of each guardian daemon (e.g. `http://127.0.0.1:7841` only if replicator and daemon run on the **same** VM; otherwise use the other VM’s IP).

See [Guardian replication deployment](guardian-replication.md) for replicator env defaults, storage limits, and validation (announce → seeder in peer list → stop original sharer → download still works).

### VM launcher script (external, not in the daemon EXE)

Use the shell wrapper so the replicator is a normal long-running process under systemd or `screen`/`tmux`:

- [`scripts/vm/run-replicator.sh`](../../scripts/vm/run-replicator.sh) — sources an env file, checks required vars, `exec`s the `stonkagents-replicator` binary.
- [`scripts/vm/replicator.vm.env.example`](../../scripts/vm/replicator.vm.env.example) — copy to `replicator.vm.env` (same directory or set `REPLICATOR_ENV_FILE`).

On the VM: `chmod +x scripts/vm/run-replicator.sh`, build `bin/stonkagents-replicator`, then run the script from that tree or set `REPLICATOR_BIN`.

The replicator logs **Info** on stdout each poll (`poll complete`, `recent_assets`, `watermark_utc`) and when a guardian finishes a download. For readable lines instead of JSON, set `REPLICATOR_LOG_FORMAT=text` in your env file.

## VMware / Windows host networking

- **`localhost` is per OS**: the Linux guest’s `127.0.0.1` is not the Windows host’s loopback.
- Prefer **bridged** networking so the guest gets a LAN IP, or **NAT + port forwarding** for tracker and libp2p ports.
- Clients and other daemons must use the guest’s **real hostname/IP** when calling the tracker or connecting to peers.

## Related

- Operations and SLOs: [VM seeder operations](vm-seeder-operations.md)
- AWS EC2 variant (optional): Terraform `infra/guardian.tf` — same software, different provisioning.
