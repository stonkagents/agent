// Chain steps for the holder-tax distribution: sweep the withheld Token-2022 fees into the
// distribution wallet, then pay holders in the quote asset in batched transfers.

import { Connection, Keypair, PublicKey, TransactionInstruction } from '@solana/web3.js';
import {
  createAssociatedTokenAccountIdempotentInstruction,
  createHarvestWithheldTokensToMintInstruction,
  createTransferCheckedInstruction,
  createWithdrawWithheldTokensFromAccountsInstruction,
  createWithdrawWithheldTokensFromMintInstruction,
  getAssociatedTokenAddressSync,
  TOKEN_2022_PROGRAM_ID,
} from '@solana/spl-token';
import type { KeeperConfig } from './env.js';
import { sendInstructions } from './tx.js';
import { tokenBalance } from './jupiter.js';
import { log } from './log.js';
import type { WithdrawBatch } from './token2022.js';
import type { RecipientRecord } from './state.js';

export interface AtaPlan {
  address: PublicKey;
  /** Present when the account does not exist yet and the batch must create it. */
  createIx?: TransactionInstruction;
}

/** ATA for `owner`, with an idempotent create instruction when it is missing on chain. */
export async function planAta(
  connection: Connection,
  payer: PublicKey,
  owner: PublicKey,
  mint: PublicKey,
  program: PublicKey,
): Promise<AtaPlan> {
  const address = getAssociatedTokenAddressSync(mint, owner, true, program);
  const info = await connection.getAccountInfo(address);
  if (info) return { address };
  return {
    address,
    createIx: createAssociatedTokenAccountIdempotentInstruction(payer, address, owner, mint, program),
  };
}

/** Same, in one RPC round trip for many owners. Order matches `owners`. */
export async function planAtas(
  connection: Connection,
  payer: PublicKey,
  owners: PublicKey[],
  mint: PublicKey,
  program: PublicKey,
): Promise<AtaPlan[]> {
  const addresses = owners.map((o) => getAssociatedTokenAddressSync(mint, o, true, program));
  const infos = addresses.length ? await connection.getMultipleAccountsInfo(addresses) : [];
  return addresses.map((address, i) => {
    const owner = owners[i];
    if (infos[i] || !owner) return { address };
    return {
      address,
      createIx: createAssociatedTokenAccountIdempotentInstruction(payer, address, owner, mint, program),
    };
  });
}

export interface WithdrawOutcome {
  signatures: string[];
  /** Measured increase of the destination account, not the sum of the withheld figures. */
  withdrawnRaw: bigint;
  failedBatches: { index: number; error: string }[];
  harvested: number;
}

/**
 * Sweeps withheld transfer fees into `destination`.
 *
 * Instruction sequence:
 *   per batch  : TransferFeeExtension WithdrawWithheldTokensFromAccounts(mint, destination, authority, sources[])
 *   on failure : TransferFeeExtension HarvestWithheldTokensToMint(mint, sources[])   [permissionless]
 *   finally    : TransferFeeExtension WithdrawWithheldTokensFromMint(mint, destination, authority)
 *
 * A frozen or closed source account makes the whole batch fail, so a failed batch is harvested to
 * the mint instead and collected by the final withdraw-from-mint.
 */
export async function withdrawWithheld(args: {
  cfg: KeeperConfig;
  connection: Connection;
  mint: PublicKey;
  destination: PublicKey;
  authority: Keypair;
  payer: Keypair;
  batches: WithdrawBatch[];
  withheldOnMintRaw: bigint;
  tokenProgram?: PublicKey;
}): Promise<WithdrawOutcome> {
  const program = args.tokenProgram ?? TOKEN_2022_PROGRAM_ID;
  const before = await tokenBalance(args.connection, args.destination);
  const signatures: string[] = [];
  const failedBatches: WithdrawOutcome['failedBatches'] = [];
  const harvestSources: PublicKey[] = [];

  for (const batch of args.batches) {
    const ix = createWithdrawWithheldTokensFromAccountsInstruction(
      args.mint,
      args.destination,
      args.authority.publicKey,
      [],
      batch.sources,
      program,
    );
    try {
      const sig = await sendInstructions(args.cfg, args.connection, args.payer, [ix], signerFor(args.payer, args.authority));
      signatures.push(sig);
      log.info('withheld fees withdrawn', { signature: sig, batch: batch.index, sources: batch.sources.length, withheldRaw: batch.withheldRaw });
    } catch (e) {
      const error = (e as Error).message;
      failedBatches.push({ index: batch.index, error });
      harvestSources.push(...batch.sources);
      log.warn('withdraw batch failed; will harvest to mint instead', { batch: batch.index, error });
    }
  }

  let harvested = 0;
  for (let i = 0; i < harvestSources.length; i += args.cfg.WITHDRAW_BATCH_SIZE) {
    const chunk = harvestSources.slice(i, i + args.cfg.WITHDRAW_BATCH_SIZE);
    try {
      const sig = await sendInstructions(args.cfg, args.connection, args.payer, [
        createHarvestWithheldTokensToMintInstruction(args.mint, chunk, program),
      ]);
      signatures.push(sig);
      harvested += chunk.length;
      log.info('withheld fees harvested to mint', { signature: sig, sources: chunk.length });
    } catch (e) {
      log.warn('harvest batch failed', { error: (e as Error).message });
    }
  }

  if (args.withheldOnMintRaw > 0n || harvested > 0) {
    try {
      const sig = await sendInstructions(
        args.cfg,
        args.connection,
        args.payer,
        [createWithdrawWithheldTokensFromMintInstruction(args.mint, args.destination, args.authority.publicKey, [], program)],
        signerFor(args.payer, args.authority),
      );
      signatures.push(sig);
      log.info('withheld fees withdrawn from mint', { signature: sig });
    } catch (e) {
      log.warn('withdraw-from-mint failed', { error: (e as Error).message });
    }
  }

  const after = await tokenBalance(args.connection, args.destination);
  return { signatures, withdrawnRaw: after > before ? after - before : 0n, failedBatches, harvested };
}

/**
 * Instructions for one payout batch: create any missing recipient ATA, then one
 * TransferChecked per recipient from the distribution wallet's quote account.
 */
export async function buildPayoutInstructions(args: {
  connection: Connection;
  payer: PublicKey;
  source: PublicKey;
  quoteMint: PublicKey;
  quoteProgram: PublicKey;
  quoteDecimals: number;
  recipients: RecipientRecord[];
}): Promise<TransactionInstruction[]> {
  const owners = args.recipients.map((r) => new PublicKey(r.owner));
  const plans = await planAtas(args.connection, args.payer, owners, args.quoteMint, args.quoteProgram);
  const ixs: TransactionInstruction[] = [];
  for (const plan of plans) if (plan.createIx) ixs.push(plan.createIx);
  for (let i = 0; i < args.recipients.length; i++) {
    const recipient = args.recipients[i];
    const plan = plans[i];
    if (!recipient || !plan) continue;
    ixs.push(
      createTransferCheckedInstruction(
        args.source,
        args.quoteMint,
        plan.address,
        args.payer,
        BigInt(recipient.amountRaw),
        args.quoteDecimals,
        [],
        args.quoteProgram,
      ),
    );
  }
  return ixs;
}

function signerFor(payer: Keypair, authority: Keypair): Keypair[] {
  return payer.publicKey.equals(authority.publicKey) ? [] : [authority];
}
