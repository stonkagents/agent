// Versioned transaction build / simulate / send. Every keeper transaction carries an explicit
// compute unit limit and priority fee so a busy slot cannot silently drop a payout.

import {
  Connection,
  Keypair,
  PublicKey,
  TransactionInstruction,
  TransactionMessage,
  VersionedTransaction,
} from '@solana/web3.js';
import type { KeeperConfig } from './env.js';
import { computeBudgetIxs } from './chain.js';
import { log } from './log.js';

export interface BuiltTx {
  transaction: VersionedTransaction;
  signature: string;
  blockhash: string;
  lastValidBlockHeight: number;
}

/**
 * Builds and signs a v0 transaction. The signature exists before the send, which is what lets the
 * distribution ledger record "sent" before the network hears about it: a crash between sign and
 * send leaves a signature we can look up rather than an unknown payment.
 */
export async function buildSignedTx(
  cfg: KeeperConfig,
  connection: Connection,
  payer: Keypair,
  instructions: TransactionInstruction[],
  extraSigners: Keypair[] = [],
): Promise<BuiltTx> {
  const { blockhash, lastValidBlockHeight } = await connection.getLatestBlockhash('confirmed');
  const message = new TransactionMessage({
    payerKey: payer.publicKey,
    recentBlockhash: blockhash,
    instructions: [...computeBudgetIxs(cfg), ...instructions],
  }).compileToV0Message();
  const tx = new VersionedTransaction(message);
  tx.sign([payer, ...extraSigners]);
  const sig = tx.signatures[0];
  if (!sig) throw new Error('transaction was not signed');
  return { transaction: tx, signature: bs58Encode(sig), blockhash, lastValidBlockHeight };
}

/** Sends an already-signed transaction and waits for `confirmed`. */
export async function sendSignedTx(connection: Connection, built: BuiltTx): Promise<string> {
  const signature = await connection.sendTransaction(built.transaction, { maxRetries: 5 });
  await connection.confirmTransaction(
    { signature, blockhash: built.blockhash, lastValidBlockHeight: built.lastValidBlockHeight },
    'confirmed',
  );
  return signature;
}

/** Build, sign, send, confirm. Returns the signature. */
export async function sendInstructions(
  cfg: KeeperConfig,
  connection: Connection,
  payer: Keypair,
  instructions: TransactionInstruction[],
  extraSigners: Keypair[] = [],
): Promise<string> {
  const built = await buildSignedTx(cfg, connection, payer, instructions, extraSigners);
  return sendSignedTx(connection, built);
}

/** Simulates without signature verification, for dry runs that want an on-chain sanity check. */
export async function simulate(
  cfg: KeeperConfig,
  connection: Connection,
  payer: PublicKey,
  instructions: TransactionInstruction[],
): Promise<{ ok: boolean; err: unknown; logs: string[]; unitsConsumed?: number }> {
  const { blockhash } = await connection.getLatestBlockhash('confirmed');
  const message = new TransactionMessage({
    payerKey: payer,
    recentBlockhash: blockhash,
    instructions: [...computeBudgetIxs(cfg), ...instructions],
  }).compileToV0Message();
  const res = await connection.simulateTransaction(new VersionedTransaction(message), {
    sigVerify: false,
    replaceRecentBlockhash: true,
  });
  return {
    ok: !res.value.err,
    err: res.value.err,
    logs: res.value.logs ?? [],
    unitsConsumed: res.value.unitsConsumed,
  };
}

/**
 * Whether a previously recorded signature already landed. Used on resume so a batch that was sent
 * before a crash is never paid twice.
 */
export async function signatureLanded(connection: Connection, signature: string): Promise<boolean> {
  const res = await connection.getSignatureStatus(signature, { searchTransactionHistory: true }).catch(() => null);
  const status = res?.value;
  if (!status) return false;
  if (status.err) {
    log.warn('recorded signature failed on chain; batch will be rebuilt', { signature, err: JSON.stringify(status.err) });
    return false;
  }
  return status.confirmationStatus === 'confirmed' || status.confirmationStatus === 'finalized';
}

const B58 = '123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz';

/** Minimal base58 encoder so the keeper does not add a dependency just to print a signature. */
export function bs58Encode(bytes: Uint8Array): string {
  let value = 0n;
  for (const b of bytes) value = value * 256n + BigInt(b);
  let out = '';
  while (value > 0n) {
    const rem = Number(value % 58n);
    out = B58[rem] + out;
    value /= 58n;
  }
  for (const b of bytes) {
    if (b === 0) out = '1' + out;
    else break;
  }
  return out;
}

export { PublicKey };
