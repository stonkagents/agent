# VM seeder operations

Operational notes for **P2P-only** availability with always-on guardian seeders. The legacy ECS + S3 hybrid (replication worker + presigned URLs) has been **removed** from the tracker.

## Metrics (source of truth)

- `at_download_route_total{mode="p2p|no_peers"}`:
  - `no_peers / (p2p + no_peers)` = share of requests where **no** online peer had the asset (investigate replicator lag, seeder downtime, or brand-new announces).
- `at_api_errors_total` for `503` on `GET /api/v1/tracker/assets/{cid}/download` — correlates with `no_peers`.

## Suggested SLOs

- Download route: successful `200` with `mode=p2p` when at least one seeder has finished replicating the CID.
- Seeder catch-up: most new assets show guardian peer(s) in `GET /api/v1/tracker/assets/{cid}/peers` within minutes (tune replicator poll interval and daemon capacity).

## Alert thresholds

- **Warning**: sustained elevation of `no_peers` rate vs baseline (e.g. seeder disk full, replicator stopped, or network partition).
- **Critical**: guardian host down or daemon API unreachable.

## Rollout checklist

1. Tracker deployed **without** `S3_REPLICATION_BUCKET` / `S3_SIGNED_URL_TTL_SECONDS`.
2. PostgreSQL reachable; migrations applied on tracker startup.
3. One or more **guardian** `sync-daemon` processes running with correct `tracker_url` and reachable libp2p addresses.
4. **stonkagents-replicator** running with correct daemon endpoint list.
5. Canary: announce from a laptop → confirm seeder appears in `/assets/{cid}/peers` → stop laptop daemon → confirm another client can still download.

## Rollback

Rollback is operational: restore previous tracker binary and processes if you maintained a backup deployment. There is **no** S3 fallback to re-enable in application code.
