// Package models: Community board activity feed (per-peer notifications).
// Purpose: One row per thing that happened to a peer on the board (a reply on their post, a
// bounty they won, an upvote, a bounty about to expire or refunded). Read by GET /api/activity.
package models

import "time"

// Board activity kinds. The actor is the peer whose action produced the row; system events
// (expiry job) carry an empty actor.
const (
	// ActivityReplyOnPost: someone replied to my post (reply_id set).
	ActivityReplyOnPost = "reply_on_post"
	// ActivityBountyAwarded: my reply won a bounty (reply_id and amount set, actor = post author).
	ActivityBountyAwarded = "bounty_awarded"
	// ActivityReplyUpvoted: my reply was upvoted. Reserved: replies carry no upvotes yet, so the
	// service never emits it.
	ActivityReplyUpvoted = "reply_upvoted"
	// ActivityPostUpvoted: my post was upvoted (once per actor and post).
	ActivityPostUpvoted = "post_upvoted"
	// ActivityBountyExpiring: my open bounty expires within 24 hours (emitted once per post).
	ActivityBountyExpiring = "bounty_expiring"
	// ActivityBountyExpiredRefunded: my bounty expired and the escrow came back (amount set).
	ActivityBountyExpiredRefunded = "bounty_expired_refunded"
	// ActivityReplyAccepted: my reply was accepted as the answer (reply_id set, actor = post author).
	ActivityReplyAccepted = "reply_accepted"
	// ActivityTokenOfferPaid: my reply was paid its token offer (reply_id, amount in raw units,
	// symbol set, actor = post author).
	ActivityTokenOfferPaid = "token_offer_paid"
	// ActivityReplyInWatched: a new reply landed on a post I watch (reply_id set, actor = replier).
	ActivityReplyInWatched = "reply_in_watched"
	// ActivityMentioned: a post or reply mentioned my display name (reply_id set for a reply).
	ActivityMentioned = "mentioned"
	// ActivityRequestRouted: a Request or Bounty post was routed to me (phase 2 matchmaking;
	// amount = bounty amount when the post has one, actor = poster).
	ActivityRequestRouted = "request_routed"
	// ActivityBountyAsk: a reply on my Request or Bounty post asks for credits (phase 3;
	// reply_id set, amount = the ask, actor = replier).
	ActivityBountyAsk = "bounty_ask"
	// ActivityBountyRaised: the author raised the bounty on a post where I asked for credits
	// (phase 3; amount = the new bounty, actor = post author).
	ActivityBountyRaised = "bounty_raised"
	// ActivityBountyDisputed: a replier disputed the award or expiry of my bounty (round 2;
	// amount = the bounty, actor = the disputing replier).
	ActivityBountyDisputed = "bounty_disputed"
	// ActivityBountyDisputeResolved: a platform peer resolved the dispute (to the author and the
	// disputer; actor = the platform peer).
	ActivityBountyDisputeResolved = "bounty_dispute_resolved"
)

// ActivityKinds lists every kind the tracker emits (GET /api/activity?kinds= validation).
var ActivityKinds = []string{
	ActivityReplyOnPost, ActivityBountyAwarded, ActivityReplyUpvoted, ActivityPostUpvoted,
	ActivityBountyExpiring, ActivityBountyExpiredRefunded, ActivityReplyAccepted, ActivityTokenOfferPaid,
	ActivityReplyInWatched, ActivityMentioned, ActivityRequestRouted, ActivityBountyAsk, ActivityBountyRaised,
	ActivityBountyDisputed, ActivityBountyDisputeResolved,
}

// IsValidActivityKind reports whether kind is one of ActivityKinds.
func IsValidActivityKind(kind string) bool {
	for _, k := range ActivityKinds {
		if k == kind {
			return true
		}
	}
	return false
}

// BoardActivity is one activity row for PeerID (the recipient).
type BoardActivity struct {
	ID          string  `json:"id"`
	PeerID      string  `json:"peer_id"`
	Kind        string  `json:"kind"`
	PostID      string  `json:"post_id"`
	ReplyID     *string `json:"reply_id,omitempty"`
	ActorPeerID string  `json:"actor_peer_id"`
	Amount      *int    `json:"amount,omitempty"`
	// Symbol is the token symbol for token_offer_paid rows (empty otherwise).
	Symbol    string     `json:"symbol,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	ReadAt    *time.Time `json:"read_at,omitempty"`
}
