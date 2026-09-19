// Package services: Community board round 2 on the ForumService.
// Purpose: What a user feels and what an abuser hits: author edits within a window (history
//          kept) and soft deletes (tombstones keep the thread readable), duplicate post and
//          reply detection, link rules, a per-peer daily upvote cap, bounty disputes, a
//          reviewer queue, room controls for the token's agent (routing switch, minimum
//          holding, mutes), per-room autopilot caps with a global circuit breaker, feed visits
//          (unread per room, "new since your last visit") and notification preferences.

package services

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/repository"
)

// Round 2 errors.
var (
	// ErrDeleted: the post or reply was soft deleted (410 on the routes).
	ErrDeleted = errors.New("content was deleted")
	// ErrNotAuthor: only the author edits or deletes their content (platform peers may delete).
	ErrNotAuthor = errors.New("only the author can do this")
	// ErrEditWindowClosed: the edit window after creation has passed.
	ErrEditWindowClosed = errors.New("edit window closed")
	// ErrDuplicatePost: the same body was posted by the same peer within DuplicateWindow.
	ErrDuplicatePost = errors.New("duplicate post")
	// ErrDuplicateReply: the same body was posted by the same peer on the same post within DuplicateWindow.
	ErrDuplicateReply = errors.New("duplicate reply")
	// ErrUpvoteLimit: the peer gave MaxUpvotesPerPeerPerDay upvotes today.
	ErrUpvoteLimit = errors.New("daily upvote limit reached")
	// ErrBountyOpenOnDelete: a post with an open bounty cannot be deleted (award it or let it expire).
	ErrBountyOpenOnDelete = errors.New("a post with an open bounty cannot be deleted")
	// ErrBountyNotDisputable: the bounty is not completed or expired, or is already disputed.
	ErrBountyNotDisputable = errors.New("bounty cannot be disputed")
	// ErrDisputeNotReplier: only a replier on the post (not its author) can dispute the bounty.
	ErrDisputeNotReplier = errors.New("only a replier on the post can dispute its bounty")
	// ErrDisputeWindowClosed: the dispute window after the award or expiry has passed.
	ErrDisputeWindowClosed = errors.New("dispute window closed")
	// ErrNoOpenDispute: resolve needs an open dispute.
	ErrNoOpenDispute = errors.New("no open dispute")
	// ErrRoomMuted: the token's agent muted the peer in that room.
	ErrRoomMuted = errors.New("muted in this room")
	// ErrAutopilotPaused: the owner flipped the global autopilot circuit breaker (AUTOPILOT_PAUSED).
	ErrAutopilotPaused = errors.New("autopilot writes are paused on this tracker")
	// ErrBountyBelowMin: the bounty is under the minimum.
	ErrBountyBelowMin = errors.New("bounty is below the minimum")
)

// Round 2 defaults (each has an env override read in cmd/tracker).
const (
	// DefaultEditWindow is how long after creation an author may edit a post or reply.
	DefaultEditWindow = 30 * time.Minute
	// DuplicateWindow is how far back the same body by the same peer counts as a duplicate.
	DuplicateWindow = 24 * time.Hour
	// DefaultMaxUpvotesPerPeerPerDay caps the upvotes one peer gives per UTC day.
	DefaultMaxUpvotesPerPeerPerDay = 50
	// DefaultAutopilotMaxRepliesPerRoomPerDay caps one peer's auto replies in one room per UTC day.
	DefaultAutopilotMaxRepliesPerRoomPerDay = 5
	// DefaultMinBountyAmount is the smallest bounty (credits); 1 keeps every existing client working.
	DefaultMinBountyAmount = 1
	// DefaultReputationMinVoterAge is how old a peer must be for its upvotes and accepts to count.
	DefaultReputationMinVoterAge = 3 * 24 * time.Hour
	// BountyDisputeWindow is how long after an award or expiry a replier may dispute it.
	BountyDisputeWindow = 7 * 24 * time.Hour
	// UnreadNeverVisitedWindow is how far back posts count as unread in a scope never visited.
	UnreadNeverVisitedWindow = 7 * 24 * time.Hour
	// MaxEditHistory caps the history rows returned per target.
	MaxEditHistory = 50
	// ModerationQueueLimit caps the open reports the reviewer queue folds.
	ModerationQueueLimit = 500
)

