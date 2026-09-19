// Package services: community board phase 3 (Autopilot v2) on the forum service: bounty
// negotiation (raise a bounty for the repliers who asked) and the autopilot outcome summary.
// The reply ask itself lives in CreateReplyWithAsk (forum_service.go).
package services

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
)

// RaiseBounty raises the author's open bounty to amount, or creates one on a post without a
// bounty (DefaultBountyDays deadline), escrowing the difference through the same escrow path
// CreatePost uses (SpendSplit, reason bounty_escrow). The raise is claimed first with one
// conditional update (no bounty, or an open one below amount), so a double submit escrows
// once: the second claim finds nothing to update and spends nothing. The escrow follows the
// claim; when it fails the claim is reverted. The post keeps its first escrow request id so
// the expiry refund returns the whole amount at once; bounty_escrow_paid accumulates the
// paid part. Every replier with an ask gets a bounty_raised activity (amount = new bounty,
// no actor: a system row; the author never gets one, even with an ask). Errors:
// ErrBountyNotAuthor, ErrBountyNotOpen (completed or expired bounty), ErrInsufficientCredits,
// models.ErrInvalidInput (amount not above the current bounty, a concurrent raise included,
// or above MaxBountyAmount).
func (s *ForumService) RaiseBounty(ctx context.Context, authorPeerID, postID string, amount int) (*models.ForumPost, error) {
	post, err := s.repo.GetPostByID(ctx, postID)
	if err != nil {
		return nil, err
	}
	if post.AuthorPeerID != authorPeerID {
		return nil, ErrBountyNotAuthor
	}
	if post.HasBounty() && post.BountyStatus != "open" {
		return nil, ErrBountyNotOpen
	}
	if amount <= 0 || amount > MaxBountyAmount {
		return nil, models.ErrInvalidInput
	}
	now := s.now()
	requestID := fmt.Sprintf("bounty_raise:%s:%d:%d", postID, amount, now.UnixNano())
	prev, err := s.repo.RaiseBounty(ctx, postID, amount, requestID, now.Add(DefaultBountyDays*24*time.Hour))
	if err != nil {
		if errors.Is(err, models.ErrInvalidInput) {
			// Re-read: a bounty that is no longer open is 409, anything else (amount not above
			// the current one, a concurrent raise that landed first) is 400.
			if cur, rerr := s.repo.GetPostByID(ctx, postID); rerr == nil && cur.HasBounty() && cur.BountyStatus != "open" {
				return nil, ErrBountyNotOpen
			}
			return nil, models.ErrInvalidInput
		}
		return nil, err
	}
	if s.accountRepo != nil && s.creditRepo != nil {
		account, err := s.accountRepo.GetByPeerID(ctx, authorPeerID)
		if err != nil {
			s.revertBountyRaise(ctx, postID, prev)
			return nil, fmt.Errorf("bounty raise: resolve account: %w", err)
		}
		_, fromPaid, err := s.creditRepo.SpendSplit(ctx, account.ID, amount-prev, "bounty_escrow", requestID)
		if err != nil {
			s.revertBountyRaise(ctx, postID, prev)
			return nil, ErrInsufficientCredits
		}
		if err := s.repo.SetBountyEscrowPaid(ctx, postID, post.BountyEscrowPaid+fromPaid); err != nil {
			slog.Warn("[forum] bounty raise escrow split not recorded", "post", postID, "error", err)
		}
	}
	updated, err := s.repo.GetPostByID(ctx, postID)
	if err != nil {
		return nil, err
	}
	askers, err := s.repo.AskerPeerIDs(ctx, postID)
	if err != nil {
		slog.Warn("[forum] bounty raise askers lookup failed", "post", postID, "error", err)
		return updated, nil
	}
	// A system row (empty actor) like bounty_expiring: the raise is the post's news, not a
	// peer's action on the asker.
	for _, peer := range askers {
		if peer == authorPeerID {
			continue
		}
		amt := amount
		s.emitActivity(ctx, peer, models.ActivityBountyRaised, postID, nil, "", &amt)
	}
	return updated, nil
}

// revertBountyRaise puts the bounty back after a failed escrow. A failure here is logged: the
// claim then stands without its escrow and needs an operator (the log line names the post).
func (s *ForumService) revertBountyRaise(ctx context.Context, postID string, prev int) {
	if err := s.repo.RevertBountyRaise(ctx, postID, prev); err != nil {
		slog.Warn("[forum] bounty raise not reverted after a failed escrow", "post", postID, "prev", prev, "error", err)
	}
}

// AutopilotOutcomeSummary aggregates the peer's auto replies over AutopilotOutcomeWindow:
// how many were posted, upvoted (post upvotes > 0), accepted, awarded, hidden and reported,
// the credits the drafts cost (completions that carried the autopilot marker) and the credits
// won (bounties awarded), plus posted and won (accepted or awarded) per post category.
func (s *ForumService) AutopilotOutcomeSummary(ctx context.Context, peerID string) (*models.AutopilotOutcomeSummary, error) {
	outcomes, err := s.ListAutopilotOutcomes(ctx, peerID, nil)
	if err != nil {
		return nil, err
	}
	sum := &models.AutopilotOutcomeSummary{ByCategory: map[string]*models.AutopilotCategoryOutcome{}}
	for _, o := range outcomes {
		sum.Posted++
		if o.Upvotes > 0 {
			sum.Upvoted++
		}
		if o.Accepted {
			sum.Accepted++
		}
		if o.Awarded {
			sum.Awarded++
		}
		if o.Hidden {
			sum.Hidden++
		}
		if o.Reported {
			sum.Reported++
		}
		sum.CreditsWon += o.AwardedAmount()
		cat := sum.ByCategory[o.Category]
		if cat == nil {
			cat = &models.AutopilotCategoryOutcome{}
			sum.ByCategory[o.Category] = cat
		}
		cat.Posted++
		if o.Accepted || o.Awarded {
			cat.Won++
		}
	}
	if s.accountRepo != nil && s.creditRepo != nil {
		account, err := s.accountRepo.GetByPeerID(ctx, peerID)
		if err == nil {
			spent, err := s.creditRepo.SumSpentByReasonsSince(ctx, account.ID, AutopilotCompletionReasons, s.now().Add(-AutopilotOutcomeWindow))
			if err != nil {
				return nil, fmt.Errorf("autopilot summary: credits spent: %w", err)
			}
			sum.CreditsSpent = spent
		} else if !errors.Is(err, models.ErrNotFound) {
			return nil, fmt.Errorf("autopilot summary: resolve account: %w", err)
		}
	}
	return sum, nil
}
