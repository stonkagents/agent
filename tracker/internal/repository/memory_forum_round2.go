// Package repository: In-memory ForumRepository, community board round 2 methods (edits, soft
// deletes, duplicates, caps, routing reasons, disputes, per-scope counts) and the in-memory
// edit history, room, visit and notification preference repositories (tests and the memory
// backend).
package repository

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/stonkagents/agent/tracker/internal/models"
)

// UpdatePostBody replaces the title and body and stamps the edit.
func (r *MemoryForumRepository) UpdatePostBody(ctx context.Context, postID, title, body, bodyHash string, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.posts[postID]
	if !ok || p.IsDeleted() {
		return models.ErrNotFound
	}
	p.Title, p.Description, p.BodyHash = title, body, bodyHash
	t := at
	p.EditedAt = &t
	p.EditCount++
	p.UpdatedAt = at
	return nil
}

// UpdateReplyBody replaces a reply's body and stamps the edit.
func (r *MemoryForumRepository) UpdateReplyBody(ctx context.Context, replyID, body, bodyHash string, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	rp := r.findReply(replyID)
	if rp == nil || rp.IsDeleted() {
		return models.ErrNotFound
	}
	rp.Body, rp.BodyHash = body, bodyHash
	t := at
	rp.EditedAt = &t
	rp.EditCount++
	return nil
}

// SoftDeletePost sets deleted_at once.
func (r *MemoryForumRepository) SoftDeletePost(ctx context.Context, postID string, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.posts[postID]
	if !ok {
		return models.ErrNotFound
	}
	if p.IsDeleted() {
		return models.ErrInvalidInput
	}
	t := at
	p.DeletedAt = &t
	return nil
}

// SoftDeleteReply sets deleted_at once on a reply.
func (r *MemoryForumRepository) SoftDeleteReply(ctx context.Context, replyID string, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	rp := r.findReply(replyID)
	if rp == nil {
		return models.ErrNotFound
	}
	if rp.IsDeleted() {
		return models.ErrInvalidInput
	}
	t := at
	rp.DeletedAt = &t
	return nil
}

// FindRecentPostByBodyHash returns the author's newest live post with that hash since the time.
func (r *MemoryForumRepository) FindRecentPostByBodyHash(ctx context.Context, authorPeerID, bodyHash string, since time.Time) (string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if bodyHash == "" {
		return "", models.ErrNotFound
	}
	var best *models.ForumPost
	for _, p := range r.posts {
		if p.AuthorPeerID == authorPeerID && p.BodyHash == bodyHash && !p.CreatedAt.Before(since) && !p.IsDeleted() {
			if best == nil || p.CreatedAt.After(best.CreatedAt) {
				best = p
			}
		}
	}
	if best == nil {
		return "", models.ErrNotFound
	}
	return best.ID, nil
}

// FindRecentReplyByBodyHash returns the author's newest live reply on the post with that hash.
func (r *MemoryForumRepository) FindRecentReplyByBodyHash(ctx context.Context, postID, authorPeerID, bodyHash string, since time.Time) (string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if bodyHash == "" {
		return "", models.ErrNotFound
	}
	var best *models.ForumReply
	for _, rp := range r.replies[postID] {
		if rp.AuthorPeerID == authorPeerID && rp.BodyHash == bodyHash && !rp.CreatedAt.Before(since) && !rp.IsDeleted() {
			if best == nil || rp.CreatedAt.After(best.CreatedAt) {
				best = rp
			}
		}
	}
	if best == nil {
		return "", models.ErrNotFound
	}
	return best.ID, nil
}

// CountUpvotesByPeerSince counts the upvotes the peer gave at or after since.
func (r *MemoryForumRepository) CountUpvotesByPeerSince(ctx context.Context, peerID string, since time.Time) (int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	n := 0
	for _, byPeer := range r.upvoteAt {
		if at, ok := byPeer[peerID]; ok && !at.Before(since) {
			n++
		}
	}
	return n, nil
}

// CountAutoRepliesByPeerInRoomSince counts the peer's auto replies on the room's posts since the time.
func (r *MemoryForumRepository) CountAutoRepliesByPeerInRoomSince(ctx context.Context, peerID, roomMint string, since time.Time) (int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	n := 0
	for postID, repl := range r.replies {
		p := r.posts[postID]
		if p == nil || !p.InRoom() || *p.RoomMint != roomMint {
			continue
		}
		for _, rp := range repl {
			if rp.AuthorPeerID == peerID && rp.Auto && !rp.CreatedAt.Before(since) {
				n++
			}
		}
	}
	return n, nil
}

