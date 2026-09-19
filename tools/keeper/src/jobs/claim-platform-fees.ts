// Job 1 — claim-platform-fees
//
// Sweeps the 1% StonkAgents platform fee out of Raydium LaunchLab into the platform fee wallet.
//
// On-chain sequence, per quote mint:
//   1. createAssociatedTokenAccountIdempotent(feeWallet, quoteMint)   [SDK, no-op if it exists]
//   2. LaunchLab claim_platform_fee_from_vault
//        accounts: feeWallet (signer), platformFeeVaultAuth, platformConfig,
//                  platformVault PDA ["<platformId>", "<quoteMint>"], feeWallet ATA, quoteMint, tokenProgram
// Fallback, per pool whose pool.platformFee is still unclaimed on the pool account:
//   1. createAssociatedTokenAccountIdempotent(feeWallet, quoteMint)
//   2. LaunchLab claim_platform_fee
//        accounts: feeWallet (signer), launchpadAuth, poolId, platformConfig, pool vaultB,
//                  feeWallet ATA, quoteMint, tokenProgram

import { Connection, Keypair, PublicKey } from '@solana/web3.js';
import {
  TxVersion,
  getPdaLaunchpadPoolId,
  getPdaPlatformVault,
  LaunchpadPool,
} from '@raydium-io/raydium-sdk-v2';
import { getAssociatedTokenAddressSync } from '@solana/spl-token';
import { isMain, runJob, type JobContext } from '../cli.js';
import { computeBudget, explorerTx, initSdk, makeConnection } from '../chain.js';
import { loadPlatform, requireKeypairs, assertAuthority, assertLedgerToken } from '../preflight.js';
import { fetchLaunches, launchesOnPlatform, quoteMintsFrom, type TrackerLaunch } from '../tracker.js';
import { fetchPrices } from '../prices.js';
import { fetchTaxedMint } from '../token2022.js';
import { LedgerClient } from '../ledger.js';
import { fmtUnits, fmtUsd, rawToUsd, Decimal } from '../amounts.js';
import { renderPlan, shortAddr, type Row } from '../plan.js';
import { log, print } from '../log.js';
import { tokenBalance } from '../jupiter.js';

interface ClaimCandidate {
  source: 'vault' | 'pool';
  quoteMint: PublicKey;
  quoteProgram: PublicKey;
  quoteDecimals: number;
  amountRaw: bigint;
  usd?: Decimal;
  /** Base mint, for the pool fallback only. */
  baseMint?: PublicKey;
  poolId?: PublicKey;
  vault?: PublicKey;
  vaultB?: PublicKey;
}