// Round2Settings are the tunables of this round (zero values take the defaults).
type Round2Settings struct {
	EditWindow                 time.Duration
	MaxUpvotesPerPeerPerDay    int
	AutopilotMaxRepliesPerRoom int
	AutopilotPaused            bool
	MinBountyAmount            int
	MaxBountyAmountOverride    int
	// DuplicateWindow is how far back the same body by the same peer counts as a duplicate
	// (DuplicateWindow when zero). Duplicate detection is only on once SetRound2 ran.
	DuplicateWindow time.Duration
}

// Round2Deps are the round 2 repositories; every field is optional (nil disables the feature).
type Round2Deps struct {
	Edits  repository.BoardEditHistoryRepository
	Rooms  repository.BoardRoomRepository
	Visits repository.BoardVisitRepository
	Prefs  repository.BoardNotificationPrefRepository
}

// SetRound2 wires the round 2 repositories and settings.
func (s *ForumService) SetRound2(d Round2Deps, cfg Round2Settings) {
	s.edits, s.rooms, s.visits, s.prefs = d.Edits, d.Rooms, d.Visits, d.Prefs
	s.editWindow = cfg.EditWindow
	if s.editWindow <= 0 {
		s.editWindow = DefaultEditWindow
	}
	s.maxUpvotesPerDay = cfg.MaxUpvotesPerPeerPerDay
	if s.maxUpvotesPerDay == 0 {
		s.maxUpvotesPerDay = DefaultMaxUpvotesPerPeerPerDay
	}
	s.autopilotMaxPerRoom = cfg.AutopilotMaxRepliesPerRoom
	if s.autopilotMaxPerRoom == 0 {
		s.autopilotMaxPerRoom = DefaultAutopilotMaxRepliesPerRoomPerDay
	}
	s.autopilotPaused = cfg.AutopilotPaused
	s.minBounty = cfg.MinBountyAmount
	if s.minBounty <= 0 {
		s.minBounty = DefaultMinBountyAmount
	}
	s.maxBounty = cfg.MaxBountyAmountOverride
	if s.maxBounty <= 0 {
		s.maxBounty = MaxBountyAmount
	}
	s.dupWindow = cfg.DuplicateWindow
	if s.dupWindow <= 0 {
		s.dupWindow = DuplicateWindow
	}
}

// round2Defaults fills the settings a service built without SetRound2 uses (tests).
func (s *ForumService) round2Defaults() {
	if s.editWindow <= 0 {
		s.editWindow = DefaultEditWindow
	}
	if s.maxUpvotesPerDay == 0 {
		s.maxUpvotesPerDay = DefaultMaxUpvotesPerPeerPerDay
	}
	if s.autopilotMaxPerRoom == 0 {
		s.autopilotMaxPerRoom = DefaultAutopilotMaxRepliesPerRoomPerDay
	}
	if s.minBounty <= 0 {
		s.minBounty = DefaultMinBountyAmount
	}
	if s.maxBounty <= 0 {
		s.maxBounty = MaxBountyAmount
	}
}

// AutopilotPaused reports the global circuit breaker.
func (s *ForumService) AutopilotPaused() bool { return s.autopilotPaused }

// SetAutopilotPaused flips the global circuit breaker (tests; production reads AUTOPILOT_PAUSED).
func (s *ForumService) SetAutopilotPaused(paused bool) { s.autopilotPaused = paused }

// BountyLimits returns the bounty amount bounds in credits.
func (s *ForumService) BountyLimits() (min, max int) {
	s.round2Defaults()
	return s.minBounty, s.maxBounty
}

// utcDayStart is the start of the UTC day of t.
func utcDayStart(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
}

