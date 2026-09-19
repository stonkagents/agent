# The StonkAgents revenue keeper

Every trade on a StonkAgents token pays 2.25%. This document explains where each part of that goes,
which program instruction moves it, which wallet signs, and how to check any of it yourself with a
block explorer and no trust in us.

The code is in [`tools/keeper/`](../tools/keeper). It is a small TypeScript service with no
framework, three jobs, and an on-disk ledger that makes a rerun safe.

## The split

| Slice | Rate | Where it goes |
| --- | --- | --- |
| Raydium protocol fee | 0.25% | Raydium. Not ours, not touchable by us. |
| StonkAgents platform fee | 1.00% | Claimed to the fee wallet, then **50% buyback and burn, 50% operations**. |
| Holder tax | 1.00% | Withheld by Token-2022 on every transfer, then **100% paid back to holders** in the pool's quote asset. |
| Creator fee | 0% | We set the on-chain creator fee to zero. |

The 50/50 platform split is `BUYBACK_SHARE_BPS`, default `5000`. It is read from configuration at
run time and printed in every plan, so a change is visible in the logs the moment it happens.

Launch fees (Solana rent plus about $0.50 in SOL, priced live) go to the treasury and are counted
alongside the platform fee when the buyback budget is worked out.

## What runs when

Three jobs, each a separate scheduled task. Every one of them is a dry run unless it is given
`--execute`.

| Job | Cadence | What it does |
| --- | --- | --- |
| `claim-platform-fees` | Hourly | Moves the accrued 1% platform fee out of Raydium into the fee wallet. |
| `distribute-holder-tax` | Daily | Sweeps the withheld 1% transfer tax, swaps it to the quote asset, pays holders pro rata. |
| `buyback-and-burn` | Daily, after the claim | Spends 50% of new revenue on STONKAGENTS and burns it. |

Every action any of them takes is written to the public revenue ledger and shown on the revenue
page. Each row carries the transaction signature, so every claim, payout, buy and burn on that page
links to something you can open on Solscan.

## The wallets

None of these are contracts. They are ordinary Solana accounts, and their roles are set in our
on-chain LaunchLab platform config, which anyone can decode.

| Wallet | Set in | Used for |
| --- | --- | --- |
| Platform admin | PDA seed of the platform config | Nothing routine. It only exists to update the config. Cold storage. |
| Fee wallet | `platformClaimFeeWallet` | Signs the fee claim and receives the 1%. |
| Transfer-fee authority | `transferFeeExtensionAuth` | Signs the withdrawal of withheld holder tax. |
| Distribution wallet | keeper configuration | Holds the withdrawn tax, swaps it, pays holders. Defaults to the transfer-fee authority. |
| Buyback wallet | keeper configuration | Buys STONKAGENTS and burns it. |
| LP NFT wallet | `platformLockNftWallet` | Receives the locked-LP fee key when a token graduates. |

To read the roles for yourself, decode the platform config account. `LAUNCHPAD_PLATFORM_ID` is
published in the tracker's launch config response (`GET /api/launch/config`), and
`scripts/launchlab-platform` has a `npm run verify -- <platform id>` command that prints every field.

## Job 1 — claim-platform-fees

The 1% platform fee accrues in a per-platform vault, one per quote asset: a program-derived address
seeded with our platform config id and the quote mint. Older pools may still hold an unclaimed
amount on the pool account itself.

Instruction sequence, per quote asset:

1. `AssociatedTokenProgram CreateIdempotent` — the fee wallet's token account for that quote asset,
   a no-op when it already exists.
2. `LaunchLab claim_platform_fee_from_vault` — accounts in order: fee wallet (signer), the fee-vault
   authority PDA, our platform config, the platform vault PDA, the fee wallet's token account, the
   quote mint, the quote asset's token program.

Fallback, per pool that still carries an unclaimed balance:

1. `AssociatedTokenProgram CreateIdempotent`
2. `LaunchLab claim_platform_fee` — fee wallet (signer), the LaunchLab authority PDA, the pool, our
   platform config, the pool's quote vault, the fee wallet's token account, the quote mint, the
   token program.

Skipped when the vault holds less than `CLAIM_MIN_USD` (default $5), because the transaction fee
would eat the claim. A quote asset Jupiter has no price for is claimed anyway rather than stranded.