async function main(ctx: JobContext): Promise<void> {
  const { cfg, args } = ctx;
  const connection = makeConnection(cfg);
  const platform = await loadPlatform(cfg, connection);
  const feeWallet = platform.config.platformClaimFeeWallet;

  // The vault sweep only needs the quote mints; the tracker's launch list adds the per-pool
  // fallback. With QUOTE_MINTS pinned, a tracker outage degrades to a vault-only sweep.
  let launches: TrackerLaunch[] = [];
  let trackerError: string | undefined;
  try {
    launches = launchesOnPlatform(await fetchLaunches(cfg), platform.platformId.toBase58());
  } catch (e) {
    if (cfg.QUOTE_MINTS.length === 0) throw e;
    trackerError = (e as Error).message;
    log.warn('tracker unreachable; sweeping pinned QUOTE_MINTS vaults only', { error: trackerError });
  }
  let quoteMints = quoteMintsFrom(launches, cfg.QUOTE_MINTS);
  if (args.quoteMint) quoteMints = quoteMints.filter((q) => q === args.quoteMint);
  if (quoteMints.length === 0) {
    print(renderPlan({
      title: 'claim-platform-fees',
      mode: args.execute ? 'execute' : 'dry-run',
      columns: [],
      rows: [],
      emptyMessage: 'No quote mints to sweep: the tracker has no launches and QUOTE_MINTS is empty.',
    }));
    return;
  }

  const quoteInfo = new Map<string, { program: PublicKey; decimals: number }>();
  for (const q of quoteMints) {
    const info = await fetchTaxedMint(connection, new PublicKey(q));
    quoteInfo.set(q, { program: info.program, decimals: info.decimals });
  }

  const candidates: ClaimCandidate[] = [];
  candidates.push(...(await scanVaults(connection, platform.platformId, platform.programId, quoteMints, quoteInfo)));
  if (!args.vaultOnly && !trackerError) {
    candidates.push(...(await scanPools(connection, platform.programId, launches, quoteMints, quoteInfo)));
  }

  const prices = await fetchPrices(cfg, quoteMints);
  for (const c of candidates) {
    const price = prices.get(c.quoteMint.toBase58());
    if (price !== undefined) c.usd = rawToUsd(c.amountRaw, c.quoteDecimals, price);
  }

  const claimable = candidates.filter((c) => {
    if (c.amountRaw <= 0n) return false;
    if (c.usd === undefined) return true; // Unpriced (new or devnet mint): claim it, do not strand it.
    return c.usd.gte(cfg.CLAIM_MIN_USD);
  });
  const belowThreshold = candidates.filter((c) => c.amountRaw > 0n && !claimable.includes(c));

  const rows: Row[] = claimable.map((c) => ({
    source: c.source,
    quote: shortAddr(c.quoteMint.toBase58()),
    account: shortAddr((c.vault ?? c.poolId ?? c.quoteMint).toBase58()),
    amount: fmtUnits(c.amountRaw, c.quoteDecimals),
    usd: fmtUsd(c.usd),
  }));

  const notes: string[] = [];
  if (trackerError) notes.push(`Tracker unreachable (${trackerError.slice(0, 80)}); per-pool fallback skipped this run.`);
  if (prices.missing.length) notes.push(`No USD price for: ${prices.missing.map(shortAddr).join(', ')}. Claimed anyway.`);
  for (const c of belowThreshold) {
    notes.push(`Skipping ${c.source} ${shortAddr((c.vault ?? c.poolId!).toBase58())}: ${fmtUsd(c.usd)} is below CLAIM_MIN_USD ${fmtUsd(cfg.CLAIM_MIN_USD)}.`);
  }

  print(renderPlan({
    title: 'claim-platform-fees',
    mode: args.execute ? 'execute' : 'dry-run',
    columns: [
      { key: 'source', label: 'SOURCE' },
      { key: 'quote', label: 'QUOTE MINT' },
      { key: 'account', label: 'FROM' },
      { key: 'amount', label: 'AMOUNT', align: 'right' },
      { key: 'usd', label: 'USD', align: 'right' },
    ],
    rows,
    facts: [
      ['cluster', `${cfg.CLUSTER} (${cfg.rpcUrl})`],
      ['platform config', platform.platformId.toBase58()],
      ['fee wallet', feeWallet.toBase58()],
      ['quote mints scanned', String(quoteMints.length)],
      ['total USD', fmtUsd(sumUsd(claimable))],
    ],
    notes,
    emptyMessage: 'Nothing accrued above CLAIM_MIN_USD.',
  }));

  if (!args.execute) {
    print('Dry run. Add --execute to send.');
    return;
  }
  if (claimable.length === 0) return;

  assertLedgerToken(cfg);
  const keys = requireKeypairs([{ envKey: 'FEE_WALLET_KEYPAIR_PATH', filePath: cfg.FEE_WALLET_KEYPAIR_PATH }]);
  const signer = keys.get('FEE_WALLET_KEYPAIR_PATH') as Keypair;
  assertAuthority('FEE_WALLET_KEYPAIR_PATH', signer.publicKey, feeWallet);

  const raydium = await initSdk(cfg, connection, signer);
  const ledgerClient = new LedgerClient(cfg);

  for (const c of claimable) {
    const recipientAta = getAssociatedTokenAddressSync(c.quoteMint, signer.publicKey, true, c.quoteProgram);
    const before = await tokenBalance(connection, recipientAta);
    try {
      const built =
        c.source === 'vault'
          ? await raydium.launchpad.claimVaultPlatformFee<TxVersion.V0>({
              programId: platform.programId,
              platformId: platform.platformId,
              mintB: c.quoteMint,
              mintBProgram: c.quoteProgram,
              txVersion: TxVersion.V0,
              computeBudgetConfig: computeBudget(cfg),
            })
          : await raydium.launchpad.claimPlatformFee<TxVersion.V0>({
              programId: platform.programId,
              platformId: platform.platformId,
              poolId: c.poolId!,
              platformClaimFeeWallet: feeWallet,
              mintB: c.quoteMint,
              vaultB: c.vaultB!,
              mintBProgram: c.quoteProgram,
              txVersion: TxVersion.V0,
              computeBudgetConfig: computeBudget(cfg),
            });
      const { txId } = await built.execute({ sendAndConfirm: true });
      const after = await tokenBalance(connection, recipientAta);
      const claimedRaw = after > before ? after - before : c.amountRaw;
      const price = prices.get(c.quoteMint.toBase58());
      log.info('platform fee claimed', {
        source: c.source,
        signature: txId,
        explorer: explorerTx(cfg, txId),
        quoteMint: c.quoteMint,
        claimedRaw,
      });
      await ledgerClient.record({
        kind: 'platform_fee_claim',
        quoteMint: c.quoteMint.toBase58(),
        amountRaw: claimedRaw,
        amountUsd: price === undefined ? undefined : rawToUsd(claimedRaw, c.quoteDecimals, price),
        signature: txId,
        mint: c.baseMint?.toBase58(),
        occurredAt: new Date().toISOString(),
        meta: {
          source: c.source,
          platformId: platform.platformId.toBase58(),
          feeWallet: feeWallet.toBase58(),
          poolId: c.poolId?.toBase58(),
          vault: c.vault?.toBase58(),
          quoteDecimals: c.quoteDecimals,
        },
      });
    } catch (e) {
      log.error('claim failed', { source: c.source, quoteMint: c.quoteMint, error: e as Error });
    }
  }
}