// checkBodyLinks applies the link rules (count and URL schemes) to a sanitised body.
func checkBodyLinks(body string) error {
	if err := models.CheckBoardLinks(body); err != nil {
		var linkErr *models.BoardLinkError
		if errors.As(err, &linkErr) {
			return &BoardTextError{Field: "links", Limit: models.MaxBoardLinks, Unit: "entries", Message: linkErr.Error()}
		}
		return err
	}
	return nil
}

// --- Edits and deletes ---

// EditPost lets the author rewrite the title and body of their post within the edit window.
// The previous text goes to the history; mentions are not re-resolved (no second round of
// notifications). Deleted posts refuse (ErrDeleted).
func (s *ForumService) EditPost(ctx context.Context, peerID, postID, title, body string) (*models.ForumPost, error) {
	s.round2Defaults()
	post, err := s.repo.GetPostByID(ctx, postID)
	if err != nil {
		return nil, err
	}
	if post.IsDeleted() {
		return nil, ErrDeleted
	}
	if post.AuthorPeerID != peerID {
		return nil, ErrNotAuthor
	}
	now := s.now()
	if now.Sub(post.CreatedAt) > s.editWindow {
		return nil, ErrEditWindowClosed
	}
	newTitle := models.SanitizeBoardLine(title, models.MaxBoardTitleRunes)
	if newTitle == "" {
		newTitle = post.Title
	}
	newBody, err := boardText("body", body, models.MaxBoardBodyRunes, true)
	if err != nil {
		return nil, err
	}
	if err := checkBodyLinks(newBody); err != nil {
		return nil, err
	}
	if newTitle == post.Title && newBody == post.Description {
		return post, nil
	}
	if err := s.repo.UpdatePostBody(ctx, postID, newTitle, newBody, models.BoardBodyHash(newBody), now); err != nil {
		return nil, err
	}
	s.recordEdit(ctx, models.ReportTargetPost, postID, peerID, post.Title, post.Description, now)
	return s.repo.GetPostByID(ctx, postID)
}

// EditReply lets the author rewrite their reply within the edit window (history kept).
func (s *ForumService) EditReply(ctx context.Context, peerID, replyID, body string) (*models.ForumReply, error) {
	s.round2Defaults()
	reply, err := s.repo.GetReplyByID(ctx, replyID)
	if err != nil {
		return nil, err
	}
	if reply.IsDeleted() {
		return nil, ErrDeleted
	}
	if reply.AuthorPeerID != peerID {
		return nil, ErrNotAuthor
	}
	now := s.now()
	if now.Sub(reply.CreatedAt) > s.editWindow {
		return nil, ErrEditWindowClosed
	}
	newBody, err := boardText("body", body, models.MaxBoardBodyRunes, true)
	if err != nil {
		return nil, err
	}
	if err := checkBodyLinks(newBody); err != nil {
		return nil, err
	}
	if newBody == reply.Body {
		return reply, nil
	}
	if err := s.repo.UpdateReplyBody(ctx, replyID, newBody, models.BoardBodyHash(newBody), now); err != nil {
		return nil, err
	}
	s.recordEdit(ctx, models.ReportTargetReply, replyID, peerID, "", reply.Body, now)
	return s.repo.GetReplyByID(ctx, replyID)
}

// recordEdit keeps the previous text (failures are logged: the edit itself already landed).
func (s *ForumService) recordEdit(ctx context.Context, targetType, targetID, editor, prevTitle, prevBody string, at time.Time) {
	if s.edits == nil {
		return
	}
	if err := s.edits.Add(ctx, &models.BoardEdit{TargetType: targetType, TargetID: targetID, EditorPeerID: editor, PreviousTitle: prevTitle, PreviousBody: prevBody, EditedAt: at}); err != nil {
		slog.Warn("[forum] edit history not recorded", "target", targetType, "id", targetID, "error", err)
	}
}

