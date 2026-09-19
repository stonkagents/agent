// Job 2 — distribute-holder-tax
//
// Turns the Token-2022 1% transfer tax withheld on every StonkAgents token into a pro-rata
// payout to that token's holders, denominated in the pool's quote asset.
//
// On-chain sequence per mint:
//   1. AssociatedToken CreateIdempotent(distribution wallet, base mint)      [if missing]
//   2. TransferFeeExtension WithdrawWithheldTokensFromAccounts(mint, dest, authority, sources[])  x N batches
//   3. TransferFeeExtension HarvestWithheldTokensToMint(mint, sources[])     [only for batches that failed]
//   4. TransferFeeExtension WithdrawWithheldTokensFromMint(mint, dest, authority)
//   5. Jupiter swap: base -> quote, signed by the distribution wallet
//   6. per payout batch: AssociatedToken CreateIdempotent(recipient, quote mint) [if missing]
//                        TokenProgram TransferChecked(source, quoteMint, recipientAta, amount, decimals)
//
// Idempotency: state/<mint>.json holds the run, its batches and their signatures. A rerun resumes
// the open run, checks every recorded signature on chain, and never rebuilds a confirmed batch.

import { Connection, Keypair, PublicKey } from '@solana/web3.js';
import { getAssociatedTokenAddressSync, TOKEN_2022_PROGRAM_ID } from '@solana/spl-token';
import { LaunchpadPool, getPdaLaunchpadPoolId } from '@raydium-io/raydium-sdk-v2';
import { isMain, runJob, type JobContext } from '../cli.js';
import { makeConnection, explorerTx } from '../chain.js';
import { loadPlatform, requireKeypairs, assertAuthority, assertLedgerToken, type PlatformContext } from '../preflight.js';
import { fetchLaunches, launchesOnPlatform, type TrackerLaunch } from '../tracker.js';
import { fetchPrices } from '../prices.js';
import { fetchTaxedMint, scanTokenAccounts, withheldSources, sumWithheld, buildWithdrawBatches, buildHolderSnapshot } from '../token2022.js';
import { selectEligible, computeProRata, buildBatches } from '../prorata.js';
import { getQuote, executeSwap, tokenBalance } from '../jupiter.js';
import { buildPayoutInstructions, planAta, withdrawWithheld } from '../distribute.js';
import { buildSignedTx, sendSignedTx, signatureLanded } from '../tx.js';
import { LedgerClient } from '../ledger.js';
import {
  loadLedger, saveLedger, openRun, startRun, markConfirmed, markFailed, markSent,
  type DistributionRun, type MintLedger,
} from '../state.js';
import { fmtUnits, fmtUsd, rawToUsd, unitsToRaw, Decimal } from '../amounts.js';
import { renderPlan, shortAddr, type Row } from '../plan.js';
import { log, print } from '../log.js';

interface Target {
  mint: PublicKey;
  quoteMint: PublicKey;
  symbol: string;
  poolId?: PublicKey;
}

