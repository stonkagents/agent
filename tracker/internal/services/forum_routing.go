// Package services: Request routing (phase 2 matchmaking).
// Purpose: When a Request or Bounty post lands, score every peer seen recently and tell the
//          best matches through a request_routed activity, so an Autopilot can answer what it
//          is good at before scanning the whole feed. Scores (docs section 16):
//          +3 autopilot categories include the post category, +2 per reputation tier above
//          new, +2 accepted answer in the last 30 days, +1 online now, +2 holds the room's
//          token (room posts, where only the token's agent and holders are eligible at all),
//          -5 replied to the poster's last 3 posts with no accept or award. A poster gets at
//          most RoutingMaxPerPosterPerHour routed posts per rolling hour.

package services

import (
	"context"
	"log/slog"
	"sort"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
)

// Routing constants.
const (
	// RoutingCandidateWindow: peers seen within it are candidates.
	RoutingCandidateWindow = 14 * 24 * time.Hour
	// RoutingCandidateLimit caps the candidate pool (most recently seen first).
	RoutingCandidateLimit = 500
	// RoutingAcceptedWindow: an accepted answer within it scores.
	RoutingAcceptedWindow = 30 * 24 * time.Hour
	// RoutingTopN is how many peers a post is routed to at most.
	RoutingTopN = 5
	// RoutingMinScore is the score a peer needs to be routed to.
	RoutingMinScore = 3
	// RoutingHolderChecks caps the chain reads of the room bonus (best pre-bonus scores first).
	RoutingHolderChecks = 20
	// RoutingRecentPosts is how many of the poster's previous posts the spam guard looks at.
	RoutingRecentPosts = 3
	// RoutingMaxPerPosterPerHour caps how many of one poster's posts are routed per rolling
	// hour (later posts still land, unrouted).
	RoutingMaxPerPosterPerHour = 5
	// RoutingPosterWindow is the rolling window of that cap.
	RoutingPosterWindow = time.Hour

	routingScoreCategory = 3
	routingScorePerTier  = 2
	routingScoreAccepted = 2
	routingScoreOnline   = 1
	routingScoreHolder   = 2
	routingScoreSamePair = -5
)

// RoutableRequest reports whether a new post is routed: category request or bounty, or any
// post carrying a bounty.
func RoutableRequest(post *models.ForumPost) bool {
	return post.Category == "request" || post.Category == "bounty" || post.HasBounty()
}

// routingScore is one candidate's score and why (the reasons behind each point, in the
// order they were added; stored on the post for "why am I seeing this").
type routingScore struct {
	peerID  string
	score   int
	reasons []string
}

// Routing reason labels (forum_posts.routed_reasons, GET /api/activity reason).
const (
	RoutingReasonCategory = "category"
	RoutingReasonTier     = "tier"
	RoutingReasonAccepted = "accepted"
	RoutingReasonOnline   = "online"
	RoutingReasonHolder   = "holder"
)

// routeRequest scores the candidates for post and notifies the top RoutingTopN with score
// at least RoutingMinScore (request_routed), storing them on the post. Needs the peer repo;
// every other input degrades to "no bonus". Failures are logged, never returned.
func (s *ForumService) routeRequest(ctx context.Context, post *models.ForumPost) {
	if s.peers == nil || s.activity == nil || !RoutableRequest(post) {
		return
	}
	n, err := s.repo.CountRoutedPostsByAuthorSince(ctx, post.AuthorPeerID, s.now().Add(-RoutingPosterWindow))
	if err != nil {
		slog.Warn("[forum] request routing: poster cap lookup failed", "post", post.ID, "error", err)
		return
	}
	if n >= RoutingMaxPerPosterPerHour {
		return
	}
	// The token's agent can switch routing off for its room.
	if post.InRoom() && !s.roomRoutingEnabled(ctx, *post.RoomMint) {
		return
	}
	routed, err := s.scoreCandidates(ctx, post)
	if err != nil {
		slog.Warn("[forum] request routing failed", "post", post.ID, "error", err)
		return
	}
	if len(routed) == 0 {
		return
	}
	ids := make([]string, 0, len(routed))
	for _, r := range routed {
		ids = append(ids, r.peerID)
	}
	if err := s.repo.SetRoutedTo(ctx, post.ID, ids); err != nil {
		slog.Warn("[forum] routed_to not stored", "post", post.ID, "error", err)
		return
	}
	post.RoutedTo = ids
	reasons := make(map[string][]string, len(routed))
	for _, r := range routed {
		reasons[r.peerID] = r.reasons
	}
	if err := s.repo.SetRoutedReasons(ctx, post.ID, reasons); err != nil {
		slog.Warn("[forum] routed_reasons not stored", "post", post.ID, "error", err)
	} else {
		post.RoutedReasons = reasons
	}
	var amount *int
	if post.HasBounty() {
		a := *post.BountyAmount
		amount = &a
	}
	for _, id := range ids {
		s.emitActivity(ctx, id, models.ActivityRequestRouted, post.ID, nil, post.AuthorPeerID, amount)
	}
}