/** Per-platform fee vaults: PDA ["<platformId>", "<quoteMint>"] holding every trade's platform cut. */
async function scanVaults(
  connection: Connection,
  platformId: PublicKey,
  programId: PublicKey,
  quoteMints: string[],
  quoteInfo: Map<string, { program: PublicKey; decimals: number }>,
): Promise<ClaimCandidate[]> {
  const out: ClaimCandidate[] = [];
  for (const q of quoteMints) {
    const quoteMint = new PublicKey(q);
    const info = quoteInfo.get(q);
    if (!info) continue;
    const vault = getPdaPlatformVault(programId, platformId, quoteMint).publicKey;
    const amountRaw = await tokenBalance(connection, vault);
    out.push({
      source: 'vault',
      quoteMint,
      quoteProgram: info.program,
      quoteDecimals: info.decimals,
      amountRaw,
      vault,
    });
  }
  return out;
}

/**
 * Pools that still carry an unclaimed platformFee on the pool account itself. Pools created before
 * the vault mechanism, and any pool the vault sweep does not cover, are claimed one at a time.
 */
async function scanPools(
  connection: Connection,
  programId: PublicKey,
  launches: TrackerLaunch[],
  quoteMints: string[],
  quoteInfo: Map<string, { program: PublicKey; decimals: number }>,
): Promise<ClaimCandidate[]> {
  const wanted = new Set(quoteMints);
  const targets = launches.filter((l) => wanted.has(l.quote_mint));
  if (targets.length === 0) return [];
  const poolIds = targets.map((l) =>
    l.pool_id
      ? new PublicKey(l.pool_id)
      : getPdaLaunchpadPoolId(programId, new PublicKey(l.mint), new PublicKey(l.quote_mint)).publicKey,
  );
  const out: ClaimCandidate[] = [];
  for (let i = 0; i < poolIds.length; i += 100) {
    const slice = poolIds.slice(i, i + 100);
    const infos = await connection.getMultipleAccountsInfo(slice);
    for (let j = 0; j < slice.length; j++) {
      const info = infos[j];
      const launch = targets[i + j];
      const poolId = slice[j];
      if (!info || !launch || !poolId || !info.owner.equals(programId)) continue;
      const pool = LaunchpadPool.decode(info.data);
      const amountRaw = BigInt(pool.platformFee.toString());
      if (amountRaw <= 0n) continue;
      const qi = quoteInfo.get(launch.quote_mint);
      if (!qi) continue;
      out.push({
        source: 'pool',
        quoteMint: new PublicKey(launch.quote_mint),
        quoteProgram: qi.program,
        quoteDecimals: qi.decimals,
        amountRaw,
        baseMint: new PublicKey(launch.mint),
        poolId,
        vaultB: pool.vaultB,
      });
    }
  }
  return out;
}

function sumUsd(candidates: ClaimCandidate[]): Decimal {
  return candidates.reduce((sum, c) => sum.plus(c.usd ?? 0), new Decimal(0));
}

export { main as claimPlatformFees };

if (isMain(import.meta.url)) {
  await runJob('claim-platform-fees', main);
}
