import { describe, expect, it } from 'vitest';
import { Connection, Keypair, PublicKey } from '@solana/web3.js';
import { ACCOUNT_SIZE, AccountLayout, AccountState, TOKEN_2022_PROGRAM_ID } from '@solana/spl-token';
import {
  buildHolderSnapshot, buildWithdrawBatches, scanTokenAccounts, sumWithheld, withheldSources,
  type ScannedAccount,
} from '../src/token2022.js';
import { chunkArray } from '../src/prices.js';

const MINT = new PublicKey('So11111111111111111111111111111111111111112');

interface AccountSpec {
  address: string;
  owner: string;
  amountRaw?: bigint;
  withheldRaw?: bigint;
  frozen?: boolean;
  programOwned?: boolean;
}

function account(spec: AccountSpec): ScannedAccount {
  return {
    address: new PublicKey(spec.address),
    owner: new PublicKey(spec.owner),
    amountRaw: spec.amountRaw ?? 0n,
    withheldRaw: spec.withheldRaw ?? 0n,
    frozen: spec.frozen ?? false,
    programOwned: spec.programOwned ?? false,
  };
}

const alice = Keypair.generate().publicKey.toBase58();
const bob = Keypair.generate().publicKey.toBase58();
const ataA1 = Keypair.generate().publicKey.toBase58();
const ataA2 = Keypair.generate().publicKey.toBase58();
const ataB = Keypair.generate().publicKey.toBase58();
const vault = Keypair.generate().publicKey.toBase58();
const pda = PublicKey.findProgramAddressSync([Buffer.from('vault')], TOKEN_2022_PROGRAM_ID)[0].toBase58();
const pdaAta = Keypair.generate().publicKey.toBase58();

describe('buildHolderSnapshot', () => {
  it('sums every token account of one owner into a single recipient', () => {
    const res = buildHolderSnapshot(
      [
        account({ address: ataA1, owner: alice, amountRaw: 100n }),
        account({ address: ataA2, owner: alice, amountRaw: 250n }),
        account({ address: ataB, owner: bob, amountRaw: 50n }),
      ],
      { exclude: [], mint: MINT },
    );
    expect(res.holders).toHaveLength(2);
    const aliceHolder = res.holders.find((h) => h.owner === alice);
    expect(aliceHolder?.balanceRaw).toBe(350n);
    expect(aliceHolder?.account).toBe([ataA1, ataA2].sort()[0]);
  });

  it('drops the pool vault and any configured exclusion', () => {
    const res = buildHolderSnapshot(
      [account({ address: vault, owner: alice, amountRaw: 9_000n }), account({ address: ataB, owner: bob, amountRaw: 50n })],
      { exclude: [vault], mint: MINT },
    );
    expect(res.holders.map((h) => h.owner)).toEqual([bob]);
    expect(res.skipped[0]).toMatchObject({ account: vault, reason: 'excluded' });
  });

  it('drops program-owned accounts by default and keeps them when asked', () => {
    const accounts = [account({ address: pdaAta, owner: pda, amountRaw: 1_000n, programOwned: true })];
    expect(buildHolderSnapshot(accounts, { exclude: [], mint: MINT }).holders).toHaveLength(0);
    expect(buildHolderSnapshot(accounts, { exclude: [], mint: MINT }).skipped[0]?.reason).toBe('program-owned');
    expect(buildHolderSnapshot(accounts, { exclude: [], mint: MINT, includeProgramOwned: true }).holders).toHaveLength(1);
  });

  it('never treats the mint itself as a holder', () => {
    const res = buildHolderSnapshot([account({ address: MINT.toBase58(), owner: alice, amountRaw: 5n })], {
      exclude: [],
      mint: MINT,
    });
    expect(res.holders).toHaveLength(0);
  });

  it('ignores zero-balance accounts without listing them as skipped', () => {
    const res = buildHolderSnapshot([account({ address: ataB, owner: bob, amountRaw: 0n })], { exclude: [], mint: MINT });
    expect(res.holders).toHaveLength(0);
    expect(res.skipped).toHaveLength(0);
  });
});