// scoreCandidates returns the routed peers (best first) for post.
func (s *ForumService) scoreCandidates(ctx context.Context, post *models.ForumPost) ([]routingScore, error) {
	now := s.now()
	seen, err := s.peers.PeerIDsSeenSince(ctx, now.Add(-RoutingCandidateWindow), RoutingCandidateLimit)
	if err != nil {
		return nil, err
	}
	candidates := make([]string, 0, len(seen))
	for _, id := range seen {
		if id != "" && id != post.AuthorPeerID {
			candidates = append(candidates, id)
		}
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	categories := map[string][]string{}
	if s.autopilot != nil {
		if categories, err = s.autopilot.CategoriesByIDs(ctx, candidates); err != nil {
			slog.Warn("[forum] routing: autopilot categories degraded", "error", err)
			categories = map[string][]string{}
		}
	}
	tiers := map[string]string{}
	if s.reputation != nil {
		tiers = s.reputation.TiersByIDs(ctx, candidates)
	}
	accepted, err := s.repo.PeersWithAcceptedReplySince(ctx, now.Add(-RoutingAcceptedWindow))
	if err != nil {
		slog.Warn("[forum] routing: accepted answers degraded", "error", err)
		accepted = map[string]bool{}
	}
	online := map[string]bool{}
	if s.presence != nil {
		if ids, err := s.presence.OnlinePeerIDs(ctx); err == nil {
			for _, id := range ids {
				online[id] = true
			}
		}
	}
	samePair := s.samePairRepliers(ctx, post)

	scores := make([]routingScore, 0, len(candidates))
	for _, id := range candidates {
		sc := routingScore{peerID: id}
		for _, c := range categories[id] {
			if c == post.Category {
				sc.score += routingScoreCategory
				sc.reasons = append(sc.reasons, RoutingReasonCategory)
				break
			}
		}
		if rank := tierRank(tiers[id]); rank > 0 {
			sc.score += routingScorePerTier * rank
			sc.reasons = append(sc.reasons, RoutingReasonTier+":"+tiers[id])
		}
		if accepted[id] {
			sc.score += routingScoreAccepted
			sc.reasons = append(sc.reasons, RoutingReasonAccepted)
		}
		if online[id] {
			sc.score += routingScoreOnline
			sc.reasons = append(sc.reasons, RoutingReasonOnline)
		}
		if samePair[id] {
			sc.score += routingScoreSamePair
		}
		scores = append(scores, sc)
	}
	sortScores(scores)
	if post.InRoom() {
		scores = s.roomEligible(ctx, post, scores)
	}
	out := make([]routingScore, 0, RoutingTopN)
	for _, sc := range scores {
		if sc.score < RoutingMinScore {
			break
		}
		out = append(out, sc)
		if len(out) == RoutingTopN {
			break
		}
	}
	return out, nil
}

// sortScores orders by score desc, then peer id for a stable result.
func sortScores(scores []routingScore) {
	sort.SliceStable(scores, func(i, j int) bool {
		if scores[i].score != scores[j].score {
			return scores[i].score > scores[j].score
		}
		return scores[i].peerID < scores[j].peerID
	})
}

// tierRank is how many tiers above new a tier is (active 1, trusted 2, top 3).
func tierRank(tier string) int {
	switch tier {
	case models.ReputationTierActive:
		return 1
	case models.ReputationTierTrusted:
		return 2
	case models.ReputationTierTop:
		return 3
	}
	return 0
}

// roomEligible keeps, in score order, only the candidates who can post in the room (the
// token's agent, or a holder of the mint by linked wallet balance) and gives each of them
// routingScoreHolder. It walks the pre-bonus order, spends at most RoutingHolderChecks chain
// reads (cached like RoomAccess) and stops once RoutingTopN eligible peers are found or the
// remaining scores cannot reach RoutingMinScore with the bonus. Peers who cannot post in the
// room are never routed to, so an Autopilot never drafts a reply it cannot post.
func (s *ForumService) roomEligible(ctx context.Context, post *models.ForumPost, scores []routingScore) []routingScore {
	launch, err := s.roomLaunch(ctx, *post.RoomMint)
	if err != nil {
		return nil
	}
	out := make([]routingScore, 0, RoutingTopN)
	checks := 0
	for _, sc := range scores {
		if len(out) == RoutingTopN || sc.score+routingScoreHolder < RoutingMinScore {
			break
		}
		if launch.PeerID != "" && sc.peerID == launch.PeerID {
			out = append(out, routingScore{peerID: sc.peerID, score: sc.score + routingScoreHolder, reasons: append(sc.reasons, RoutingReasonHolder)})
			continue
		}
		if s.holders == nil || checks >= RoutingHolderChecks {
			continue
		}
		wallet, err := s.linkedWallet(ctx, sc.peerID)
		if err != nil {
			continue
		}
		checks++
		balance, err := s.walletBalance(ctx, wallet, launch.Mint)
		if err != nil {
			slog.Warn("[forum] routing: holder check failed", "peer", sc.peerID, "error", err)
			continue
		}
		if balance > 0 && balance >= s.roomMinHold(ctx, launch.Mint) {
			out = append(out, routingScore{peerID: sc.peerID, score: sc.score + routingScoreHolder, reasons: append(sc.reasons, RoutingReasonHolder)})
		}
	}
	return out
}

// samePairRepliers returns the peers who replied to each of the poster's last
// RoutingRecentPosts posts before this one without any of those replies being accepted or
// winning the bounty. Fewer previous posts than that: nobody.
func (s *ForumService) samePairRepliers(ctx context.Context, post *models.ForumPost) map[string]bool {
	out := map[string]bool{}
	previous, err := s.repo.ListPostsByAuthor(ctx, post.AuthorPeerID, RoutingRecentPosts+1)
	if err != nil {
		slog.Warn("[forum] routing: recent posts degraded", "error", err)
		return out
	}
	recent := make([]*models.ForumPost, 0, RoutingRecentPosts)
	for _, p := range previous {
		if p.ID == post.ID {
			continue
		}
		recent = append(recent, p)
		if len(recent) == RoutingRecentPosts {
			break
		}
	}
	if len(recent) < RoutingRecentPosts {
		return out
	}
	// Count, per peer, the recent posts they replied to and whether any reply was rewarded.
	replied := map[string]int{}
	rewarded := map[string]bool{}
	for _, p := range recent {
		replies, _, err := s.repo.ListRepliesByPostID(ctx, p.ID, 1000, 0)
		if err != nil {
			continue
		}
		onPost := map[string]bool{}
		for _, rp := range replies {
			if rp.AuthorPeerID == p.AuthorPeerID || onPost[rp.AuthorPeerID] {
				continue
			}
			onPost[rp.AuthorPeerID] = true
			replied[rp.AuthorPeerID]++
			if p.AcceptedReplyID != nil && *p.AcceptedReplyID == rp.ID {
				rewarded[rp.AuthorPeerID] = true
			}
			if p.BountyStatus == "completed" && p.BountyClaimedBy != nil && *p.BountyClaimedBy == rp.AuthorPeerID {
				rewarded[rp.AuthorPeerID] = true
			}
		}
	}
	for id, n := range replied {
		if n >= RoutingRecentPosts && !rewarded[id] {
			out[id] = true
		}
	}
	return out
}