// EditHistory returns the previous versions of a post or reply, newest first: the author and
// platform peers only (ErrNotAuthor otherwise).
func (s *ForumService) EditHistory(ctx context.Context, viewerPeerID, targetType, targetID string) ([]*models.BoardEdit, error) {
	author, _, err := s.targetAuthor(ctx, targetType, targetID)
	if err != nil {
		return nil, err
	}
	if viewerPeerID == "" || (author != viewerPeerID && !s.IsPlatformPeer(viewerPeerID)) {
		return nil, ErrNotAuthor
	}
	if s.edits == nil {
		return []*models.BoardEdit{}, nil
	}
	out, err := s.edits.List(ctx, targetType, targetID, MaxEditHistory)
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []*models.BoardEdit{}
	}
	return out, nil
}

// DeletePost soft deletes a post: its author or a platform peer. A post with an open bounty
// stays until the bounty is awarded or expires (the escrow must settle first).
func (s *ForumService) DeletePost(ctx context.Context, peerID, postID string) error {
	post, err := s.repo.GetPostByID(ctx, postID)
	if err != nil {
		return err
	}
	if post.IsDeleted() {
		return ErrDeleted
	}
	if post.AuthorPeerID != peerID && !s.IsPlatformPeer(peerID) {
		return ErrNotAuthor
	}
	if post.HasBounty() && post.BountyStatus == "open" {
		return ErrBountyOpenOnDelete
	}
	if err := s.repo.SoftDeletePost(ctx, postID, s.now()); err != nil {
		if errors.Is(err, models.ErrInvalidInput) {
			return ErrDeleted
		}
		return err
	}
	s.invalidateCounts()
	return nil
}

// DeleteReply soft deletes a reply: its author or a platform peer. The tombstone stays in the
// thread (and keeps the post's reply count).
func (s *ForumService) DeleteReply(ctx context.Context, peerID, replyID string) error {
	reply, err := s.repo.GetReplyByID(ctx, replyID)
	if err != nil {
		return err
	}
	if reply.IsDeleted() {
		return ErrDeleted
	}
	if reply.AuthorPeerID != peerID && !s.IsPlatformPeer(peerID) {
		return ErrNotAuthor
	}
	if err := s.repo.SoftDeleteReply(ctx, replyID, s.now()); err != nil {
		if errors.Is(err, models.ErrInvalidInput) {
			return ErrDeleted
		}
		return err
	}
	return nil
}

// invalidateCounts drops the cached board counts so a delete shows at once.
func (s *ForumService) invalidateCounts() {
	s.countsMu.Lock()
	s.countsCached, s.countsAt = nil, nil
	s.countsMu.Unlock()
}

// --- Duplicates ---

// duplicatePost reports the id of the author's live post with the same body within the
// duplicate window ("" when none, or while detection is off).
func (s *ForumService) duplicatePost(ctx context.Context, authorPeerID, bodyHash string) string {
	if s.dupWindow <= 0 || bodyHash == "" {
		return ""
	}
	id, err := s.repo.FindRecentPostByBodyHash(ctx, authorPeerID, bodyHash, s.now().Add(-s.dupWindow))
	if err != nil {
		return ""
	}
	return id
}

// duplicateReply reports the id of the author's live reply on the post with the same body
// within DuplicateWindow ("" when none).
func (s *ForumService) duplicateReply(ctx context.Context, postID, authorPeerID, bodyHash string) string {
	if s.dupWindow <= 0 || bodyHash == "" {
		return ""
	}
	id, err := s.repo.FindRecentReplyByBodyHash(ctx, postID, authorPeerID, bodyHash, s.now().Add(-s.dupWindow))
	if err != nil {
		return ""
	}
	return id
}

// --- Upvote cap ---

// checkUpvoteCap refuses the peer's next upvote once MaxUpvotesPerPeerPerDay landed this UTC day.
func (s *ForumService) checkUpvoteCap(ctx context.Context, peerID string) error {
	s.round2Defaults()
	if s.maxUpvotesPerDay < 0 {
		return nil
	}
	n, err := s.repo.CountUpvotesByPeerSince(ctx, peerID, utcDayStart(s.now()))
	if err != nil {
		return fmt.Errorf("upvote cap: %w", err)
	}
	if n >= s.maxUpvotesPerDay {
		return ErrUpvoteLimit
	}
	return nil
}

