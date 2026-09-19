# StonkAgents revenue keeper

Three scheduled jobs that move the money the platform earns: claim the platform trade fee, pay the
holder tax out to holders, and spend a share of the take buying back and burning STONKAGENTS.

What each job does on chain, which wallet signs it, and how to verify a transaction yourself is in
[`docs/keeper.md`](../../docs/keeper.md). This file is the operator's copy: install, run, deploy.

## Install and run

```bash
cd tools/keeper
npm install
cp .env.example .env      # fill in at least LAUNCHPAD_PLATFORM_ID and TRACKER_URL

npm run claim-platform-fees      # dry run: prints a plan, sends nothing
npm run distribute-holder-tax
npm run buyback-and-burn
npm run all                      # all three, in order

npm run claim-platform-fees -- --execute   # sends transactions
npm run all -- --execute
```

Every job is a dry run unless `--execute` is passed. There is no environment variable that turns
execution on: it is always an explicit argument.

| Flag | Jobs | Effect |
| --- | --- | --- |
| `--dry-run` | all | Default. Builds the plan, reads the chain, sends nothing. |
| `--execute` | all | Sends transactions and writes ledger rows. |
| `--mint <mint>` | distribute-holder-tax | Restrict to one token. |
| `--quote-mint <mint>` | claim-platform-fees | Restrict to one quote asset. |
| `--quote-mint <mint>` | distribute-holder-tax | With `--mint`: name the target directly, no tracker call. |
| `--vault-only` | claim-platform-fees | Skip the per-pool fallback sweep. |

When the tracker is unreachable, `claim-platform-fees` still sweeps the vaults of every mint pinned
in `QUOTE_MINTS` (the per-pool fallback is skipped and the plan says so); `buyback-and-burn` cannot
size its budget without `GET /api/revenue` and fails.

Extra commands:

```bash
npm run flush-queue -- --execute   # re-post ledger rows the tracker refused earlier
npm run typecheck
npm test
```

## Safety behaviour

`--execute` aborts before signing anything when:

- `LAUNCHPAD_PLATFORM_ID` is unset, or that account does not exist on `CLUSTER`, or it is not owned
  by the LaunchLab program for that cluster. A mainnet platform id against a devnet RPC stops here.
- A keypair path a job needs is unset, missing, or not a 64-byte solana-keygen JSON array. All
  problems are reported at once.
- A loaded key is not the authority the on-chain platform config names. The fee wallet must equal
  `platformClaimFeeWallet`; the tax authority must equal `transferFeeExtensionAuth`.
- `INTERNAL_API_TOKEN` is unset, so no ledger row could be written for what was about to be sent.

Secrets: keypairs are read from files only, never from an environment variable, so a container
inspect or a process listing cannot leak one. `.env`, `state/` and `node_modules/` are gitignored.

## State directory

`STATE_DIR` (default `./state`, `/data/state` in the image) holds:

| File | Purpose |
| --- | --- |
| `<mint>.json` | Distribution ledger for one token: runs, batches, signatures, carry. |
| `buyback-watermark.json` | Cumulative revenue already counted toward a buyback. |
| `ledger-queue.jsonl` | Rows the tracker refused, retried by `flush-queue`. |

This directory is the idempotency record. Losing it can cause a double payout, so it must live on a
persistent volume and be backed up. Ledgers are stamped with the cluster and refuse to load under a
different one; use a separate `STATE_DIR` per cluster.

## Docker

```bash
docker build -t stonkagents/keeper:latest tools/keeper
docker run --rm --env-file tools/keeper/.env \
  -v "$PWD/tools/keeper/state:/data/state" \
  -v "/secure/keys:/keys:ro" \
  stonkagents/keeper:latest src/jobs/claim-platform-fees.ts --execute
```

The entrypoint is `npx tsx`; the command is the job script plus its flags. With no command it runs
`src/jobs/all.ts` as a dry run.

## ECS scheduled task shape

One task definition, three EventBridge rules that each override the container command.

```
Cluster            stonkagents
Launch type        FARGATE  (0.5 vCPU / 1 GB is enough; the holder scan is RPC bound)
Network            private subnets + NAT, security group egress 443 only
Task role          read the four SSM parameters below, read/write the EFS access point
Execution role     pull from ECR, write to CloudWatch Logs
Log driver         awslogs, group /stonkagents/keeper, stream prefix <job>
Volume             EFS access point mounted at /data  (STATE_DIR=/data/state)
Secrets (SSM)      INTERNAL_API_TOKEN
                   FEE_WALLET_KEYPAIR_PATH content, TRANSFER_FEE_AUTHORITY_KEYPAIR_PATH content,
                   BUYBACK_WALLET_KEYPAIR_PATH content  -> written to /keys/*.json by an init step,
                   or mounted from Secrets Manager; the env vars hold the file paths, not the keys
Environment        CLUSTER, RPC_URL, LAUNCHPAD_PLATFORM_ID, PLATFORM_TOKEN_MINT, TRACKER_URL,
                   BUYBACK_SHARE_BPS, BUYBACK_MODE, DISTRIBUTION_MIN_USD, COMPUTE_UNIT_*
```

| Rule | Schedule (UTC) | Container command |
| --- | --- | --- |
| `keeper-claim` | `cron(0 * * * ? *)` hourly | `src/jobs/claim-platform-fees.ts --execute` |
| `keeper-distribute` | `cron(0 3 * * ? *)` daily | `src/jobs/distribute-holder-tax.ts --execute` |
| `keeper-buyback` | `cron(30 3 * * ? *)` daily | `src/jobs/buyback-and-burn.ts --execute` |

Notes for whoever wires this:

- Do not run two invocations of the same job concurrently. Set the rule's retry policy to 0 attempts
  and give each job its own rule; the on-disk ledger protects against a double payout but a
  concurrent run wastes fees losing the race.
- The buyback rule must run after the claim rule, otherwise it sees no new revenue.
- Alarm on the CloudWatch metric filter `{ $.level = "error" }` over the log group, and on any
  non-zero task exit code.
- A row that never posted stays in `ledger-queue.jsonl`; alarm on a fourth rule running
  `src/jobs/flush-queue.ts` (dry run) whose output is non-empty.
