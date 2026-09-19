// Jupiter swap API v1: quote, build, sign, send. All amounts in raw base units as BigInt.
// The realised output is read from the destination token account delta, not from the quote,
// so the ledger records what actually arrived.

import { Connection, Keypair, PublicKey, VersionedTransaction } from '@solana/web3.js';
import type { KeeperConfig } from './env.js';
import type { FetchLike } from './prices.js';
import { log } from './log.js';

export interface JupQuote {
  inputMint: string;
  outputMint: string;
  inAmount: string;
  outAmount: string;
  otherAmountThreshold: string;
  priceImpactPct?: string;
  slippageBps?: number;
  routePlan?: unknown[];
  [k: string]: unknown;
}

export interface QuoteRequest {
  inputMint: PublicKey;
  outputMint: PublicKey;
  amountRaw: bigint;
  slippageBps?: number;
}

/** GET /quote. Throws with Jupiter's message when no route exists. */
export async function getQuote(
  cfg: KeeperConfig,
  req: QuoteRequest,
  fetchImpl: FetchLike = fetch,
): Promise<JupQuote> {
  if (req.amountRaw <= 0n) throw new Error('swap amount must be > 0');
  const params = new URLSearchParams({
    inputMint: req.inputMint.toBase58(),
    outputMint: req.outputMint.toBase58(),
    amount: req.amountRaw.toString(),
    slippageBps: String(req.slippageBps ?? cfg.JUPITER_SLIPPAGE_BPS),
    restrictIntermediateTokens: 'true',
  });
  const url = `${cfg.JUPITER_API_URL}/quote?${params.toString()}`;
  const res = await fetchImpl(url, { headers: { accept: 'application/json' } });
  const body = await res.text();
  if (!res.ok) throw new Error(`Jupiter quote failed (HTTP ${res.status}): ${body.slice(0, 400)}`);
  const quote = JSON.parse(body) as JupQuote;
  if (!quote.outAmount) throw new Error(`Jupiter returned no route for ${req.inputMint.toBase58()} -> ${req.outputMint.toBase58()}`);
  return quote;
}

/** POST /swap, returning the unsigned VersionedTransaction Jupiter built for `user`. */
export async function buildSwapTransaction(
  cfg: KeeperConfig,
  quote: JupQuote,
  user: PublicKey,
  fetchImpl: FetchLike = fetch,
): Promise<VersionedTransaction> {
  const res = await fetchImpl(`${cfg.JUPITER_API_URL}/swap`, {
    method: 'POST',
    headers: { 'content-type': 'application/json', accept: 'application/json' },
    body: JSON.stringify({
      quoteResponse: quote,
      userPublicKey: user.toBase58(),
      wrapAndUnwrapSol: true,
      dynamicComputeUnitLimit: true,
      prioritizationFeeLamports: {
        priorityLevelWithMaxLamports: {
          maxLamports: Math.max(1, Math.round((cfg.COMPUTE_UNIT_PRICE_MICROLAMPORTS * cfg.COMPUTE_UNIT_LIMIT) / 1_000_000)),
          priorityLevel: 'high',
        },
      },
    }),
  });
  const body = await res.text();
  if (!res.ok) throw new Error(`Jupiter swap build failed (HTTP ${res.status}): ${body.slice(0, 400)}`);
  const json = JSON.parse(body) as { swapTransaction?: string };
  if (!json.swapTransaction) throw new Error('Jupiter swap response had no swapTransaction');
  return VersionedTransaction.deserialize(Buffer.from(json.swapTransaction, 'base64'));
}

export interface SwapResult {
  signature: string;
  inAmountRaw: bigint;
  quotedOutRaw: bigint;
  minOutRaw: bigint;
  /** Realised output, measured from the destination account delta. */
  receivedRaw: bigint;
}

export interface SwapDeps {
  connection: Connection;
  fetchImpl?: FetchLike;
  /** Token account whose balance delta measures the realised output. */
  destinationAccount: PublicKey;
}

/** Signs and sends a Jupiter swap, then measures what landed in `destinationAccount`. */
export async function executeSwap(
  cfg: KeeperConfig,
  quote: JupQuote,
  signer: Keypair,
  deps: SwapDeps,
): Promise<SwapResult> {
  const { connection, destinationAccount } = deps;
  const before = await tokenBalance(connection, destinationAccount);
  const tx = await buildSwapTransaction(cfg, quote, signer.publicKey, deps.fetchImpl ?? fetch);
  tx.sign([signer]);
  const signature = await connection.sendTransaction(tx, { maxRetries: 5, skipPreflight: false });
  const blockhash = await connection.getLatestBlockhash('confirmed');
  await connection.confirmTransaction(
    { signature, blockhash: blockhash.blockhash, lastValidBlockHeight: blockhash.lastValidBlockHeight },
    'confirmed',
  );
  const after = await tokenBalance(connection, destinationAccount);
  const receivedRaw = after > before ? after - before : 0n;
  log.info('swap confirmed', {
    signature,
    inputMint: quote.inputMint,
    outputMint: quote.outputMint,
    inAmountRaw: BigInt(quote.inAmount),
    quotedOutRaw: BigInt(quote.outAmount),
    receivedRaw,
  });
  return {
    signature,
    inAmountRaw: BigInt(quote.inAmount),
    quotedOutRaw: BigInt(quote.outAmount),
    minOutRaw: BigInt(quote.otherAmountThreshold ?? quote.outAmount),
    receivedRaw,
  };
}

export async function tokenBalance(connection: Connection, account: PublicKey): Promise<bigint> {
  const info = await connection.getTokenAccountBalance(account).catch(() => null);
  return info ? BigInt(info.value.amount) : 0n;
}
