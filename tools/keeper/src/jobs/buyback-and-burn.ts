// Job 3 — buyback-and-burn
//
// Spends BUYBACK_SHARE_BPS of the platform fees and launch fees earned since the last run on
// STONKAGENTS, then destroys what it bought.
//
// On-chain sequence:
//   1. AssociatedToken CreateIdempotent(buyback wallet, STONKAGENTS mint)   [if missing]
//   2. Jupiter swap: quote asset -> STONKAGENTS, signed by the buyback wallet
//   3. TokenProgram BurnChecked(buyback ATA, STONKAGENTS mint, buyback wallet, amount, decimals)
//      skipped when BUYBACK_MODE=lock, which leaves the tokens in the buyback wallet
//
// The watermark in state/buyback-watermark.json advances only by the share of the budget that was
// actually spent, so revenue is never written off because a wallet was short.

import { Connection, Keypair, PublicKey } from '@solana/web3.js';
import { NATIVE_MINT, createBurnCheckedInstruction, getAssociatedTokenAddressSync } from '@solana/spl-token';
import { isMain, runJob, type JobContext } from '../cli.js';
import { explorerTx, makeConnection } from '../chain.js';
import { loadPlatform, requireKeypairs, assertLedgerToken } from '../preflight.js';
import { fetchLaunches, fetchRevenueSummary, launchesOnPlatform, quoteMintsFrom } from '../tracker.js';
import { fetchPrices } from '../prices.js';
import { fetchTaxedMint } from '../token2022.js';
import { executeSwap, getQuote, tokenBalance } from '../jupiter.js';
import { planAta } from '../distribute.js';
import { buildSignedTx, sendSignedTx } from '../tx.js';
import { LedgerClient } from '../ledger.js';
import { advanceWatermark, computeBuybackBudget, loadWatermark, saveWatermark } from '../state.js';
import { fmtUnits, fmtUsd, rawToUsd, usdToRaw, Decimal, minBigInt } from '../amounts.js';
import { renderPlan, shortAddr, type Row } from '../plan.js';
import { log, print } from '../log.js';

/** Lamports left in the buyback wallet for transaction fees when spending native SOL. */
const SOL_FEE_RESERVE = 20_000_000n;

interface SpendPlan {
  quoteMint: PublicKey;
  program: PublicKey;
  decimals: number;
  balanceRaw: bigint;
  spendRaw: bigint;
  spendUsd: Decimal;
  price: number;
}

