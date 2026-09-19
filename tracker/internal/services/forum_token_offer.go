// Package services: Community board token offers that settle wallet to wallet (phase 1).
// Purpose: A post can offer a launched token per accepted reply. The tracker validates the
//          offer against token_launches at post time and, when the author pays a reply from
//          their wallet, verifies the SPL transfer on chain before counting it as paid.

package services

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/repository"
)

// Token offer limits and errors.
const (
	// MaxTokenOfferAccepts caps how many replies one offer can pay.
	MaxTokenOfferAccepts = 100
	// tokenOfferWalletChain is the wallet chain token offers settle on.
	tokenOfferWalletChain = "solana"
)

var (
	// ErrTokenOfferUnknownMint: the mint is not a launch recorded on this platform.
	ErrTokenOfferUnknownMint = errors.New("token offer mint is not a launch on this platform")
	// ErrTokenOfferNoWallet: the poster (at post time) or the reply author (at pay time) has no linked wallet.
	ErrTokenOfferNoWallet = errors.New("no linked wallet")
	// ErrTokenOfferNotAuthor: only the post author pays.
	ErrTokenOfferNotAuthor = errors.New("only the post author can pay a token offer")
	// ErrTokenOfferNone: the post has no settling token offer.
	ErrTokenOfferNone = errors.New("post has no token offer")
	// ErrTokenOfferOwnReply: the author cannot pay their own reply.
	ErrTokenOfferOwnReply = errors.New("cannot pay your own reply")
	// ErrTokenOfferExhausted: every offered payment was made.
	ErrTokenOfferExhausted = errors.New("token offer exhausted")
	// ErrTokenOfferAlreadyPaid: this reply was already paid.
	ErrTokenOfferAlreadyPaid = errors.New("reply already paid")
)

// TokenOfferTxError is the 422 TOKEN_OFFER_TX_INVALID error with the reason the transaction
// did not satisfy the offer.
type TokenOfferTxError struct{ Reason string }

func (e *TokenOfferTxError) Error() string { return "token offer transaction invalid: " + e.Reason }

// SetTokenOfferDeps wires what token offers need: launches to validate the mint, wallets to
// resolve linked wallets, payments for the ledger and the on-chain verifier.
func (s *ForumService) SetTokenOfferDeps(launches repository.LaunchRepository, wallets repository.WalletRepository, payments repository.TokenOfferPaymentRepository, verifier TokenTransferVerifier) {
	s.launches, s.wallets, s.payments, s.txVerifier = launches, wallets, payments, verifier
}

// applyTokenOffer validates a phase 1 token offer on a new post and fills its fields.
func (s *ForumService) applyTokenOffer(ctx context.Context, post *models.ForumPost, authorPeerID string, in CreatePostInput) error {
	if s.launches == nil {
		return ErrTokenOfferUnknownMint
	}
	mint := strings.TrimSpace(in.TokenOfferMint)
	if in.TokenOfferAmountRaw <= 0 || in.TokenOfferMax <= 0 || in.TokenOfferMax > MaxTokenOfferAccepts {
		return models.ErrInvalidInput
	}
	launch, err := s.launches.GetByMint(ctx, mint)
	if err != nil {
		if errors.Is(err, models.ErrNotFound) {
			return ErrTokenOfferUnknownMint
		}
		return fmt.Errorf("token offer: launch lookup: %w", err)
	}
	// Every poster needs a linked wallet, the token's agent included: the payment is sent from
	// that wallet and verified against it, so an offer without one could never be paid.
	if _, err := s.linkedWallet(ctx, authorPeerID); err != nil {
		return err
	}
	amount := int(in.TokenOfferAmountRaw)
	symbol := launch.Symbol
	decimals := LaunchTokenDecimals
	max := in.TokenOfferMax
	post.TokenOfferAmount = &amount
	post.TokenOfferToken = &symbol
	post.TokenOfferMint = &mint
	post.TokenOfferSymbol = &symbol
	post.TokenOfferDecimals = &decimals
	post.TokenOfferMax = &max
	post.TokenOfferPaid = 0
	return nil
}

