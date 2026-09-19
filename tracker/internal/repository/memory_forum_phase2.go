// Package repository: In-memory ForumRepository, community board phase 2 methods (rooms,
// request routing, room digest).
package repository

import (
	"context"
	"sort"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
)

// RoomStats returns, per room mint, the visible post count since `since` and the newest post time.
func (r *MemoryForumRepository) RoomStats(ctx context.Context, mints []string, since time.Time) (map[string]*RoomPostStats, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	want := make(map[string]bool, len(mints))
	for _, m := range mints {
		want[m] = true
	}
	out := make(map[string]*RoomPostStats, len(mints))
	for _, p := range r.posts {
		if p.Hidden || p.IsDeleted() || !p.InRoom() || !want[*p.RoomMint] {
			continue
		}
		st := out[*p.RoomMint]
		if st == nil {
			st = &RoomPostStats{}
			out[*p.RoomMint] = st
		}
		if !p.CreatedAt.Before(since) {
			st.PostsSince++
		}
		if st.LastPostAt == nil || p.CreatedAt.After(*st.LastPostAt) {
			t := p.CreatedAt
			st.LastPostAt = &t
		}
	}
	return out, nil
}

// GetRoomAnnouncement returns the room's pinned announcement, or models.ErrNotFound.
func (r *MemoryForumRepository) GetRoomAnnouncement(ctx context.Context, roomMint string) (*models.ForumPost, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, p := range r.posts {
		if p.RoomPinned && p.InRoom() && *p.RoomMint == roomMint {
			cp := *p
			cp.ReplyCount = len(r.replies[p.ID])
			return &cp, nil
		}
	}
	return nil, models.ErrNotFound
}

// SetRoutedTo stores the peers a post was routed to.
func (r *MemoryForumRepository) SetRoutedTo(ctx context.Context, postID string, peerIDs []string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.posts[postID]
	if !ok {
		return models.ErrNotFound
	}
	p.RoutedTo = append([]string(nil), peerIDs...)
	return nil
}

// ListPostsByAuthor returns the author's posts newest first, at most limit.
func (r *MemoryForumRepository) ListPostsByAuthor(ctx context.Context, authorPeerID string, limit int) ([]*models.ForumPost, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []*models.ForumPost
	for _, p := range r.posts {
		if p.AuthorPeerID != authorPeerID {
			continue
		}
		cp := *p
		cp.ReplyCount = len(r.replies[p.ID])
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// PeersWithAcceptedReplySince returns the authors of accepted replies created at or after since.
func (r *MemoryForumRepository) PeersWithAcceptedReplySince(ctx context.Context, since time.Time) (map[string]bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := map[string]bool{}
	for _, p := range r.posts {
		if p.AcceptedReplyID == nil {
			continue
		}
		for _, rp := range r.replies[p.ID] {
			if rp.ID == *p.AcceptedReplyID && !rp.CreatedAt.Before(since) {
				out[rp.AuthorPeerID] = true
			}
		}
	}
	return out, nil
}

// RoomDigest aggregates the room's visible activity in [from, to]. Thread reply counts leave
// hidden replies out, like the period total does.
func (r *MemoryForumRepository) RoomDigest(ctx context.Context, roomMint string, from, to time.Time) (*RoomDigestStats, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	in := func(t time.Time) bool { return !t.Before(from) && !t.After(to) }
	st := &RoomDigestStats{TopThreads: []RoomDigestThread{}}
	var top []*models.ForumPost
	for _, p := range r.posts {
		if !p.InRoom() || *p.RoomMint != roomMint {
			continue
		}
		if p.HasBounty() && p.BountyStatus == "completed" && p.BountyCompletedAt != nil && in(*p.BountyCompletedAt) {
			st.BountiesAwarded++
			st.CreditsAwarded += *p.BountyAmount
		}
		if p.Hidden {
			continue
		}
		visible := 0
		for _, rp := range r.replies[p.ID] {
			if rp.Hidden {
				continue
			}
			visible++
			if in(rp.CreatedAt) {
				st.Replies++
			}
		}
		if in(p.CreatedAt) {
			st.Posts++
			cp := *p
			cp.ReplyCount = visible
			top = append(top, &cp)
		}
	}
	sort.Slice(top, func(i, j int) bool {
		if top[i].UpvoteCount != top[j].UpvoteCount {
			return top[i].UpvoteCount > top[j].UpvoteCount
		}
		if top[i].ReplyCount != top[j].ReplyCount {
			return top[i].ReplyCount > top[j].ReplyCount
		}
		return top[i].CreatedAt.After(top[j].CreatedAt)
	})
	for i, p := range top {
		if i >= 5 {
			break
		}
		st.TopThreads = append(st.TopThreads, RoomDigestThread{ID: p.ID, Title: p.Title, Upvotes: p.UpvoteCount, Replies: p.ReplyCount})
	}
	return st, nil
}

// CountRoutedPostsByAuthorSince counts the author's posts since `since` with a non-empty routed_to.
func (r *MemoryForumRepository) CountRoutedPostsByAuthorSince(ctx context.Context, authorPeerID string, since time.Time) (int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	n := 0
	for _, p := range r.posts {
		if p.AuthorPeerID == authorPeerID && !p.CreatedAt.Before(since) && len(p.RoutedTo) > 0 {
			n++
		}
	}
	return n, nil
}

// BoardActivity counts the peer's visible posts and replies (rooms included) and their newest time.
func (r *MemoryForumRepository) BoardActivity(ctx context.Context, peerID string) (*BoardActivity, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	st := &BoardActivity{}
	touch := func(at time.Time) {
		if st.LastActiveAt == nil || at.After(*st.LastActiveAt) {
			t := at
			st.LastActiveAt = &t
		}
	}
	for _, p := range r.posts {
		if p.AuthorPeerID == peerID && !p.Hidden {
			st.Posts++
			touch(p.CreatedAt)
		}
	}
	for _, repl := range r.replies {
		for _, rp := range repl {
			if rp.AuthorPeerID == peerID && !rp.Hidden {
				st.Replies++
				touch(rp.CreatedAt)
			}
		}
	}
	return st, nil
}