// --- Bounty disputes ---

// DisputeBounty lets a replier (never the author) dispute an awarded or expired bounty within
// BountyDisputeWindow of the award or expiry. One open dispute per post; a platform peer
// resolves it. No credits move: the dispute is a flag for the reviewer queue.
func (s *ForumService) DisputeBounty(ctx context.Context, peerID, postID, note string) (*models.ForumPost, error) {
	post, err := s.repo.GetPostByID(ctx, postID)
	if err != nil {
		return nil, err
	}
	if post.IsDeleted() {
		return nil, ErrDeleted
	}
	if !post.HasBounty() || (post.BountyStatus != "completed" && post.BountyStatus != "expired") {
		return nil, ErrBountyNotDisputable
	}
	if post.HasOpenDispute() {
		return nil, ErrBountyNotDisputable
	}
	if post.AuthorPeerID == peerID {
		return nil, ErrDisputeNotReplier
	}
	replies, _, err := s.repo.ListRepliesByPostID(ctx, postID, 1000, 0)
	if err != nil {
		return nil, fmt.Errorf("dispute: list replies: %w", err)
	}
	replied := false
	for _, r := range replies {
		if r.AuthorPeerID == peerID && !r.IsDeleted() {
			replied = true
			break
		}
	}
	if !replied {
		return nil, ErrDisputeNotReplier
	}
	now := s.now()
	closedAt := post.BountyCompletedAt
	if post.BountyStatus == "expired" {
		closedAt = post.BountyExpiresAt
	}
	if closedAt != nil && now.Sub(*closedAt) > BountyDisputeWindow {
		return nil, ErrDisputeWindowClosed
	}
	note = models.SanitizeBoardText(note, models.MaxBountyDisputeNoteRunes)
	if err := s.repo.OpenBountyDispute(ctx, postID, peerID, note, now); err != nil {
		if errors.Is(err, models.ErrInvalidInput) {
			return nil, ErrBountyNotDisputable
		}
		return nil, err
	}
	amount := *post.BountyAmount
	s.emitActivity(ctx, post.AuthorPeerID, models.ActivityBountyDisputed, postID, nil, peerID, &amount)
	return s.repo.GetPostByID(ctx, postID)
}

// ResolveBountyDispute closes the open dispute (platform peers): upheld or dismissed. An
// upheld dispute counts like an upheld report against the post author's reputation is a
// product question; here it only records the outcome and tells both sides.
func (s *ForumService) ResolveBountyDispute(ctx context.Context, platformPeerID, postID string, uphold bool) (*models.ForumPost, error) {
	post, err := s.repo.GetPostByID(ctx, postID)
	if err != nil {
		return nil, err
	}
	if !post.HasOpenDispute() {
		return nil, ErrNoOpenDispute
	}
	status := models.BountyDisputeDismissed
	if uphold {
		status = models.BountyDisputeUpheld
	}
	if err := s.repo.ResolveBountyDispute(ctx, postID, status, s.now()); err != nil {
		if errors.Is(err, models.ErrInvalidInput) {
			return nil, ErrNoOpenDispute
		}
		return nil, err
	}
	s.emitActivity(ctx, post.BountyDisputeBy, models.ActivityBountyDisputeResolved, postID, nil, platformPeerID, nil)
	s.emitActivity(ctx, post.AuthorPeerID, models.ActivityBountyDisputeResolved, postID, nil, platformPeerID, nil)
	return s.repo.GetPostByID(ctx, postID)
}

// --- Reviewer queue ---

// ModerationTarget is one reported post or reply in the reviewer queue, with every open report on it.
type ModerationTarget struct {
	TargetType         string
	TargetID           string
	PostID             string
	TargetAuthorPeerID string
	Excerpt            string
	Hidden             bool
	Deleted            bool
	Reports            []*models.BoardReport
	FirstReportedAt    time.Time
	LastReportedAt     time.Time
}