async function main(ctx: JobContext): Promise<void> {
  const { cfg, args } = ctx;
  const connection = makeConnection(cfg);
  const platform = await loadPlatform(cfg, connection);

  // `--mint X --quote-mint Y` names the target directly, so one token can be handled while the
  // tracker is down. Otherwise the tracker's launch list is the source of truth.
  let targets: Target[];
  if (args.mint && args.quoteMint) {
    targets = [{ mint: new PublicKey(args.mint), quoteMint: new PublicKey(args.quoteMint), symbol: '-' }];
  } else {
    const launches = launchesOnPlatform(await fetchLaunches(cfg), platform.platformId.toBase58());
    targets = resolveTargets(launches, args.mint);
  }
  if (targets.length === 0) {
    print(renderPlan({
      title: 'distribute-holder-tax', mode: args.execute ? 'execute' : 'dry-run', columns: [], rows: [],
      emptyMessage: args.mint
        ? `Mint ${args.mint} is not a recorded launch on our platform (pass --quote-mint to target it directly).`
        : 'No launched token carries a transfer fee.',
    }));
    return;
  }

  const ledgerClient = new LedgerClient(cfg);
  let keys: Map<string, Keypair> | undefined;
  if (args.execute) {
    assertLedgerToken(cfg);
    keys = requireKeypairs([
      { envKey: 'TRANSFER_FEE_AUTHORITY_KEYPAIR_PATH', filePath: cfg.TRANSFER_FEE_AUTHORITY_KEYPAIR_PATH },
      { envKey: 'DISTRIBUTION_WALLET_KEYPAIR_PATH', filePath: cfg.distributionKeypairPath },
    ]);
    assertAuthority(
      'TRANSFER_FEE_AUTHORITY_KEYPAIR_PATH',
      (keys.get('TRANSFER_FEE_AUTHORITY_KEYPAIR_PATH') as Keypair).publicKey,
      platform.config.transferFeeExtensionAuth,
    );
  }

  const rows: Row[] = [];
  const notes: string[] = [];
  for (const target of targets) {
    try {
      const row = await processMint({ ctx, connection, platform, target, ledgerClient, keys, notes });
      if (row) rows.push(row);
    } catch (e) {
      log.error('mint failed', { mint: target.mint, error: e as Error });
      notes.push(`${shortAddr(target.mint.toBase58())}: ${(e as Error).message}`);
    }
  }

  print(renderPlan({
    title: 'distribute-holder-tax',
    mode: args.execute ? 'execute' : 'dry-run',
    columns: [
      { key: 'mint', label: 'MINT' },
      { key: 'symbol', label: 'SYMBOL' },
      { key: 'withheld', label: 'WITHHELD', align: 'right' },
      { key: 'usd', label: 'USD', align: 'right' },
      { key: 'holders', label: 'HOLDERS', align: 'right' },
      { key: 'payout', label: 'PAYOUT (QUOTE)', align: 'right' },
      { key: 'action', label: 'ACTION' },
    ],
    rows,
    facts: [
      ['cluster', `${cfg.CLUSTER} (${cfg.rpcUrl})`],
      ['transfer-fee authority', platform.config.transferFeeExtensionAuth.toBase58()],
      ['min per mint', fmtUsd(cfg.DISTRIBUTION_MIN_USD)],
      ['dust threshold', `${cfg.DISTRIBUTION_DUST_UNITS} tokens`],
      ['recipients per tx', String(cfg.DISTRIBUTION_BATCH_SIZE)],
      ['state dir', cfg.stateDir],
    ],
    notes,
    emptyMessage: 'No mint has withheld tax above DISTRIBUTION_MIN_USD.',
  }));
  if (!args.execute) print('Dry run. Add --execute to send.');
}

interface ProcessArgs {
  ctx: JobContext;
  connection: Connection;
  platform: PlatformContext;
  target: Target;
  ledgerClient: LedgerClient;
  keys?: Map<string, Keypair>;
  notes: string[];
}

