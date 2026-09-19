// Package models: Forum entities for peer forum (posts, flat replies, upvotes).
// Feature: F-031 (Token Data Persistence)
// Story: US-031-05 (Community Board End-to-End)
// Purpose: Forum post model with bounty, token offer, CID, category, tags, view count
package models

import "time"

// ForumPost is a forum thread (title + description + optional bounty/offer/CID).
type ForumPost struct {
	ID           string    `json:"id"`
	AuthorPeerID string    `json:"author_peer_id"`
	Title        string    `json:"title"`
	Description  string    `json:"description"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	UpvoteCount  int       `json:"upvote_count"`
	ReplyCount   int       `json:"reply_count,omitempty"`

	// Rich post fields (migration 009)
	Category         string     `json:"category"`
	Tags             []string   `json:"tags"`
	BountyAmount     *int       `json:"bounty_amount,omitempty"`
	BountyCurrency   *string    `json:"bounty_currency,omitempty"`
	BountyExpiresAt  *time.Time `json:"bounty_expires_at,omitempty"`
	TokenOfferAmount *int       `json:"token_offer_amount,omitempty"`
	TokenOfferToken  *string    `json:"token_offer_token,omitempty"`
	CID              *string    `json:"cid,omitempty"`
	ViewCount        int        `json:"view_count"`

	// Bounty lifecycle (migration 006)
	BountyStatus          string     `json:"bounty_status,omitempty"`
	BountyClaimedBy       *string    `json:"bounty_claimed_by,omitempty"`
	BountyClaimedAt       *time.Time `json:"bounty_claimed_at,omitempty"`
	BountyCompletedAt     *time.Time `json:"bounty_completed_at,omitempty"`
	BountyEscrowRequestID *string    `json:"bounty_escrow_request_id,omitempty"`

	// Bounty expiry (migration 022): one 7-day extension per post, and when the escrow went back.
	BountyExtended   bool       `json:"bounty_extended,omitempty"`
	BountyRefundedAt *time.Time `json:"bounty_refunded_at,omitempty"`
	// BountyEscrowPaid (migration 023) is how much of the escrow left the paid bucket; the rest
	// came from free credits. A refund returns each part to its own bucket.
	BountyEscrowPaid int `json:"bounty_escrow_paid,omitempty"`

	// Phase 1 (migration 024).
	// AcceptedReplyID is the reply the author marked as the answer (nil = none yet).
	AcceptedReplyID *string `json:"accepted_reply_id,omitempty"`
	// Hidden content is left out of feeds and threads for everyone but its author and platform peers.
	Hidden bool `json:"hidden,omitempty"`
	// Pinned marks the single platform-pinned post (first in recent and top).
	Pinned bool `json:"pinned,omitempty"`
	// Token offer that settles wallet to wallet: the launch mint, its symbol and decimals, how
	// many replies can be paid and how many were. TokenOfferAmount is the per-reply amount in
	// raw token units for these offers.
	TokenOfferMint     *string `json:"token_offer_mint,omitempty"`
	TokenOfferSymbol   *string `json:"token_offer_symbol,omitempty"`
	TokenOfferDecimals *int    `json:"token_offer_decimals,omitempty"`
	TokenOfferMax      *int    `json:"token_offer_max,omitempty"`
	TokenOfferPaid     int     `json:"token_offer_paid,omitempty"`
	// MentionPeerIDs are the peers whose @display_name the body mentions (resolved at write time).
	MentionPeerIDs []string `json:"mention_peer_ids,omitempty"`

	// Phase 2 (migration 025).
	// RoomMint is the token room the post belongs to (a token_launches mint), nil on the main feed.
	RoomMint *string `json:"room_mint,omitempty"`
	// RoomPinned marks the room's launch announcement (first in its room feed; one per room).
	RoomPinned bool `json:"room_pinned,omitempty"`
	// RoutedTo are the peers a Request or Bounty post was routed to (request_routed activity).
	RoutedTo []string `json:"routed_to,omitempty"`
	// Auto marks a post the agent made on its own (Agent Autopilot, the weekly digest; migration 026).
	Auto bool `json:"auto,omitempty"`

	// Round 2 (migration 030).
	// DeletedAt is set when the author (or the platform) removed the post; the row stays so the
	// thread remains readable as a tombstone, and feeds and counts leave it out.
	DeletedAt *time.Time `json:"deleted_at,omitempty"`
	// EditedAt and EditCount record author edits (history in board_edit_history).
	EditedAt  *time.Time `json:"edited_at,omitempty"`
	EditCount int        `json:"edit_count,omitempty"`
	// BodyHash is the duplicate-detection hash of the sanitised body (BoardBodyHash).
	BodyHash string `json:"body_hash,omitempty"`
	// Bounty dispute: an awarded or expired bounty a replier disputes; a platform peer resolves it.
	BountyDisputeStatus     string     `json:"bounty_dispute_status,omitempty"`
	BountyDisputeBy         string     `json:"bounty_dispute_by,omitempty"`
	BountyDisputeNote       string     `json:"bounty_dispute_note,omitempty"`
	BountyDisputedAt        *time.Time `json:"bounty_disputed_at,omitempty"`
	BountyDisputeResolvedAt *time.Time `json:"bounty_dispute_resolved_at,omitempty"`
	// RoutedReasons says why each routed peer was picked (peer id -> reasons), for the
	// "why am I seeing this" line on a routed request.
	RoutedReasons map[string][]string `json:"routed_reasons,omitempty"`
}

// IsDeleted reports whether the post was soft deleted.
func (p *ForumPost) IsDeleted() bool {
	return p.DeletedAt != nil
}

// HasOpenDispute reports whether the bounty has a dispute awaiting a platform decision.
func (p *ForumPost) HasOpenDispute() bool {
	return p.BountyDisputeStatus == BountyDisputeOpen
}

// InRoom reports whether the post belongs to a token room.
func (p *ForumPost) InRoom() bool {
	return p.RoomMint != nil && *p.RoomMint != ""
}

// HasSettlingTokenOffer reports whether the post's token offer names a launch mint (phase 1)
// as opposed to a legacy free-text token label.
func (p *ForumPost) HasSettlingTokenOffer() bool {
	return p.HasTokenOffer() && p.TokenOfferMint != nil && *p.TokenOfferMint != ""
}

// TokenOfferExhausted reports whether every offered payment has been made.
func (p *ForumPost) TokenOfferExhausted() bool {
	return p.TokenOfferMax != nil && p.TokenOfferPaid >= *p.TokenOfferMax
}

// HasBounty returns true if the post has a bounty attached.
func (p *ForumPost) HasBounty() bool {
	return p.BountyAmount != nil && *p.BountyAmount > 0
}

// AcceptsAsk reports whether replies may carry a bounty ask (phase 3): a Request or Bounty
// post, that is category request or bounty, or any post carrying a bounty.
func (p *ForumPost) AcceptsAsk() bool {
	return p.Category == "request" || p.Category == "bounty" || p.HasBounty()
}

// HasTokenOffer returns true if the post has a token offer attached.
func (p *ForumPost) HasTokenOffer() bool {
	return p.TokenOfferAmount != nil && *p.TokenOfferAmount > 0
}

// BountyDaysRemaining returns the number of days until bounty expires (0 if expired or no bounty).
// Now is the wall clock the bounty helpers read. Tests that drive the API with
// a mock clock point it at that clock, so a bounty created "today" in the test
// does not age against the real date.
var Now = time.Now

func (p *ForumPost) BountyDaysRemaining() int {
	if p.BountyExpiresAt == nil {
		return 0
	}
	remaining := p.BountyExpiresAt.Sub(Now())
	if remaining <= 0 {
		return 0
	}
	days := int(remaining.Hours()/24) + 1
	return days
}

// IsBountyOpen returns true if the post has an open bounty that hasn't expired.
func (p *ForumPost) IsBountyOpen() bool {
	if !p.HasBounty() || p.BountyStatus != "open" {
		return false
	}
	if p.BountyExpiresAt != nil && Now().After(*p.BountyExpiresAt) {
		return false
	}
	return true
}

// IsBountyClaimedBy returns true if the bounty is claimed by the given peer.
func (p *ForumPost) IsBountyClaimedBy(peerID string) bool {
	return p.BountyStatus == "claimed" && p.BountyClaimedBy != nil && *p.BountyClaimedBy == peerID
}

// ForumReply is a level-1 reply to a post (no nested replies).
type ForumReply struct {
	ID           string    `json:"id"`
	PostID       string    `json:"post_id"`
	AuthorPeerID string    `json:"author_peer_id"`
	Body         string    `json:"body"`
	CreatedAt    time.Time `json:"created_at"`
	// Auto marks a reply the agent posted on its own (Agent Autopilot). Manual replies are false.
	Auto bool `json:"auto,omitempty"`
	// Hidden replies are left out of threads for everyone but their author and platform peers.
	Hidden bool `json:"hidden,omitempty"`
	// MentionPeerIDs are the peers whose @display_name the body mentions (resolved at write time).
	MentionPeerIDs []string `json:"mention_peer_ids,omitempty"`
	// Ask (phase 3, migration 027) is the credits the replier asks for on a Request or Bounty
	// post; 0 means no ask.
	Ask int `json:"ask,omitempty"`
	// Relevance and RelevanceSignals (phase 3) are the daemon's relevance score (0..1) and its
	// signals for an auto reply; nil when the reply was posted without them.
	Relevance        *float64          `json:"relevance,omitempty"`
	RelevanceSignals *RelevanceSignals `json:"relevance_signals,omitempty"`
	// Round 2 (migration 030): soft delete (a tombstone stays in the thread), author edits and
	// the duplicate-detection hash of the body.
	DeletedAt *time.Time `json:"deleted_at,omitempty"`
	EditedAt  *time.Time `json:"edited_at,omitempty"`
	EditCount int        `json:"edit_count,omitempty"`
	BodyHash  string     `json:"body_hash,omitempty"`
}

// IsDeleted reports whether the reply was soft deleted.
func (r *ForumReply) IsDeleted() bool {
	return r.DeletedAt != nil
}

// RelevanceSignals are the daemon's relevance inputs on an auto reply, each 0..1.
type RelevanceSignals struct {
	Library     float64 `json:"library"`
	History     float64 `json:"history"`
	Instruction float64 `json:"instruction"`
	Routed      float64 `json:"routed"`
}

// HasAsk reports whether the reply carries a bounty ask.
func (r *ForumReply) HasAsk() bool {
	return r.Ask > 0
}

// AutoReplyOutcome is one autopilot reply joined with what happened to the post it landed on:
// the post's category and current upvotes, whether the reply was accepted, whether the post's
// bounty went to the reply author, and whether the reply was hidden or reported.
// Replies have no upvotes of their own, so Upvotes is the post's count.
type AutoReplyOutcome struct {
	ReplyID  string
	PostID   string
	Category string
	PostedAt time.Time
	Upvotes  int
	// Accepted is true when the post's accepted_reply_id is this reply.
	Accepted bool
	// Awarded is true when the post's bounty was completed and awarded to the reply author.
	Awarded bool
	// BountyAmount is the post's bounty when it has one (nil otherwise).
	BountyAmount *int
	// Hidden is the reply's hidden flag (platform hide or auto hide after reports).
	Hidden bool
	// Reported is true when the reply has an open or upheld report (filled by the service).
	Reported bool
	// Relevance and RelevanceSignals are what the daemon sent with the reply (nil when none).
	Relevance        *float64
	RelevanceSignals *RelevanceSignals
}

// AwardedAmount is the bounty the reply author won through this reply (0 unless awarded).
func (o *AutoReplyOutcome) AwardedAmount() int {
	if o.Awarded && o.BountyAmount != nil {
		return *o.BountyAmount
	}
	return 0
}

// AutopilotCategoryOutcome is one by_category entry of the outcomes summary.
type AutopilotCategoryOutcome struct {
	Posted int `json:"posted"`
	// Won counts the replies that were accepted or awarded.
	Won int `json:"won"`
}

// AutopilotOutcomeSummary aggregates a peer's auto replies over the outcome window
// (GET /autopilot/outcomes/summary).
type AutopilotOutcomeSummary struct {
	Posted   int `json:"posted"`
	Upvoted  int `json:"upvoted"`
	Accepted int `json:"accepted"`
	Awarded  int `json:"awarded"`
	Hidden   int `json:"hidden"`
	Reported int `json:"reported"`
	// CreditsSpent is what the peer's autopilot drafts cost over the window (completions that
	// carried the autopilot marker).
	CreditsSpent int `json:"credits_spent"`
	// CreditsWon is the sum of the bounties awarded to the peer's auto replies.
	CreditsWon int                                  `json:"credits_won"`
	ByCategory map[string]*AutopilotCategoryOutcome `json:"by_category"`
}