// linkedWallet returns the peer's linked Solana wallet address, or ErrTokenOfferNoWallet.
func (s *ForumService) linkedWallet(ctx context.Context, peerID string) (string, error) {
	if s.accountRepo == nil || s.wallets == nil {
		return "", ErrTokenOfferNoWallet
	}
	account, err := s.accountRepo.GetByPeerID(ctx, peerID)
	if err != nil {
		if errors.Is(err, models.ErrNotFound) {
			return "", ErrTokenOfferNoWallet
		}
		return "", err
	}
	wallet, err := s.wallets.GetByAccountID(ctx, account.ID, tokenOfferWalletChain)
	if err != nil {
		if errors.Is(err, models.ErrNotFound) {
			return "", ErrTokenOfferNoWallet
		}
		return "", err
	}
	if wallet.WalletAddress == "" {
		return "", ErrTokenOfferNoWallet
	}
	return wallet.WalletAddress, nil
}

// LinkedWallets returns peer id -> linked wallet for the given peers (peers without one are
// absent). Used to expose author_wallet to a token offer post's author.
func (s *ForumService) LinkedWallets(ctx context.Context, peerIDs []string) map[string]string {
	out := make(map[string]string, len(peerIDs))
	for _, id := range peerIDs {
		if id == "" {
			continue
		}
		if _, done := out[id]; done {
			continue
		}
		if w, err := s.linkedWallet(ctx, id); err == nil {
			out[id] = w
		}
	}
	return out
}

// PaidReplyIDs returns the replies of postID that were paid (empty without a payment repo).
func (s *ForumService) PaidReplyIDs(ctx context.Context, postID string) map[string]bool {
	if s.payments == nil {
		return map[string]bool{}
	}
	out, err := s.payments.PaidReplyIDs(ctx, postID)
	if err != nil {
		return map[string]bool{}
	}
	return out
}

// TokenOfferProgram returns the token program of the post's offer mint (from its launch),
// empty when unknown.
func (s *ForumService) TokenOfferProgram(ctx context.Context, post *models.ForumPost) string {
	if s.launches == nil || !post.HasSettlingTokenOffer() {
		return ""
	}
	launch, err := s.launches.GetByMint(ctx, *post.TokenOfferMint)
	if err != nil {
		return ""
	}
	return LaunchTokenProgram(launch)
}