// ModerationQueue folds the open reports per target, most reported first (then oldest first),
// plus the posts with an open bounty dispute (platform peers).
func (s *ForumService) ModerationQueue(ctx context.Context) ([]*ModerationTarget, []*models.ForumPost, error) {
	targets := []*ModerationTarget{}
	if s.reports != nil {
		reports, err := s.reports.List(ctx, models.ReportStatusOpen, ModerationQueueLimit)
		if err != nil {
			return nil, nil, err
		}
		byKey := map[string]*ModerationTarget{}
		for _, r := range reports {
			key := r.TargetType + ":" + r.TargetID
			t := byKey[key]
			if t == nil {
				t = &ModerationTarget{TargetType: r.TargetType, TargetID: r.TargetID, TargetAuthorPeerID: r.TargetAuthorPeerID, FirstReportedAt: r.CreatedAt, LastReportedAt: r.CreatedAt}
				byKey[key] = t
				targets = append(targets, t)
			}
			t.Reports = append(t.Reports, r)
			if r.CreatedAt.Before(t.FirstReportedAt) {
				t.FirstReportedAt = r.CreatedAt
			}
			if r.CreatedAt.After(t.LastReportedAt) {
				t.LastReportedAt = r.CreatedAt
			}
		}
		for _, t := range targets {
			s.fillModerationTarget(ctx, t)
		}
		sort.SliceStable(targets, func(i, j int) bool {
			if len(targets[i].Reports) != len(targets[j].Reports) {
				return len(targets[i].Reports) > len(targets[j].Reports)
			}
			return targets[i].FirstReportedAt.Before(targets[j].FirstReportedAt)
		})
	}
	disputes, err := s.repo.ListDisputedBounties(ctx, ModerationQueueLimit)
	if err != nil {
		return nil, nil, err
	}
	if disputes == nil {
		disputes = []*models.ForumPost{}
	}
	return targets, disputes, nil
}

// fillModerationTarget adds the target's post id, excerpt and flags.
func (s *ForumService) fillModerationTarget(ctx context.Context, t *ModerationTarget) {
	switch t.TargetType {
	case models.ReportTargetPost:
		if p, err := s.repo.GetPostByID(ctx, t.TargetID); err == nil {
			t.PostID, t.Excerpt, t.Hidden, t.Deleted = p.ID, TruncateRunes(p.Description, 140), p.Hidden, p.IsDeleted()
		}
	case models.ReportTargetReply:
		if r, err := s.repo.GetReplyByID(ctx, t.TargetID); err == nil {
			t.PostID, t.Excerpt, t.Hidden, t.Deleted = r.PostID, TruncateRunes(r.Body, 140), r.Hidden, r.IsDeleted()
		}
	}
}

// --- Room controls ---

// RoomSettings returns the room's settings (defaults without a stored row).
func (s *ForumService) RoomSettings(ctx context.Context, mint string) (*models.RoomSettings, error) {
	if _, err := s.roomLaunch(ctx, mint); err != nil {
		return nil, err
	}
	if s.rooms == nil {
		return models.DefaultRoomSettings(mint), nil
	}
	return s.rooms.GetSettings(ctx, mint)
}

// SetRoomSettings lets the token's agent set the room's routing switch and minimum holding.
func (s *ForumService) SetRoomSettings(ctx context.Context, agentPeerID, mint string, routing bool, minHoldRaw int64) (*models.RoomSettings, error) {
	launch, err := s.roomLaunch(ctx, mint)
	if err != nil {
		return nil, err
	}
	if launch.PeerID == "" || launch.PeerID != agentPeerID {
		return nil, ErrRoomNotAgent
	}
	if minHoldRaw < 1 {
		return nil, models.ErrInvalidInput
	}
	if s.rooms == nil {
		return nil, fmt.Errorf("room controls not configured")
	}
	settings := &models.RoomSettings{Mint: launch.Mint, Routing: routing, MinHoldRaw: minHoldRaw, UpdatedAt: s.now()}
	if err := s.rooms.SetSettings(ctx, settings); err != nil {
		return nil, err
	}
	return settings, nil
}

