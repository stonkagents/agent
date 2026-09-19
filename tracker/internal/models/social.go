// Package: tracker/internal/models
// Feature: F-013 (Credits & Identity)
// Story: US-013-05 (Social Connections)
// Purpose: Domain models for social connections, wallets, and purchase intents

package models

import "time"

// Platform constants for social connections.
const (
	PlatformWallet   = "wallet"
	PlatformGitHub   = "github"
	PlatformTwitter  = "twitter"
	PlatformEmail    = "email"
	PlatformDiscord  = "discord"
	PlatformTelegram = "telegram"
	PlatformCalendar = "calendar"
)

// SocialConnection represents a verified social platform link.
type SocialConnection struct {
	ID             string    `json:"id"`
	AccountID      string    `json:"account_id"`
	Platform       string    `json:"platform"`
	PlatformUserID string    `json:"platform_user_id"`
	VerifiedAt     time.Time `json:"verified_at"`
	BonusGranted   int       `json:"bonus_granted"`
}

// AccountWallet represents a linked Solana wallet.
type AccountWallet struct {
	AccountID     string    `json:"account_id"`
	WalletAddress string    `json:"wallet_address"`
	Chain         string    `json:"chain"`
	LinkedAt      time.Time `json:"linked_at"`
	BalanceAtLink *int64    `json:"balance_at_link,omitempty"`
}

// WalletGrantHistory records a one-time wallet bonus grant.
type WalletGrantHistory struct {
	WalletAddress string    `json:"wallet_address"`
	Chain         string    `json:"chain"`
	GrantedAt     time.Time `json:"granted_at"`
	Amount        int       `json:"amount"`
}

// PurchaseIntentStatus constants.
const (
	PurchaseIntentPending  = "pending"
	PurchaseIntentVerified = "verified"
	PurchaseIntentExpired  = "expired"
)

// PurchaseIntent represents a two-step Solana purchase flow intent.
type PurchaseIntent struct {
	ID             string     `json:"id"`
	AccountID      string     `json:"account_id"`
	AmountLamports int64      `json:"amount_lamports"`
	CreditAmount   int        `json:"credit_amount"`
	Status         string     `json:"status"`
	CreatedAt      time.Time  `json:"created_at"`
	ExpiresAt      time.Time  `json:"expires_at"`
	TxSignature    string     `json:"tx_signature,omitempty"`
	VerifiedAt     *time.Time `json:"verified_at,omitempty"`
}