async function processMint(a: ProcessArgs): Promise<Row | undefined> {
  const { ctx, connection, target, notes } = a;
  const { cfg, args } = ctx;
  const mintB58 = target.mint.toBase58();

  const baseInfo = await fetchTaxedMint(connection, target.mint);
  if (baseInfo.transferFeeBps === undefined) {
    notes.push(`${shortAddr(mintB58)}: no transfer-fee extension; nothing to distribute.`);
    return undefined;
  }
  const quoteInfo = await fetchTaxedMint(connection, target.quoteMint);
  const ledger = loadLedger(cfg.stateDir, mintB58, cfg.CLUSTER);

  const accounts = await scanTokenAccounts(connection, target.mint, baseInfo.program);
  const sources = withheldSources(accounts);
  const withheldRaw = sumWithheld(accounts) + baseInfo.withheldOnMintRaw;

  const exclusions = await poolExclusions(connection, a.platform, target);
  const snapshot = buildHolderSnapshot(accounts, {
    exclude: [...exclusions, ...cfg.HOLDER_EXCLUDE],
    mint: target.mint,
  });
  const dustRaw = unitsToRaw(cfg.DISTRIBUTION_DUST_UNITS, baseInfo.decimals);
  const eligibility = selectEligible(snapshot.holders, { dustRaw, exclude: cfg.HOLDER_EXCLUDE });

  const prices = await fetchPrices(cfg, [mintB58, target.quoteMint.toBase58()]);
  const basePrice = prices.get(mintB58);
  const withheldUsd = basePrice === undefined ? undefined : rawToUsd(withheldRaw, baseInfo.decimals, basePrice);

  const resume = openRun(ledger);
  const carryRaw = BigInt(ledger.carryRaw);

  // Estimate the quote proceeds so the dry-run plan shows a real payout figure.
  let estimatedQuoteRaw = carryRaw;
  if (withheldRaw > 0n) {
    try {
      const quote = await getQuote(cfg, { inputMint: target.mint, outputMint: target.quoteMint, amountRaw: withheldRaw });
      estimatedQuoteRaw += BigInt(quote.outAmount);
    } catch (e) {
      notes.push(`${shortAddr(mintB58)}: no Jupiter route yet (${(e as Error).message.slice(0, 80)}).`);
    }
  }

  const row: Row = {
    mint: shortAddr(mintB58),
    symbol: target.symbol,
    withheld: fmtUnits(withheldRaw, baseInfo.decimals),
    usd: fmtUsd(withheldUsd),
    holders: String(eligibility.eligible.length),
    payout: fmtUnits(estimatedQuoteRaw, quoteInfo.decimals),
    action: 'distribute',
  };

  if (resume) {
    row.action = `resume run ${resume.id}`;
  } else if (eligibility.eligible.length === 0) {
    row.action = 'skip: no eligible holders';
    notes.push(`${shortAddr(mintB58)}: ${snapshot.skipped.length} accounts excluded, ${eligibility.dust.length} below dust.`);
    return row;
  } else if (withheldUsd !== undefined && withheldUsd.lt(cfg.DISTRIBUTION_MIN_USD)) {
    row.action = `skip: below ${fmtUsd(cfg.DISTRIBUTION_MIN_USD)}`;
    return row;
  } else if (withheldRaw === 0n && carryRaw === 0n) {
    row.action = 'skip: nothing withheld';
    return row;
  }

  if (!args.execute) return row;

  const authority = a.keys?.get('TRANSFER_FEE_AUTHORITY_KEYPAIR_PATH');
  const distributor = a.keys?.get('DISTRIBUTION_WALLET_KEYPAIR_PATH');
  if (!authority || !distributor) throw new Error('execute path reached without loaded keypairs');

  let run = resume;
  if (!run) {
    run = await prepareRun({
      cfg, connection, ledger, target, baseInfo, quoteInfo, authority, distributor,
      sources, withheldOnMintRaw: baseInfo.withheldOnMintRaw, carryRaw, eligibility,
    });
    if (!run) {
      row.action = 'skip: nothing to pay after swap';
      return row;
    }
  }

  const paid = await settleRun({ cfg, connection, ledger, run, target, quoteInfo, distributor, ledgerClient: a.ledgerClient });
  row.action = `paid ${paid} batch(es)`;
  row.payout = fmtUnits(BigInt(run.distributableRaw), quoteInfo.decimals);
  return row;
}

interface PrepareArgs {
  cfg: JobContext['cfg'];
  connection: Connection;
  ledger: MintLedger;
  target: Target;
  baseInfo: Awaited<ReturnType<typeof fetchTaxedMint>>;
  quoteInfo: Awaited<ReturnType<typeof fetchTaxedMint>>;
  authority: Keypair;
  distributor: Keypair;
  sources: Awaited<ReturnType<typeof scanTokenAccounts>>;
  withheldOnMintRaw: bigint;
  carryRaw: bigint;
  eligibility: ReturnType<typeof selectEligible>;
}