// SetRoutedReasons stores why each routed peer was picked.
func (r *MemoryForumRepository) SetRoutedReasons(ctx context.Context, postID string, reasons map[string][]string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.posts[postID]
	if !ok {
		return models.ErrNotFound
	}
	cp := make(map[string][]string, len(reasons))
	for k, v := range reasons {
		cp[k] = append([]string(nil), v...)
	}
	p.RoutedReasons = cp
	return nil
}

// OpenBountyDispute records a dispute on a completed or expired bounty with no open dispute.
func (r *MemoryForumRepository) OpenBountyDispute(ctx context.Context, postID, byPeerID, note string, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.posts[postID]
	if !ok {
		return models.ErrNotFound
	}
	if !p.HasBounty() || (p.BountyStatus != "completed" && p.BountyStatus != "expired") || p.HasOpenDispute() {
		return models.ErrInvalidInput
	}
	t := at
	p.BountyDisputeStatus, p.BountyDisputeBy, p.BountyDisputeNote, p.BountyDisputedAt, p.BountyDisputeResolvedAt = models.BountyDisputeOpen, byPeerID, note, &t, nil
	return nil
}

// ResolveBountyDispute closes the open dispute.
func (r *MemoryForumRepository) ResolveBountyDispute(ctx context.Context, postID, status string, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.posts[postID]
	if !ok {
		return models.ErrNotFound
	}
	if !p.HasOpenDispute() {
		return models.ErrInvalidInput
	}
	t := at
	p.BountyDisputeStatus, p.BountyDisputeResolvedAt = status, &t
	return nil
}

