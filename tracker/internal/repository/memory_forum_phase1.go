// Package repository: In-memory ForumRepository, community board phase 1 methods (accepted
// answer, hide and pin, token offer paid count, reputation counters).
package repository

import (
	"context"
	"sort"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
)

// GetReplyByID returns one reply, or models.ErrNotFound.
func (r *MemoryForumRepository) GetReplyByID(ctx context.Context, replyID string) (*models.ForumReply, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if rp := r.findReply(replyID); rp != nil {
		cp := *rp
		return &cp, nil
	}
	return nil, models.ErrNotFound
}

// findReply returns the stored reply pointer (caller holds the lock).
func (r *MemoryForumRepository) findReply(replyID string) *models.ForumReply {
	for _, repl := range r.replies {
		for _, rp := range repl {
			if rp.ID == replyID {
				return rp
			}
		}
	}
	return nil
}

// SetAcceptedReply records the accepted answer (nil clears it).
func (r *MemoryForumRepository) SetAcceptedReply(ctx context.Context, postID string, replyID *string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.posts[postID]
	if !ok {
		return models.ErrNotFound
	}
	if replyID == nil {
		p.AcceptedReplyID = nil
		return nil
	}
	id := *replyID
	p.AcceptedReplyID = &id
	return nil
}

// SetPostHidden flips a post's hidden flag.
func (r *MemoryForumRepository) SetPostHidden(ctx context.Context, postID string, hidden bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.posts[postID]
	if !ok {
		return models.ErrNotFound
	}
	p.Hidden = hidden
	return nil
}

// SetReplyHidden flips a reply's hidden flag.
func (r *MemoryForumRepository) SetReplyHidden(ctx context.Context, replyID string, hidden bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	rp := r.findReply(replyID)
	if rp == nil {
		return models.ErrNotFound
	}
	rp.Hidden = hidden
	return nil
}

// PinPost pins postID and unpins every other post.
func (r *MemoryForumRepository) PinPost(ctx context.Context, postID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.posts[postID]
	if !ok {
		return models.ErrNotFound
	}
	for _, other := range r.posts {
		other.Pinned = false
	}
	p.Pinned = true
	return nil
}

// UnpinPost clears the pinned flag.
func (r *MemoryForumRepository) UnpinPost(ctx context.Context, postID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.posts[postID]
	if !ok {
		return models.ErrNotFound
	}
	p.Pinned = false
	return nil
}

// IncrementTokenOfferPaid bumps token_offer_paid under the repository lock while it is below
// token_offer_max; models.ErrInvalidInput once the offer is exhausted.
func (r *MemoryForumRepository) IncrementTokenOfferPaid(ctx context.Context, postID string) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.posts[postID]
	if !ok {
		return 0, models.ErrNotFound
	}
	if p.TokenOfferExhausted() {
		return p.TokenOfferPaid, models.ErrInvalidInput
	}
	p.TokenOfferPaid++
	return p.TokenOfferPaid, nil
}

// DecrementTokenOfferPaid gives a reserved slot back (never below zero).
func (r *MemoryForumRepository) DecrementTokenOfferPaid(ctx context.Context, postID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.posts[postID]
	if !ok {
		return models.ErrNotFound
	}
	if p.TokenOfferPaid > 0 {
		p.TokenOfferPaid--
	}
	return nil
}

// BoardStats derives the reputation counters for peerID from the posts and replies.
func (r *MemoryForumRepository) BoardStats(ctx context.Context, peerID string, guard BoardStatsGuard) (*BoardStats, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	st := &BoardStats{}
	for _, p := range r.posts {
		if p.AuthorPeerID == peerID {
			for voter := range r.upvotes[p.ID] {
				if r.voterQualifies(voter, peerID, guard) {
					st.UpvotesReceived++
				}
			}
		}
		if p.BountyStatus == "completed" && p.BountyClaimedBy != nil && *p.BountyClaimedBy == peerID && p.BountyAmount != nil {
			st.BountiesWon++
			st.CreditsWon += *p.BountyAmount
		}
		repl := r.replies[p.ID]
		if p.AcceptedReplyID != nil && r.voterQualifies(p.AuthorPeerID, peerID, guard) {
			for _, rp := range repl {
				if rp.ID == *p.AcceptedReplyID && rp.AuthorPeerID == peerID {
					st.AnswersAccepted++
				}
			}
		}
		// First manual reply on someone else's post within the hour (autopilot replies never count).
		if p.AuthorPeerID != peerID {
			var first *models.ForumReply
			for _, rp := range repl {
				if !rp.Auto && (first == nil || rp.CreatedAt.Before(first.CreatedAt)) {
					first = rp
				}
			}
			if first != nil && first.AuthorPeerID == peerID && !first.CreatedAt.After(p.CreatedAt.Add(time.Hour)) {
				st.FirstReplies1h++
			}
		}
	}
	return st, nil
}

// voterQualifies applies the BoardStatsGuard to one voter or accepting author (see the
// interface): off when the guard is zero; else the peer must be older than VoterSince
// (PeerFirstSeen), have a post or a reply, and share no wallet with the beneficiary. Callers hold
// the read lock.
func (r *MemoryForumRepository) voterQualifies(voter, beneficiary string, guard BoardStatsGuard) bool {
	if guard.VoterSince.IsZero() {
		return true
	}
	first, ok := r.PeerFirstSeen[voter]
	if !ok || !first.Before(guard.VoterSince) {
		return false
	}
	active := false
	for _, p := range r.posts {
		if p.AuthorPeerID == voter {
			active = true
			break
		}
	}
	if !active {
		for _, repl := range r.replies {
			for _, rp := range repl {
				if rp.AuthorPeerID == voter {
					active = true
					break
				}
			}
			if active {
				break
			}
		}
	}
	if !active {
		return false
	}
	if w := r.PeerWallet[voter]; w != "" && w == r.PeerWallet[beneficiary] {
		return false
	}
	return true
}

// BoardPeerIDs returns every peer that authored a post or a reply, sorted.
func (r *MemoryForumRepository) BoardPeerIDs(ctx context.Context) ([]string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	seen := map[string]struct{}{}
	for _, p := range r.posts {
		seen[p.AuthorPeerID] = struct{}{}
	}
	for _, repl := range r.replies {
		for _, rp := range repl {
			seen[rp.AuthorPeerID] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for id := range seen {
		if id != "" {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out, nil
}