// MuteInRoom lets the token's agent mute a peer in its room (the agent itself never).
func (s *ForumService) MuteInRoom(ctx context.Context, agentPeerID, mint, targetPeerID, reason string) (*models.RoomMute, error) {
	launch, err := s.roomLaunch(ctx, mint)
	if err != nil {
		return nil, err
	}
	if launch.PeerID == "" || launch.PeerID != agentPeerID {
		return nil, ErrRoomNotAgent
	}
	targetPeerID = strings.TrimSpace(targetPeerID)
	if targetPeerID == "" || targetPeerID == agentPeerID {
		return nil, models.ErrInvalidInput
	}
	if s.rooms == nil {
		return nil, fmt.Errorf("room controls not configured")
	}
	m := &models.RoomMute{Mint: launch.Mint, PeerID: targetPeerID, ByPeerID: agentPeerID, Reason: models.SanitizeBoardLine(reason, models.MaxRoomMuteReasonRunes), CreatedAt: s.now()}
	if err := s.rooms.Mute(ctx, m); err != nil {
		return nil, err
	}
	return m, nil
}

// UnmuteInRoom removes a mute (ErrNotFound when there was none).
func (s *ForumService) UnmuteInRoom(ctx context.Context, agentPeerID, mint, targetPeerID string) error {
	launch, err := s.roomLaunch(ctx, mint)
	if err != nil {
		return err
	}
	if launch.PeerID == "" || launch.PeerID != agentPeerID {
		return ErrRoomNotAgent
	}
	if s.rooms == nil {
		return models.ErrNotFound
	}
	return s.rooms.Unmute(ctx, launch.Mint, strings.TrimSpace(targetPeerID))
}

// ListRoomMutes lists the room's mutes (the token's agent only).
func (s *ForumService) ListRoomMutes(ctx context.Context, agentPeerID, mint string) ([]*models.RoomMute, error) {
	launch, err := s.roomLaunch(ctx, mint)
	if err != nil {
		return nil, err
	}
	if launch.PeerID == "" || launch.PeerID != agentPeerID {
		return nil, ErrRoomNotAgent
	}
	if s.rooms == nil {
		return []*models.RoomMute{}, nil
	}
	out, err := s.rooms.ListMutes(ctx, launch.Mint)
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []*models.RoomMute{}
	}
	return out, nil
}

// roomMuted reports whether the peer is muted in the room (false without the room repo).
func (s *ForumService) roomMuted(ctx context.Context, mint, peerID string) bool {
	if s.rooms == nil || peerID == "" {
		return false
	}
	muted, err := s.rooms.IsMuted(ctx, mint, peerID)
	if err != nil {
		slog.Warn("[forum] room mute lookup failed", "mint", mint, "peer", peerID, "error", err)
		return false
	}
	return muted
}

// roomMinHold returns the room's minimum raw holding to post (1 without a stored row).
func (s *ForumService) roomMinHold(ctx context.Context, mint string) uint64 {
	if s.rooms == nil {
		return 1
	}
	settings, err := s.rooms.GetSettings(ctx, mint)
	if err != nil || settings.MinHoldRaw < 1 {
		return 1
	}
	return uint64(settings.MinHoldRaw)
}

// roomRoutingEnabled reports whether the room routes Requests and Bounties (true without a row).
func (s *ForumService) roomRoutingEnabled(ctx context.Context, mint string) bool {
	if s.rooms == nil {
		return true
	}
	settings, err := s.rooms.GetSettings(ctx, mint)
	if err != nil {
		return true
	}
	return settings.Routing
}

// --- Autopilot per-room cap and circuit breaker ---