// ListDisputedBounties returns the posts with an open dispute, oldest first.
func (r *MemoryForumRepository) ListDisputedBounties(ctx context.Context, limit int) ([]*models.ForumPost, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []*models.ForumPost
	for _, p := range r.posts {
		if p.HasOpenDispute() {
			cp := *p
			cp.ReplyCount = len(r.replies[p.ID])
			out = append(out, &cp)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].BountyDisputedAt.Before(*out[j].BountyDisputedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// CountPostsSince counts the visible posts of the scope created after since.
func (r *MemoryForumRepository) CountPostsSince(ctx context.Context, scope string, since time.Time) (int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	n := 0
	for _, p := range r.posts {
		if p.Hidden || p.IsDeleted() || !p.CreatedAt.After(since) {
			continue
		}
		if scope == "" {
			if !p.InRoom() {
				n++
			}
		} else if p.InRoom() && *p.RoomMint == scope {
			n++
		}
	}
	return n, nil
}

// --- Edit history ---

// MemoryBoardEditHistoryRepository implements BoardEditHistoryRepository in memory.
type MemoryBoardEditHistoryRepository struct {
	mu   sync.RWMutex
	rows []*models.BoardEdit
}

// NewMemoryBoardEditHistoryRepository creates an empty repository.
func NewMemoryBoardEditHistoryRepository() *MemoryBoardEditHistoryRepository {
	return &MemoryBoardEditHistoryRepository{}
}

// Add inserts one row.
func (r *MemoryBoardEditHistoryRepository) Add(ctx context.Context, e *models.BoardEdit) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e.ID == "" {
		e.ID = uuid.New().String()
	}
	cp := *e
	r.rows = append(r.rows, &cp)
	return nil
}

// List returns the target's edits newest first.
func (r *MemoryBoardEditHistoryRepository) List(ctx context.Context, targetType, targetID string, limit int) ([]*models.BoardEdit, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := []*models.BoardEdit{}
	for _, e := range r.rows {
		if e.TargetType == targetType && e.TargetID == targetID {
			cp := *e
			out = append(out, &cp)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].EditedAt.After(out[j].EditedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// --- Room controls ---

// MemoryBoardRoomRepository implements BoardRoomRepository in memory.
type MemoryBoardRoomRepository struct {
	mu       sync.RWMutex
	settings map[string]*models.RoomSettings
	mutes    map[string]map[string]*models.RoomMute // mint -> peer -> mute
}

// NewMemoryBoardRoomRepository creates an empty repository.
func NewMemoryBoardRoomRepository() *MemoryBoardRoomRepository {
	return &MemoryBoardRoomRepository{settings: map[string]*models.RoomSettings{}, mutes: map[string]map[string]*models.RoomMute{}}
}

// GetSettings returns the room's settings, the defaults without a row.
func (r *MemoryBoardRoomRepository) GetSettings(ctx context.Context, mint string) (*models.RoomSettings, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if s, ok := r.settings[mint]; ok {
		cp := *s
		return &cp, nil
	}
	return models.DefaultRoomSettings(mint), nil
}

// SetSettings upserts the room's settings.
func (r *MemoryBoardRoomRepository) SetSettings(ctx context.Context, s *models.RoomSettings) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *s
	r.settings[s.Mint] = &cp
	return nil
}

// SettingsByMints returns the stored rows among mints.
func (r *MemoryBoardRoomRepository) SettingsByMints(ctx context.Context, mints []string) (map[string]*models.RoomSettings, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := map[string]*models.RoomSettings{}
	for _, m := range mints {
		if s, ok := r.settings[m]; ok {
			cp := *s
			out[m] = &cp
		}
	}
	return out, nil
}

// Mute upserts a mute.
func (r *MemoryBoardRoomRepository) Mute(ctx context.Context, m *models.RoomMute) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.mutes[m.Mint] == nil {
		r.mutes[m.Mint] = map[string]*models.RoomMute{}
	}
	cp := *m
	r.mutes[m.Mint][m.PeerID] = &cp
	return nil
}

// Unmute removes a mute.
func (r *MemoryBoardRoomRepository) Unmute(ctx context.Context, mint, peerID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.mutes[mint][peerID]; !ok {
		return models.ErrNotFound
	}
	delete(r.mutes[mint], peerID)
	return nil
}

// IsMuted reports whether the peer is muted in the room.
func (r *MemoryBoardRoomRepository) IsMuted(ctx context.Context, mint, peerID string) (bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.mutes[mint][peerID]
	return ok, nil
}

// ListMutes returns the room's mutes newest first.
func (r *MemoryBoardRoomRepository) ListMutes(ctx context.Context, mint string) ([]*models.RoomMute, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := []*models.RoomMute{}
	for _, m := range r.mutes[mint] {
		cp := *m
		out = append(out, &cp)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

// --- Visits ---

// MemoryBoardVisitRepository implements BoardVisitRepository in memory.
type MemoryBoardVisitRepository struct {
	mu     sync.Mutex
	visits map[string]time.Time // peer + "\x00" + scope
}

// NewMemoryBoardVisitRepository creates an empty repository.
func NewMemoryBoardVisitRepository() *MemoryBoardVisitRepository {
	return &MemoryBoardVisitRepository{visits: map[string]time.Time{}}
}

// Visit records the visit and returns the previous one.
func (r *MemoryBoardVisitRepository) Visit(ctx context.Context, peerID, scope string, at time.Time) (*time.Time, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := peerID + "\x00" + scope
	var prev *time.Time
	if t, ok := r.visits[key]; ok {
		p := t
		prev = &p
		if at.Before(t) {
			return prev, nil
		}
	}
	r.visits[key] = at
	return prev, nil
}

// LastVisits returns scope -> last visit for the visited scopes.
func (r *MemoryBoardVisitRepository) LastVisits(ctx context.Context, peerID string, scopes []string) (map[string]time.Time, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[string]time.Time{}
	for _, s := range scopes {
		if t, ok := r.visits[peerID+"\x00"+s]; ok {
			out[s] = t
		}
	}
	return out, nil
}

// --- Notification preferences ---

// MemoryBoardNotificationPrefRepository implements BoardNotificationPrefRepository in memory.
type MemoryBoardNotificationPrefRepository struct {
	mu    sync.RWMutex
	prefs map[string]*models.NotificationPrefs
}

// NewMemoryBoardNotificationPrefRepository creates an empty repository.
func NewMemoryBoardNotificationPrefRepository() *MemoryBoardNotificationPrefRepository {
	return &MemoryBoardNotificationPrefRepository{prefs: map[string]*models.NotificationPrefs{}}
}

// Get returns the peer's preferences (nothing muted without a row).
func (r *MemoryBoardNotificationPrefRepository) Get(ctx context.Context, peerID string) (*models.NotificationPrefs, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if p, ok := r.prefs[peerID]; ok {
		cp := *p
		cp.MutedKinds = append([]string{}, p.MutedKinds...)
		return &cp, nil
	}
	return &models.NotificationPrefs{PeerID: peerID, MutedKinds: []string{}}, nil
}

// Set upserts the peer's preferences.
func (r *MemoryBoardNotificationPrefRepository) Set(ctx context.Context, p *models.NotificationPrefs) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *p
	cp.MutedKinds = append([]string{}, p.MutedKinds...)
	r.prefs[p.PeerID] = &cp
	return nil
}