Ledger rows: one `platform_fee_claim` per claim, with the amount actually received (measured as the
change in the fee wallet's balance, not the amount we expected).

**Verify it.** Open the signature on Solscan. You should see exactly one LaunchLab instruction, an
inner transfer from the platform vault to the fee wallet, and a token balance change on the fee
wallet equal to `amount_raw` on the revenue page.

## Job 2 — distribute-holder-tax

Every StonkAgents token is a Token-2022 mint with a 1% transfer fee. The fee is not sent anywhere at
transfer time: it is withheld inside the recipient's own token account. Collecting it means
withdrawing it from those accounts, which only the transfer-fee authority can do.

Instruction sequence, per token:

1. `AssociatedTokenProgram CreateIdempotent` — the distribution wallet's account for the token.
2. `TransferFeeExtension WithdrawWithheldTokensFromAccounts`, in batches of `WITHDRAW_BATCH_SIZE`
   (default 20) source accounts, signed by the transfer-fee authority.
3. `TransferFeeExtension HarvestWithheldTokensToMint` — only for a batch that failed, for example
   because one source account was frozen. Harvest is permissionless and pushes the withheld amount
   to the mint instead.
4. `TransferFeeExtension WithdrawWithheldTokensFromMint` — collects whatever reached the mint.
5. A Jupiter swap, token to quote asset, signed by the distribution wallet.
6. Per payout batch: `AssociatedTokenProgram CreateIdempotent` for any recipient that has no account
   for the quote asset yet, then one `TransferChecked` per recipient.

### Who counts as a holder

A snapshot is taken from the chain at the moment of the run: every token account of that mint, read
directly from the Token-2022 program. Balances are summed per owner, so one wallet with three token
accounts is one recipient paid once.

Left out:

- the bonding-curve pool, its two vaults and the pool creator account,
- the mint itself,
- accounts owned by a program rather than a person (an address off the ed25519 curve),
- anything listed in `HOLDER_EXCLUDE`,
- balances below `DISTRIBUTION_DUST_UNITS` (default 1 whole token).

### How much each holder gets

`payout = floor(distributable × balance ÷ total eligible balance)`

Always floored, never rounded up, so the sum of payouts can never exceed what was actually
collected. The leftover from flooring is carried in the ledger and added to the next run; it is not
kept. A holder whose share floors to zero keeps their weight for the next run instead of receiving
an empty transfer.

The whole run is skipped when the withheld tax is worth less than `DISTRIBUTION_MIN_USD` (default
$25), so a distribution never costs more in fees than it delivers.

Ledger rows: one `holder_distribution` per batch, whose meta carries `amountPerUnit` (quote units
per token unit for that run) and the recipient list with each amount.

**Verify it.** Take any `holder_distribution` signature: the transaction shows one `TransferChecked`
per recipient from the distribution wallet. Divide the amount you received by your balance at the
snapshot and compare it against `amountPerUnit` in the row's meta; every recipient in the same run
has the same figure.

**Reruns cannot pay twice.** `state/<mint>.json` records each run, each batch, its recipients and
its signature, written before the transaction is sent and again after it confirms. A rerun resumes
the open run, checks every recorded signature on chain, and never rebuilds a batch that landed.

## Job 3 — buyback-and-burn

New revenue since the last buyback is measured as the growth of the public revenue ledger: the
`launch_fee` total in lamports (priced in SOL at run time) plus the `platform_fee_claim` total in
USD. `BUYBACK_SHARE_BPS` of that sum is the budget. A ledger that shrank yields a budget of zero, so
a restored backup cannot conjure a buyback.

Instruction sequence:

1. `AssociatedTokenProgram CreateIdempotent` — the buyback wallet's STONKAGENTS account.
2. A Jupiter swap, quote asset to STONKAGENTS, signed by the buyback wallet.
3. `TokenProgram BurnChecked` — the tokens just bought, destroyed. Skipped when `BUYBACK_MODE=lock`,
   which keeps them in the buyback wallet instead.

The run is skipped when the budget is below `BUYBACK_MIN_USD` (default $25), and skipped with a
clear log line when `PLATFORM_TOKEN_MINT` is not configured, which is the case until STONKAGENTS
itself has launched.

If the buyback wallet cannot cover the whole budget, the watermark advances only by the share that
was actually spent. The rest stays eligible for the next run rather than being written off.

Ledger rows: one `buyback` per swap (with the tokens received in its meta) and one `burn` for the
burn.

**Verify it.** The `burn` signature shows a `BurnChecked` instruction, and the STONKAGENTS total
supply drops by exactly that amount. Compare the sum of `burn` rows against the mint's supply
history; nothing we do can move a burned token.

## Thresholds and settings

Every one of these is configuration, not a constant in the code, and every job prints its values.

| Setting | Default | Meaning |
| --- | --- | --- |
| `BUYBACK_SHARE_BPS` | 5000 | Share of new revenue spent on buyback. The rest funds operations. |
| `BUYBACK_MODE` | burn | `burn` destroys what was bought; `lock` holds it. |
| `BUYBACK_MIN_USD` | 25 | Below this a buyback run does nothing. |
| `DISTRIBUTION_MIN_USD` | 25 | Below this a token's tax is left to accrue. |
| `DISTRIBUTION_DUST_UNITS` | 1 | Holders below this many tokens are not paid. |
| `DISTRIBUTION_BATCH_SIZE` | 10 | Recipients per payout transaction. |
| `WITHDRAW_BATCH_SIZE` | 20 | Source accounts per withheld-fee withdrawal. |
| `CLAIM_MIN_USD` | 5 | Below this a quote asset's fee vault is left to accrue. |
| `JUPITER_SLIPPAGE_BPS` | 100 | Slippage tolerance on both swaps. |
| `COMPUTE_UNIT_LIMIT` / `COMPUTE_UNIT_PRICE_MICROLAMPORTS` | 400000 / 50000 | Explicit compute budget and priority fee on every transaction. |

## Manual runbook

Everything below runs from `tools/keeper` after `npm install` and a filled-in `.env`.

**See what would happen, touching nothing.**

```bash
npm run all
```

Dry run is the default. Each job prints a plan table with amounts in token units and USD, the
wallets involved, and the reason for anything it is skipping.

**Run one job for real.**

```bash
npm run claim-platform-fees -- --execute
npm run distribute-holder-tax -- --execute
npm run buyback-and-burn -- --execute
```

**Handle one token only.**

```bash
npm run distribute-holder-tax -- --mint <mint>
npm run distribute-holder-tax -- --mint <mint> --execute
npm run distribute-holder-tax -- --mint <mint> --quote-mint <quote>   # tracker down: name the pair directly
```

**A distribution died halfway.** Rerun the same command. The open run in `state/<mint>.json` is
resumed, confirmed batches are skipped, and a batch whose signature is on chain is marked confirmed
rather than resent.

```bash
npm run distribute-holder-tax -- --mint <mint> --execute
```

**A transaction landed but the revenue page is missing it.** The row is in
`state/ledger-queue.jsonl`. Inspect it, then re-post:

```bash
npm run flush-queue                 # lists what is queued and why
npm run flush-queue -- --execute    # re-posts; the endpoint is idempotent on the signature
```

**The tracker is down.** `claim-platform-fees` still sweeps the vaults of every quote asset pinned
in `QUOTE_MINTS` and notes that the per-pool fallback was skipped. `distribute-holder-tax` needs
`--mint` and `--quote-mint` together. `buyback-and-burn` waits: its budget comes from the ledger.

**A claim keeps failing.** Check in this order: the fee wallet is the one the platform config names,
the fee wallet has SOL for transaction fees, `CLUSTER` and `RPC_URL` agree with the platform id. The
job refuses to sign when any of these are wrong and says which one.

**Pause the buyback.** Set `BUYBACK_SHARE_BPS=0`, or `BUYBACK_MODE=lock` to keep buying while
holding rather than burning. Either shows up in the next plan and in the ledger meta.

**Never do this.** Do not delete `STATE_DIR`, and do not run two copies of
`distribute-holder-tax` against the same token at once. The ledger is what proves a holder was
already paid.

## Open questions we will answer publicly

- The distribution cadence is daily; if gas makes that uneconomic for a small token, the threshold,
  not the promise, is what changes, and the change lands in this file.
- Holder payouts are off chain in the sense that a program does not enforce them. What makes them
  checkable is that every one of them is a signature on the revenue page against a snapshot rule
  written down here.
