// Package: tracker/internal/solana/tx
// Feature: StonkAgents devnet drip
// Purpose: The three instructions the drip needs — System transfer, Associated Token Account
//          CreateIdempotent and SPL Token Transfer — plus ATA derivation.

package tx

import (
	"encoding/binary"

	"github.com/stonkagents/agent/tracker/internal/solana/pda"
)

// Well-known program ids.
var (
	SystemProgramID          = MustPubkey("11111111111111111111111111111111")
	TokenProgramID           = MustPubkey("TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA")
	AssociatedTokenProgramID = MustPubkey("ATokenGPvbdGVxr1b2hvZbsiqW5xWH25efTNsLJA8knL")
)

const (
	systemInstructionTransfer      uint32 = 2
	tokenInstructionTransfer       byte   = 3
	ataInstructionCreateIdempotent byte   = 1
)

// SystemTransfer moves lamports from one system account to another.
func SystemTransfer(from, to Pubkey, lamports uint64) Instruction {
	data := make([]byte, 12)
	binary.LittleEndian.PutUint32(data[0:4], systemInstructionTransfer)
	binary.LittleEndian.PutUint64(data[4:12], lamports)
	return Instruction{
		ProgramID: SystemProgramID,
		Accounts: []AccountMeta{
			{Pubkey: from, IsSigner: true, IsWritable: true},
			{Pubkey: to, IsWritable: true},
		},
		Data: data,
	}
}

// CreateAssociatedTokenAccountIdempotent creates owner's ATA for mint if it does not exist
// (no-op otherwise). payer funds the rent.
func CreateAssociatedTokenAccountIdempotent(payer, ata, owner, mint, tokenProgram Pubkey) Instruction {
	return Instruction{
		ProgramID: AssociatedTokenProgramID,
		Accounts: []AccountMeta{
			{Pubkey: payer, IsSigner: true, IsWritable: true},
			{Pubkey: ata, IsWritable: true},
			{Pubkey: owner},
			{Pubkey: mint},
			{Pubkey: SystemProgramID},
			{Pubkey: tokenProgram},
		},
		Data: []byte{ataInstructionCreateIdempotent},
	}
}

// TokenTransfer moves amount (raw units) from source to dest, authorised by owner.
func TokenTransfer(source, dest, owner Pubkey, amount uint64, tokenProgram Pubkey) Instruction {
	data := make([]byte, 9)
	data[0] = tokenInstructionTransfer
	binary.LittleEndian.PutUint64(data[1:9], amount)
	return Instruction{
		ProgramID: tokenProgram,
		Accounts: []AccountMeta{
			{Pubkey: source, IsWritable: true},
			{Pubkey: dest, IsWritable: true},
			{Pubkey: owner, IsSigner: true},
		},
		Data: data,
	}
}

// FindAssociatedTokenAddress derives owner's ATA for mint under tokenProgram:
// findProgramAddress([owner, tokenProgram, mint], AssociatedTokenProgramID).
func FindAssociatedTokenAddress(owner, mint, tokenProgram Pubkey) (Pubkey, error) {
	addr, _, err := pda.FindProgramAddress([][]byte{owner[:], tokenProgram[:], mint[:]}, [32]byte(AssociatedTokenProgramID))
	if err != nil {
		return Pubkey{}, err
	}
	return Pubkey(addr), nil
}