/** Withdraw -> swap -> snapshot -> pro-rata -> open a run in the on-disk ledger. */
async function prepareRun(a: PrepareArgs): Promise<DistributionRun | undefined> {
  const { cfg, connection, ledger, target, baseInfo, quoteInfo, authority, distributor } = a;

  const baseAta = await planAta(connection, distributor.publicKey, distributor.publicKey, target.mint, baseInfo.program);
  if (baseAta.createIx) {
    const built = await buildSignedTx(cfg, connection, distributor, [baseAta.createIx]);
    await sendSignedTx(connection, built);
  }

  const withdrawal = await withdrawWithheld({
    cfg, connection,
    mint: target.mint,
    destination: baseAta.address,
    authority, payer: distributor,
    batches: buildWithdrawBatches(a.sources, cfg.WITHDRAW_BATCH_SIZE),
    withheldOnMintRaw: a.withheldOnMintRaw,
    tokenProgram: baseInfo.program,
  });
  for (const sig of withdrawal.signatures) {
    ledger.withdrawals.push({ signature: sig, amountRaw: withdrawal.withdrawnRaw.toString(), at: new Date().toISOString() });
  }
  saveLedger(cfg.stateDir, ledger);

  const quoteAta = await planAta(connection, distributor.publicKey, distributor.publicKey, target.quoteMint, quoteInfo.program);
  if (quoteAta.createIx) {
    const built = await buildSignedTx(cfg, connection, distributor, [quoteAta.createIx]);
    await sendSignedTx(connection, built);
  }

  let distributableRaw = a.carryRaw;
  const swapInputRaw = await tokenBalance(connection, baseAta.address);
  if (swapInputRaw > 0n) {
    const quote = await getQuote(cfg, { inputMint: target.mint, outputMint: target.quoteMint, amountRaw: swapInputRaw });
    const swap = await executeSwap(cfg, quote, distributor, { connection, destinationAccount: quoteAta.address });
    ledger.swaps.push({
      signature: swap.signature,
      inRaw: swap.inAmountRaw.toString(),
      outRaw: swap.receivedRaw.toString(),
      at: new Date().toISOString(),
    });
    distributableRaw += swap.receivedRaw;
    saveLedger(cfg.stateDir, ledger);
  }
  if (distributableRaw <= 0n) return undefined;

  const prorata = computeProRata(distributableRaw, a.eligibility.eligible);
  if (prorata.payouts.length === 0) {
    ledger.carryRaw = distributableRaw.toString();
    saveLedger(cfg.stateDir, ledger);
    return undefined;
  }
  const batches = buildBatches(prorata.payouts, cfg.DISTRIBUTION_BATCH_SIZE);
  const run = startRun(ledger, {
    quoteMint: target.quoteMint.toBase58(),
    quoteDecimals: quoteInfo.decimals,
    distributableRaw: prorata.paidRaw,
    totalEligibleRaw: prorata.totalEligibleRaw,
    amountPerUnit: prorata.amountPerUnit,
    batches,
  });
  ledger.carryRaw = prorata.remainderRaw.toString();
  saveLedger(cfg.stateDir, ledger);
  log.info('distribution run opened', {
    run: run.id, batches: batches.length, recipients: prorata.payouts.length,
    distributableRaw: prorata.paidRaw, carryRaw: prorata.remainderRaw,
  });
  return run;
}

interface SettleArgs {
  cfg: JobContext['cfg'];
  connection: Connection;
  ledger: MintLedger;
  run: DistributionRun;
  target: Target;
  quoteInfo: Awaited<ReturnType<typeof fetchTaxedMint>>;
  distributor: Keypair;
  ledgerClient: LedgerClient;
}