async function main(ctx: JobContext): Promise<void> {
  const { cfg, args } = ctx;
  const connection = makeConnection(cfg);
  const platform = await loadPlatform(cfg, connection);

  if (!cfg.PLATFORM_TOKEN_MINT) {
    log.info('buyback skipped: PLATFORM_TOKEN_MINT unset', { reason: 'STONKAGENTS mint not configured yet' });
    print(renderPlan({
      title: 'buyback-and-burn',
      mode: args.execute ? 'execute' : 'dry-run',
      columns: [], rows: [],
      emptyMessage: 'Skipped: PLATFORM_TOKEN_MINT is not set, so there is no STONKAGENTS mint to buy. Set it after the token launches.',
    }));
    return;
  }
  const platformToken = new PublicKey(cfg.PLATFORM_TOKEN_MINT);

  const watermark = loadWatermark(cfg.stateDir, cfg.CLUSTER);
  const summary = await fetchRevenueSummary(cfg);
  const launches = launchesOnPlatform(await fetchLaunches(cfg), platform.platformId.toBase58());
  const quoteMints = quoteMintsFrom(launches, [...cfg.QUOTE_MINTS, NATIVE_MINT.toBase58()]);

  const prices = await fetchPrices(cfg, [...quoteMints, NATIVE_MINT.toBase58(), cfg.PLATFORM_TOKEN_MINT]);
  const solUsd = prices.get(NATIVE_MINT.toBase58());
  if (solUsd === undefined) throw new Error('No SOL price from Jupiter; cannot value launch fees');

  const budget = computeBuybackBudget({ summary, watermark, shareBps: cfg.BUYBACK_SHARE_BPS, solUsd });

  let signer: Keypair | undefined;
  if (args.execute) {
    assertLedgerToken(cfg);
    const keys = requireKeypairs([{ envKey: 'BUYBACK_WALLET_KEYPAIR_PATH', filePath: cfg.BUYBACK_WALLET_KEYPAIR_PATH }]);
    signer = keys.get('BUYBACK_WALLET_KEYPAIR_PATH');
  } else if (cfg.BUYBACK_WALLET_KEYPAIR_PATH) {
    // Dry runs read the wallet only to show real balances; a missing key is not fatal here.
    try {
      const keys = requireKeypairs([{ envKey: 'BUYBACK_WALLET_KEYPAIR_PATH', filePath: cfg.BUYBACK_WALLET_KEYPAIR_PATH }]);
      signer = keys.get('BUYBACK_WALLET_KEYPAIR_PATH');
    } catch (e) {
      log.warn('buyback wallet not readable; plan will show budget only', { error: (e as Error).message });
    }
  }

  const spends = signer
    ? await planSpends(connection, cfg, quoteMints, prices, budget.budgetUsd, signer.publicKey)
    : [];
  const plannedUsd = spends.reduce((sum, s) => sum.plus(s.spendUsd), new Decimal(0));

  const rows: Row[] = spends.map((s) => ({
    quote: shortAddr(s.quoteMint.toBase58()),
    balance: fmtUnits(s.balanceRaw, s.decimals),
    spend: fmtUnits(s.spendRaw, s.decimals),
    usd: fmtUsd(s.spendUsd),
  }));

  const notes: string[] = [];
  if (!signer) notes.push('BUYBACK_WALLET_KEYPAIR_PATH is not readable; balances and spend are unknown.');
  if (budget.budgetUsd.lt(cfg.BUYBACK_MIN_USD)) {
    notes.push(`Budget ${fmtUsd(budget.budgetUsd)} is below BUYBACK_MIN_USD ${fmtUsd(cfg.BUYBACK_MIN_USD)}; nothing will be bought.`);
  }
  if (signer && spends.length === 0 && budget.budgetUsd.gt(0)) {
    notes.push('Buyback wallet holds none of the quote assets; fund it from the fee wallet.');
  }
  notes.push(`Mode ${cfg.BUYBACK_MODE}: bought tokens are ${cfg.BUYBACK_MODE === 'burn' ? 'burned' : 'held in the buyback wallet'}.`);

  print(renderPlan({
    title: 'buyback-and-burn',
    mode: args.execute ? 'execute' : 'dry-run',
    columns: [
      { key: 'quote', label: 'SPEND MINT' },
      { key: 'balance', label: 'WALLET BAL', align: 'right' },
      { key: 'spend', label: 'SPEND', align: 'right' },
      { key: 'usd', label: 'USD', align: 'right' },
    ],
    rows,
    facts: [
      ['cluster', `${cfg.CLUSTER} (${cfg.rpcUrl})`],
      ['STONKAGENTS mint', platformToken.toBase58()],
      ['buyback wallet', signer ? signer.publicKey.toBase58() : '(key not loaded)'],
      ['new launch fees', `${fmtUnits(budget.newLaunchFeeRaw, 9)} SOL (${fmtUsd(budget.newLaunchFeeUsd)})`],
      ['new platform fees', fmtUsd(budget.newPlatformFeeUsd)],
      ['share', `${cfg.BUYBACK_SHARE_BPS / 100}% of ${fmtUsd(budget.grossUsd)}`],
      ['budget', fmtUsd(budget.budgetUsd)],
      ['planned spend', fmtUsd(plannedUsd)],
      ['watermark', watermark.lastRunAt ?? 'never run'],
    ],
    notes,
    emptyMessage: 'No new revenue to spend.',
  }));

  if (!args.execute) {
    print('Dry run. Add --execute to send.');
    return;
  }
  if (!signer || spends.length === 0 || budget.budgetUsd.lt(cfg.BUYBACK_MIN_USD)) return;

  const ledgerClient = new LedgerClient(cfg);
  const tokenInfo = await fetchTaxedMint(connection, platformToken);
  const tokenAta = await planAta(connection, signer.publicKey, signer.publicKey, platformToken, tokenInfo.program);
  if (tokenAta.createIx) {
    const built = await buildSignedTx(cfg, connection, signer, [tokenAta.createIx]);
    await sendSignedTx(connection, built);
  }

  let spentUsd = new Decimal(0);
  let boughtRaw = 0n;
  for (const spend of spends) {
    try {
      const quote = await getQuote(cfg, {
        inputMint: spend.quoteMint,
        outputMint: platformToken,
        amountRaw: spend.spendRaw,
      });
      const swap = await executeSwap(cfg, quote, signer, { connection, destinationAccount: tokenAta.address });
      spentUsd = spentUsd.plus(spend.spendUsd);
      boughtRaw += swap.receivedRaw;
      log.info('buyback executed', {
        signature: swap.signature, explorer: explorerTx(cfg, swap.signature),
        spentRaw: swap.inAmountRaw, receivedRaw: swap.receivedRaw,
      });
      await ledgerClient.record({
        kind: 'buyback',
        quoteMint: spend.quoteMint.toBase58(),
        amountRaw: swap.inAmountRaw,
        amountUsd: spend.spendUsd,
        signature: swap.signature,
        mint: platformToken.toBase58(),
        occurredAt: new Date().toISOString(),
        meta: {
          receivedRaw: swap.receivedRaw.toString(),
          quotedOutRaw: swap.quotedOutRaw.toString(),
          minOutRaw: swap.minOutRaw.toString(),
          shareBps: cfg.BUYBACK_SHARE_BPS,
          budgetUsd: budget.budgetUsd.toFixed(),
          mode: cfg.BUYBACK_MODE,
        },
      });
    } catch (e) {
      log.error('buyback swap failed', { quoteMint: spend.quoteMint, error: e as Error });
    }
  }

  saveWatermark(cfg.stateDir, advanceWatermark({ watermark, budget, spentUsd }));

  if (cfg.BUYBACK_MODE === 'lock') {
    log.info('BUYBACK_MODE=lock: tokens kept in the buyback wallet, not burned', { boughtRaw });
    return;
  }
  if (boughtRaw <= 0n) return;

  const burnRaw = minBigInt(boughtRaw, await tokenBalance(connection, tokenAta.address));
  if (burnRaw <= 0n) return;
  const burnIx = createBurnCheckedInstruction(
    tokenAta.address, platformToken, signer.publicKey, burnRaw, tokenInfo.decimals, [], tokenInfo.program,
  );
  const built = await buildSignedTx(cfg, connection, signer, [burnIx]);
  const signature = await sendSignedTx(connection, built);
  const tokenPrice = prices.get(cfg.PLATFORM_TOKEN_MINT);
  log.info('buyback burned', { signature, explorer: explorerTx(cfg, signature), burnedRaw: burnRaw });
  await ledgerClient.record({
    kind: 'burn',
    quoteMint: platformToken.toBase58(),
    amountRaw: burnRaw,
    amountUsd: tokenPrice === undefined ? undefined : rawToUsd(burnRaw, tokenInfo.decimals, tokenPrice),
    signature,
    mint: platformToken.toBase58(),
    occurredAt: new Date().toISOString(),
    meta: { decimals: tokenInfo.decimals, mode: cfg.BUYBACK_MODE },
  });
}