describe('withheld fee batching', () => {
  const accounts = [
    account({ address: ataA1, owner: alice, withheldRaw: 10n }),
    account({ address: ataA2, owner: alice, withheldRaw: 0n }),
    account({ address: ataB, owner: bob, withheldRaw: 300n }),
    account({ address: vault, owner: alice, withheldRaw: 25n }),
  ];

  it('keeps only accounts carrying withheld tax, largest first', () => {
    expect(withheldSources(accounts).map((a) => a.withheldRaw)).toEqual([300n, 25n, 10n]);
  });

  it('totals the withheld tax across accounts', () => {
    expect(sumWithheld(accounts)).toBe(335n);
  });

  it('splits sources into transaction-sized batches with per-batch totals', () => {
    const batches = buildWithdrawBatches(withheldSources(accounts), 2);
    expect(batches).toHaveLength(2);
    expect(batches[0]?.sources).toHaveLength(2);
    expect(batches[0]?.withheldRaw).toBe(325n);
    expect(batches[1]?.withheldRaw).toBe(10n);
    expect(batches.map((b) => b.index)).toEqual([0, 1]);
  });

  it('produces no batches when nothing is withheld', () => {
    expect(buildWithdrawBatches([], 20)).toEqual([]);
  });
});

describe('chunkArray', () => {
  it('splits evenly and keeps the tail', () => {
    expect(chunkArray([1, 2, 3, 4, 5], 2)).toEqual([[1, 2], [3, 4], [5]]);
    expect(chunkArray([], 3)).toEqual([]);
    expect(() => chunkArray([1], 0)).toThrow(/chunk size/);
  });
});

describe('scanTokenAccounts against a mocked RPC', () => {
  function encodeAccount(owner: PublicKey, amount: bigint, mint = MINT): Buffer {
    const data = Buffer.alloc(ACCOUNT_SIZE);
    AccountLayout.encode(
      {
        mint,
        owner,
        amount,
        delegateOption: 0,
        delegate: PublicKey.default,
        state: AccountState.Initialized,
        isNativeOption: 0,
        isNative: 0n,
        delegatedAmount: 0n,
        closeAuthorityOption: 0,
        closeAuthority: PublicKey.default,
      },
      data,
    );
    return data;
  }

  function mockConnection(entries: { pubkey: PublicKey; data: Buffer }[]): Connection {
    return {
      getProgramAccounts: async () =>
        entries.map((e) => ({
          pubkey: e.pubkey,
          account: { data: e.data, executable: false, lamports: 1, owner: TOKEN_2022_PROGRAM_ID, rentEpoch: 0 },
        })),
    } as unknown as Connection;
  }

  it('decodes balances, owners and program-owned flags', async () => {
    const humanOwner = new PublicKey(alice);
    const programOwner = new PublicKey(pda);
    const connection = mockConnection([
      { pubkey: new PublicKey(ataA1), data: encodeAccount(humanOwner, 500n) },
      { pubkey: new PublicKey(pdaAta), data: encodeAccount(programOwner, 900n) },
    ]);
    const scanned = await scanTokenAccounts(connection, MINT);
    expect(scanned).toHaveLength(2);
    const human = scanned.find((s) => s.owner.equals(humanOwner));
    expect(human?.amountRaw).toBe(500n);
    expect(human?.programOwned).toBe(false);
    expect(scanned.find((s) => s.owner.equals(programOwner))?.programOwned).toBe(true);
  });

  it('skips accounts of another mint and undecodable data', async () => {
    const other = Keypair.generate().publicKey;
    const connection = mockConnection([
      { pubkey: new PublicKey(ataA1), data: encodeAccount(new PublicKey(alice), 1n, other) },
      { pubkey: new PublicKey(ataB), data: Buffer.alloc(12) },
    ]);
    expect(await scanTokenAccounts(connection, MINT)).toEqual([]);
  });

  it('returns accounts in a stable address order', async () => {
    const connection = mockConnection([
      { pubkey: new PublicKey(ataB), data: encodeAccount(new PublicKey(bob), 1n) },
      { pubkey: new PublicKey(ataA1), data: encodeAccount(new PublicKey(alice), 2n) },
    ]);
    const scanned = await scanTokenAccounts(connection, MINT);
    const addresses = scanned.map((s) => s.address.toBase58());
    expect(addresses).toEqual([...addresses].sort());
  });
});