/** Sends every batch that is not confirmed yet, recording each one before and after the send. */
async function settleRun(a: SettleArgs): Promise<number> {
  const { cfg, connection, ledger, run, target, quoteInfo, distributor, ledgerClient } = a;
  const source = getAssociatedTokenAddressSync(target.quoteMint, distributor.publicKey, true, quoteInfo.program);
  let confirmed = 0;

  for (const batch of run.batches) {
    if (batch.status === 'confirmed') continue;
    if (batch.signature && (await signatureLanded(connection, batch.signature))) {
      markConfirmed(run, batch.index, batch.signature);
      saveLedger(cfg.stateDir, ledger);
      confirmed++;
      continue;
    }
    try {
      const ixs = await buildPayoutInstructions({
        connection,
        payer: distributor.publicKey,
        source,
        quoteMint: target.quoteMint,
        quoteProgram: quoteInfo.program,
        quoteDecimals: quoteInfo.decimals,
        recipients: batch.recipients,
      });
      const built = await buildSignedTx(cfg, connection, distributor, ixs);
      markSent(run, batch.index, built.signature);
      saveLedger(cfg.stateDir, ledger);
      const signature = await sendSignedTx(connection, built);
      markConfirmed(run, batch.index, signature);
      saveLedger(cfg.stateDir, ledger);
      confirmed++;
      log.info('holder batch paid', {
        run: run.id, batch: batch.index, signature, explorer: explorerTx(cfg, signature),
        recipients: batch.recipients.length, amountRaw: batch.totalRaw,
      });
      await ledgerClient.record({
        kind: 'holder_distribution',
        quoteMint: run.quoteMint,
        amountRaw: BigInt(batch.totalRaw),
        amountUsd: await usdFor(cfg, run.quoteMint, BigInt(batch.totalRaw), run.quoteDecimals),
        signature,
        mint: target.mint.toBase58(),
        occurredAt: new Date().toISOString(),
        meta: {
          run: run.id,
          batch: batch.index,
          amountPerUnit: run.amountPerUnit,
          recipients: batch.recipients.map((r) => ({ owner: r.owner, amountRaw: r.amountRaw })),
          recipientCount: batch.recipients.length,
          totalEligibleRaw: run.totalEligibleRaw,
        },
      });
    } catch (e) {
      markFailed(run, batch.index, (e as Error).message);
      saveLedger(cfg.stateDir, ledger);
      log.error('holder batch failed', { run: run.id, batch: batch.index, error: e as Error });
    }
  }
  return confirmed;
}

async function usdFor(cfg: JobContext['cfg'], mint: string, raw: bigint, decimals: number): Promise<Decimal | undefined> {
  const prices = await fetchPrices(cfg, [mint]);
  const price = prices.get(mint);
  return price === undefined ? undefined : rawToUsd(raw, decimals, price);
}

/** Pool id, base vault and quote vault: never holders, never recipients. */
async function poolExclusions(connection: Connection, platform: PlatformContext, target: Target): Promise<string[]> {
  const poolId = target.poolId ?? getPdaLaunchpadPoolId(platform.programId, target.mint, target.quoteMint).publicKey;
  const info = await connection.getAccountInfo(poolId);
  if (!info || !info.owner.equals(platform.programId)) return [poolId.toBase58()];
  const pool = LaunchpadPool.decode(info.data);
  return [poolId.toBase58(), pool.vaultA.toBase58(), pool.vaultB.toBase58(), pool.creator.toBase58()];
}

function resolveTargets(launches: TrackerLaunch[], mintFilter?: string): Target[] {
  const rows = mintFilter ? launches.filter((l) => l.mint === mintFilter) : launches.filter((l) => l.transfer_fee_bps > 0);
  return rows.map((l) => ({
    mint: new PublicKey(l.mint),
    quoteMint: new PublicKey(l.quote_mint),
    symbol: l.symbol || '-',
    poolId: l.pool_id ? new PublicKey(l.pool_id) : undefined,
  }));
}

export { main as distributeHolderTax };
export { TOKEN_2022_PROGRAM_ID };

if (isMain(import.meta.url)) {
  await runJob('distribute-holder-tax', main);
}