/**
 * How much of each quote asset the buyback wallet can spend, largest holding first, until the
 * USD budget is used up. Native SOL keeps SOL_FEE_RESERVE lamports back for transaction fees.
 */
async function planSpends(
  connection: Connection,
  cfg: JobContext['cfg'],
  quoteMints: string[],
  prices: Awaited<ReturnType<typeof fetchPrices>>,
  budgetUsd: Decimal,
  wallet: PublicKey,
): Promise<SpendPlan[]> {
  const candidates: SpendPlan[] = [];
  for (const q of quoteMints) {
    const price = prices.get(q);
    if (price === undefined) continue;
    const mint = new PublicKey(q);
    const info = await fetchTaxedMint(connection, mint);
    let balanceRaw = await tokenBalance(connection, getAssociatedTokenAddressSync(mint, wallet, true, info.program));
    if (mint.equals(NATIVE_MINT)) {
      const lamports = BigInt(await connection.getBalance(wallet));
      balanceRaw += lamports > SOL_FEE_RESERVE ? lamports - SOL_FEE_RESERVE : 0n;
    }
    if (balanceRaw <= 0n) continue;
    candidates.push({
      quoteMint: mint, program: info.program, decimals: info.decimals,
      balanceRaw, spendRaw: 0n, spendUsd: new Decimal(0), price,
    });
  }
  candidates.sort((a, b) =>
    rawToUsd(b.balanceRaw, b.decimals, b.price).comparedTo(rawToUsd(a.balanceRaw, a.decimals, a.price)),
  );

  const out: SpendPlan[] = [];
  let remaining = budgetUsd;
  for (const c of candidates) {
    if (remaining.lte(0)) break;
    const wanted = usdToRaw(remaining, c.decimals, c.price);
    const spendRaw = minBigInt(wanted, c.balanceRaw);
    if (spendRaw <= 0n) continue;
    const spendUsd = rawToUsd(spendRaw, c.decimals, c.price);
    out.push({ ...c, spendRaw, spendUsd });
    remaining = remaining.minus(spendUsd);
  }
  return out;
}

export { main as buybackAndBurn };

if (isMain(import.meta.url)) {
  await runJob('buyback-and-burn', main);
}