// checkAutopilotRound2 applies the global circuit breaker and, on a room post, the per-room
// daily cap of auto replies for the peer.
func (s *ForumService) checkAutopilotRound2(ctx context.Context, post *models.ForumPost, peerID string, now time.Time) error {
	s.round2Defaults()
	if s.autopilotPaused {
		return ErrAutopilotPaused
	}
	if !post.InRoom() || s.autopilotMaxPerRoom < 0 {
		return nil
	}
	n, err := s.repo.CountAutoRepliesByPeerInRoomSince(ctx, peerID, *post.RoomMint, utcDayStart(now))
	if err != nil {
		return fmt.Errorf("autopilot: count room replies today: %w", err)
	}
	if n >= s.autopilotMaxPerRoom {
		return ErrAutoReplyLimit
	}
	return nil
}

// --- Visits and unread ---

// RecordVisit stamps the peer's visit of a feed scope ("" = main feed, else a room mint) and
// returns the previous visit (nil the first time). Without a visit repo it returns nil.
func (s *ForumService) RecordVisit(ctx context.Context, peerID, scope string) (*time.Time, error) {
	if s.visits == nil || peerID == "" {
		return nil, nil
	}
	return s.visits.Visit(ctx, peerID, scope, s.now())
}

// UnreadByScope counts, per scope, the visible posts newer than the peer's last visit; a scope
// never visited counts the last UnreadNeverVisitedWindow. Without a visit repo every count is 0.
func (s *ForumService) UnreadByScope(ctx context.Context, peerID string, scopes []string) map[string]int {
	out := make(map[string]int, len(scopes))
	if s.visits == nil || peerID == "" {
		return out
	}
	visits, err := s.visits.LastVisits(ctx, peerID, scopes)
	if err != nil {
		slog.Warn("[forum] visits lookup failed", "peer", peerID, "error", err)
		return out
	}
	now := s.now()
	for _, scope := range scopes {
		since, ok := visits[scope]
		if !ok {
			since = now.Add(-UnreadNeverVisitedWindow)
		}
		n, err := s.repo.CountPostsSince(ctx, scope, since)
		if err != nil {
			slog.Warn("[forum] unread count failed", "scope", scope, "error", err)
			continue
		}
		out[scope] = n
	}
	return out
}

// --- Notification preferences ---

// NotificationPrefs returns the peer's muted activity kinds (none without a prefs repo).
func (s *ForumService) NotificationPrefs(ctx context.Context, peerID string) (*models.NotificationPrefs, error) {
	if s.prefs == nil {
		return &models.NotificationPrefs{PeerID: peerID, MutedKinds: []string{}}, nil
	}
	return s.prefs.Get(ctx, peerID)
}

// SetNotificationPrefs stores the peer's muted kinds (every one must be a known activity kind;
// duplicates dropped; the order kept).
func (s *ForumService) SetNotificationPrefs(ctx context.Context, peerID string, mutedKinds []string) (*models.NotificationPrefs, error) {
	kinds := make([]string, 0, len(mutedKinds))
	seen := map[string]bool{}
	for _, k := range mutedKinds {
		k = strings.ToLower(strings.TrimSpace(k))
		if k == "" || seen[k] {
			continue
		}
		if !models.IsValidActivityKind(k) {
			return nil, models.ErrInvalidInput
		}
		seen[k] = true
		kinds = append(kinds, k)
	}
	p := &models.NotificationPrefs{PeerID: peerID, MutedKinds: kinds, UpdatedAt: s.now()}
	if s.prefs == nil {
		return p, nil
	}
	if err := s.prefs.Set(ctx, p); err != nil {
		return nil, err
	}
	return p, nil
}

// mutedKinds returns the peer's muted kinds ("" and errors degrade to none).
func (s *ForumService) mutedKinds(ctx context.Context, peerID string) []string {
	if s.prefs == nil || peerID == "" {
		return nil
	}
	p, err := s.prefs.Get(ctx, peerID)
	if err != nil {
		slog.Warn("[forum] notification prefs lookup failed", "peer", peerID, "error", err)
		return nil
	}
	return p.MutedKinds
}