// PayTokenOffer verifies that signature paid replyID its offer (an SPL transfer of exactly the
// per-reply amount of the offer mint from the author's linked wallet to the reply author's
// linked wallet, confirmed, memo stonkagents:offer:<post>:<reply>, not seen before), records the
// payment, bumps the paid count and notifies the reply author (token_offer_paid).
func (s *ForumService) PayTokenOffer(ctx context.Context, authorPeerID, postID, replyID, signature string) (*models.TokenOfferPayment, error) {
	if s.payments == nil || s.txVerifier == nil {
		return nil, fmt.Errorf("token offers not configured")
	}
	signature = strings.TrimSpace(signature)
	if signature == "" {
		return nil, models.ErrInvalidInput
	}
	post, err := s.repo.GetPostByID(ctx, postID)
	if err != nil {
		return nil, err
	}
	if post.AuthorPeerID != authorPeerID {
		return nil, ErrTokenOfferNotAuthor
	}
	if !post.HasSettlingTokenOffer() {
		return nil, ErrTokenOfferNone
	}
	reply, err := s.repo.GetReplyByID(ctx, replyID)
	if err != nil || reply.PostID != postID {
		return nil, models.ErrNotFound
	}
	if reply.AuthorPeerID == authorPeerID {
		return nil, ErrTokenOfferOwnReply
	}
	if post.TokenOfferExhausted() {
		return nil, ErrTokenOfferExhausted
	}
	if paid, err := s.payments.PaidReplyIDs(ctx, postID); err == nil && paid[replyID] {
		return nil, ErrTokenOfferAlreadyPaid
	}
	if seen, err := s.payments.HasSignature(ctx, signature); err == nil && seen {
		return nil, &TokenOfferTxError{Reason: "signature already used"}
	}
	fromWallet, err := s.linkedWallet(ctx, authorPeerID)
	if err != nil {
		return nil, err
	}
	toWallet, err := s.linkedWallet(ctx, reply.AuthorPeerID)
	if err != nil {
		return nil, err
	}
	amount := int64(*post.TokenOfferAmount)
	res, err := s.txVerifier.VerifyTokenTransfer(ctx, TokenTransferVerifyParams{
		Signature: signature, Mint: *post.TokenOfferMint, FromWallet: fromWallet, ToWallet: toWallet,
		AmountRaw: amount, Memo: TokenOfferMemo(postID, replyID),
	})
	if err != nil {
		return nil, fmt.Errorf("token offer: verify transaction: %w", err)
	}
	if reason := s.checkTokenTransfer(ctx, post, res, amount); reason != "" {
		return nil, &TokenOfferTxError{Reason: reason}
	}
	// Reserve the slot first: the increment is conditional on paid < max, so two payments
	// racing for the last slot cannot both land. A payment that then fails to record (same
	// reply or signature paid concurrently) gives the slot back.
	if _, err := s.repo.IncrementTokenOfferPaid(ctx, postID); err != nil {
		if errors.Is(err, models.ErrInvalidInput) {
			return nil, ErrTokenOfferExhausted
		}
		return nil, err
	}
	payment := &models.TokenOfferPayment{
		Signature: signature, PostID: postID, ReplyID: replyID, FromWallet: fromWallet, ToWallet: toWallet,
		AmountRaw: amount, VerifiedAt: s.now(),
	}
	if err := s.payments.Create(ctx, payment); err != nil {
		if derr := s.repo.DecrementTokenOfferPaid(ctx, postID); derr != nil {
			slog.Warn("[forum] token offer slot not released", "post", postID, "error", derr)
		}
		if errors.Is(err, models.ErrAlreadyExists) {
			return nil, ErrTokenOfferAlreadyPaid
		}
		return nil, err
	}
	amt := int(amount)
	symbol := ""
	if post.TokenOfferSymbol != nil {
		symbol = *post.TokenOfferSymbol
	}
	s.emitActivityRow(ctx, &models.BoardActivity{
		PeerID: reply.AuthorPeerID, Kind: models.ActivityTokenOfferPaid, PostID: postID, ReplyID: &replyID,
		ActorPeerID: authorPeerID, Amount: &amt, Symbol: symbol,
	})
	return payment, nil
}

// checkTokenTransfer applies the offer rules to what the verifier saw; "" means it passes.
// The sender must have paid exactly amount; the recipient must have received it minus at most
// the launch's transfer fee (Token-2022 withholds the fee on the receiving side).
func (s *ForumService) checkTokenTransfer(ctx context.Context, post *models.ForumPost, res *TokenTransferVerifyResult, amount int64) string {
	switch {
	case !res.Found:
		return "transaction not found or not confirmed"
	case !res.Succeeded:
		return "transaction failed on chain"
	case !res.FromSigner:
		return "poster wallet did not sign the transaction"
	case res.FromDebit != amount:
		return fmt.Sprintf("expected %d raw units from the poster wallet, saw %d", amount, res.FromDebit)
	case !res.MemoMatch:
		return "memo does not match stonkagents:offer:<post id>:<reply id>"
	}
	minCredit := amount
	if s.launches != nil {
		if launch, err := s.launches.GetByMint(ctx, *post.TokenOfferMint); err == nil && launch.TransferFeeBps > 0 {
			fee := (amount*int64(launch.TransferFeeBps) + 9999) / 10000
			minCredit = amount - fee
		}
	}
	if res.ToCredit < minCredit || res.ToCredit > amount {
		return fmt.Sprintf("expected the reply author wallet to receive %d raw units (less the transfer fee), saw %d", amount, res.ToCredit)
	}
	return ""
}
