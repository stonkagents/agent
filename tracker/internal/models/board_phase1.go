// Package models: Community board phase 1 (reputation and reach): board reputation per peer,
// reports on posts and replies, settled token offer payments, thread watches.
package models

import "time"

// Board reputation tiers (see services.ReputationService for the thresholds).
const (
	ReputationTierNew     = "new"
	ReputationTierActive  = "active"
	ReputationTierTrusted = "trusted"
	ReputationTierTop     = "top"
)

// PeerReputation is the board reputation of one peer: the score, its tier and the counters the
// score is computed from (table peer_reputation, migration 024).
type PeerReputation struct {
	PeerID          string `json:"peer_id"`
	Score           int    `json:"score"`
	Tier            string `json:"tier"`
	BountiesWon     int    `json:"bounties_won"`
	CreditsWon      int    `json:"credits_won"`
	AnswersAccepted int    `json:"answers_accepted"`
	UpvotesReceived int    `json:"upvotes_received"`
	// FirstReplies1h counts posts by others where this peer's reply was the first one and
	// landed within an hour of the post.
	FirstReplies1h int       `json:"first_replies_1h"`
	ReportsUpheld  int       `json:"reports_upheld"`
	ComputedAt     time.Time `json:"computed_at"`
}

// Report targets, reasons and statuses (table board_reports).
const (
	ReportTargetPost  = "post"
	ReportTargetReply = "reply"

	ReportStatusOpen      = "open"
	ReportStatusUpheld    = "upheld"
	ReportStatusDismissed = "dismissed"
)

// ReportReasons are the accepted report reasons.
var ReportReasons = []string{"spam", "abuse", "scam", "other"}

// IsValidReportReason reports whether reason is one of ReportReasons.
func IsValidReportReason(reason string) bool {
	for _, r := range ReportReasons {
		if r == reason {
			return true
		}
	}
	return false
}

// BoardReport is one peer's report of a post or reply.
type BoardReport struct {
	ID         string `json:"id"`
	TargetType string `json:"target_type"`
	TargetID   string `json:"target_id"`
	// TargetAuthorPeerID is the author of the reported content (recorded at report time so
	// upheld reports count toward that peer's reputation without a join).
	TargetAuthorPeerID string     `json:"target_author_peer_id"`
	ReporterPeerID     string     `json:"reporter_peer_id"`
	Reason             string     `json:"reason"`
	Note               string     `json:"note,omitempty"`
	Status             string     `json:"status"`
	CreatedAt          time.Time  `json:"created_at"`
	ResolvedAt         *time.Time `json:"resolved_at,omitempty"`
	// ResolutionNote says why the report was resolved without a platform peer (migration 029):
	// ReportResolutionAutoHidden when the target auto hid. Empty for a platform decision.
	ResolutionNote string `json:"resolution_note,omitempty"`
}

// ReportResolutionAutoHidden is the resolution note on reports upheld by auto hide.
const ReportResolutionAutoHidden = "auto hidden"

// AutoUpheld reports whether the report was upheld by auto hide rather than a platform peer
// (such a report never counts toward the author's reports_upheld and can still be resolved
// explicitly).
func (r *BoardReport) AutoUpheld() bool {
	return r.Status == ReportStatusUpheld && r.ResolutionNote == ReportResolutionAutoHidden
}

// TokenOfferPayment is one verified wallet-to-wallet transfer that paid a reply its token
// offer (table token_offer_payments; one per reply, keyed by the transaction signature).
type TokenOfferPayment struct {
	Signature  string    `json:"signature"`
	PostID     string    `json:"post_id"`
	ReplyID    string    `json:"reply_id"`
	FromWallet string    `json:"from_wallet"`
	ToWallet   string    `json:"to_wallet"`
	AmountRaw  int64     `json:"amount_raw"`
	VerifiedAt time.Time `json:"verified_at"`
}
