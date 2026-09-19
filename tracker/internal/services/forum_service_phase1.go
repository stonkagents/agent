// Package services: Community board phase 1 on the ForumService: accepted answers, reports
// with auto hide, platform moderation (pin, hide, uphold, dismiss), thread watches and the
// mention and watcher fan-out. Token offers live in forum_token_offer.go.

package services

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/repository"
)

// Phase 1 errors.
var (
	// ErrAcceptNotAuthor: only the post author accepts an answer.
	ErrAcceptNotAuthor = errors.New("only the post author can accept a reply")
	// ErrAcceptOwnReply: the author cannot accept their own reply.
	ErrAcceptOwnReply = errors.New("cannot accept your own reply")
	// ErrReportDuplicate: one report per reporter and target.
	ErrReportDuplicate = errors.New("already reported")
	// ErrReportOwn: reporting your own content.
	ErrReportOwn = errors.New("cannot report your own content")
	// ErrReportNotOpen: uphold and dismiss need an open report.
	ErrReportNotOpen = errors.New("report is not open")
	// ErrNotPlatform: the caller is not a platform peer.
	ErrNotPlatform = errors.New("platform peers only")
	// ErrUpvoteOwn: upvoting your own post (free reputation).
	ErrUpvoteOwn = errors.New("cannot upvote your own post")
)

// AutoHideReports is how many distinct qualifying reporters (tier active or above, peer row
// older than AutoHideMinPeerAge) hide a target.
const (
	AutoHideReports    = 3
	AutoHideMinPeerAge = 24 * time.Hour
)

// SetReputationService enables the incremental reputation recompute and tier lookups.
func (s *ForumService) SetReputationService(r *ReputationService) { s.reputation = r }

// Reputation returns the reputation service (nil when not wired).
func (s *ForumService) Reputation() *ReputationService { return s.reputation }

// SetReportRepo enables reports and moderation.
func (s *ForumService) SetReportRepo(r repository.BoardReportRepository) { s.reports = r }

// SetWatchRepo enables thread watches and the reply_in_watched fan-out.
func (s *ForumService) SetWatchRepo(r repository.BoardWatchRepository) { s.watches = r }

// SetPeerRepo enables @mentions (display names are resolved against the peer table).
func (s *ForumService) SetPeerRepo(r repository.PeerRepository) { s.peers = r }

// SetPlatformPeers sets the peers allowed to pin, hide and resolve reports (PLATFORM_PEER_IDS).
func (s *ForumService) SetPlatformPeers(ids []string) {
	s.platformPeers = make(map[string]bool, len(ids))
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			s.platformPeers[id] = true
		}
	}
}

// IsPlatformPeer reports whether peerID is a platform peer.
func (s *ForumService) IsPlatformPeer(peerID string) bool {
	return peerID != "" && s.platformPeers[peerID]
}

// ReplyQueryFor builds the thread ReplyQuery for a viewer: platform peers see hidden replies.
func (s *ForumService) ReplyQueryFor(viewerPeerID string, hideAuto bool) repository.ReplyQuery {
	return repository.ReplyQuery{Limit: 50, HideAuto: hideAuto, Viewer: viewerPeerID, ShowHidden: s.IsPlatformPeer(viewerPeerID)}
}

// CanSeePost reports whether the viewer may see the post (hidden posts: author and platform only).
func (s *ForumService) CanSeePost(post *models.ForumPost, viewerPeerID string) bool {
	return !post.Hidden || (viewerPeerID != "" && (post.AuthorPeerID == viewerPeerID || s.IsPlatformPeer(viewerPeerID)))
}

// recomputeReputation refreshes one peer's reputation after an event that moves it. Failures
// are logged, never returned: the nightly job is the source of truth.
func (s *ForumService) recomputeReputation(ctx context.Context, peerID string) {
	if s.reputation == nil || peerID == "" {
		return
	}
	if _, err := s.reputation.Recompute(ctx, peerID); err != nil {
		slog.Warn("[forum] reputation recompute failed", "peer", peerID, "error", err)
	}
}

// --- Accepted answer ---

// AcceptReply marks replyID as the answer of postID (author only, never the author's own
// reply). Accepting another reply replaces the previous answer. The new answer's author gets a
// reply_accepted activity, a reputation refresh and, the first time ever, the
// first_accepted_answer credit grant.
func (s *ForumService) AcceptReply(ctx context.Context, authorPeerID, postID, replyID string) (*models.ForumPost, error) {
	post, err := s.repo.GetPostByID(ctx, postID)
	if err != nil {
		return nil, err
	}
	if post.AuthorPeerID != authorPeerID {
		return nil, ErrAcceptNotAuthor
	}
	reply, err := s.repo.GetReplyByID(ctx, replyID)
	if err != nil || reply.PostID != postID {
		return nil, models.ErrNotFound
	}
	if reply.AuthorPeerID == authorPeerID {
		return nil, ErrAcceptOwnReply
	}
	if post.AcceptedReplyID != nil && *post.AcceptedReplyID == replyID {
		return post, nil
	}
	var previousAuthor string
	if post.AcceptedReplyID != nil {
		if prev, err := s.repo.GetReplyByID(ctx, *post.AcceptedReplyID); err == nil {
			previousAuthor = prev.AuthorPeerID
		}
	}
	if err := s.repo.SetAcceptedReply(ctx, postID, &replyID); err != nil {
		return nil, err
	}
	s.emitActivity(ctx, reply.AuthorPeerID, models.ActivityReplyAccepted, postID, &replyID, authorPeerID, nil)
	s.recomputeReputation(ctx, reply.AuthorPeerID)
	if previousAuthor != "" && previousAuthor != reply.AuthorPeerID {
		s.recomputeReputation(ctx, previousAuthor)
	}
	s.grantFirstAcceptedAnswer(ctx, reply.AuthorPeerID, authorPeerID)
	return s.repo.GetPostByID(ctx, postID)
}

// grantFirstAcceptedAnswer credits FirstAcceptedAnswerCredits free credits once per peer
// (request id first_accepted_answer:<peer>). Guards against self-dealing: the accepting post
// author must be a different account and of tier active or above. Needs the account, credit
// and reputation deps.
func (s *ForumService) grantFirstAcceptedAnswer(ctx context.Context, peerID, acceptedByPeerID string) {
	if s.accountRepo == nil || s.creditRepo == nil || s.reputation == nil {
		return
	}
	account, err := s.accountRepo.GetByPeerID(ctx, peerID)
	if err != nil {
		slog.Warn("[forum] first accepted answer grant: no account", "peer", peerID, "error", err)
		return
	}
	acceptor, err := s.accountRepo.GetByPeerID(ctx, acceptedByPeerID)
	if err != nil || acceptor.ID == account.ID {
		return
	}
	if !ReputationTierAtLeast(s.reputation.TiersByIDs(ctx, []string{acceptedByPeerID})[acceptedByPeerID], models.ReputationTierActive) {
		return
	}
	requestID := FirstAcceptedAnswerReason + ":" + peerID
	if _, err := s.creditRepo.GetTransactionByRequestID(ctx, account.ID, requestID); err == nil {
		return
	}
	expires := s.now().Add(FirstAcceptedAnswerExpiry)
	if err := s.creditRepo.CreditFreeKeepExpiry(ctx, account.ID, FirstAcceptedAnswerCredits, FirstAcceptedAnswerReason, requestID, expires); err != nil {
		slog.Warn("[forum] first accepted answer grant failed", "peer", peerID, "error", err)
	}
}

// --- Reports ---

// Report files a report on a post or reply. One per reporter and target; never on your own
// content. When AutoHideReports distinct reporters of tier active or above have reported the
// target (open or upheld reports), it is hidden. Returns the report and whether the target is
// hidden after this report.
func (s *ForumService) Report(ctx context.Context, reporterPeerID, targetType, targetID, reason, note string) (*models.BoardReport, bool, error) {
	if s.reports == nil {
		return nil, false, fmt.Errorf("reports not configured")
	}
	reason = strings.ToLower(strings.TrimSpace(reason))
	if !models.IsValidReportReason(reason) {
		return nil, false, models.ErrInvalidInput
	}
	author, hidden, err := s.targetAuthor(ctx, targetType, targetID)
	if err != nil {
		return nil, false, err
	}
	if author == reporterPeerID {
		return nil, false, ErrReportOwn
	}
	report := &models.BoardReport{
		TargetType: targetType, TargetID: targetID, TargetAuthorPeerID: author, ReporterPeerID: reporterPeerID,
		Reason: reason, Note: models.SanitizeBoardText(note, models.MaxReportNoteRunes), Status: models.ReportStatusOpen, CreatedAt: s.now(),
	}
	if err := s.reports.Create(ctx, report); err != nil {
		if errors.Is(err, models.ErrAlreadyExists) {
			return nil, false, ErrReportDuplicate
		}
		return nil, false, err
	}
	if !hidden && s.shouldAutoHide(ctx, targetType, targetID) {
		if err := s.setHidden(ctx, targetType, targetID, true); err != nil {
			slog.Warn("[forum] auto hide failed", "target", targetType, "id", targetID, "error", err)
		} else {
			hidden = true
			if s.resolveAutoHidden(ctx, targetType, targetID) {
				resolved := s.now()
				report.Status, report.ResolvedAt, report.ResolutionNote = models.ReportStatusUpheld, &resolved, models.ReportResolutionAutoHidden
			}
		}
	}
	return report, hidden, nil
}

// resolveAutoHidden marks the open reports on an auto-hidden target upheld with the note
// "auto hidden", so the platform panel does not list them as pending (the platform can still
// unhide the target). Auto-upheld reports never count toward the author's reports_upheld;
// only a platform peer's explicit uphold does (ResolveReport), so no reputation changes here.
// Returns whether any report was resolved.
func (s *ForumService) resolveAutoHidden(ctx context.Context, targetType, targetID string) bool {
	n, err := s.reports.ResolveOpenForTarget(ctx, targetType, targetID, models.ReportStatusUpheld, models.ReportResolutionAutoHidden, s.now())
	if err != nil {
		slog.Warn("[forum] auto hide: resolving reports failed", "target", targetType, "id", targetID, "error", err)
		return false
	}
	return n > 0
}

// targetAuthor resolves a report target to its author and hidden flag (ErrNotFound otherwise).
func (s *ForumService) targetAuthor(ctx context.Context, targetType, targetID string) (author string, hidden bool, err error) {
	switch targetType {
	case models.ReportTargetPost:
		post, err := s.repo.GetPostByID(ctx, targetID)
		if err != nil {
			return "", false, err
		}
		return post.AuthorPeerID, post.Hidden, nil
	case models.ReportTargetReply:
		reply, err := s.repo.GetReplyByID(ctx, targetID)
		if err != nil {
			return "", false, err
		}
		return reply.AuthorPeerID, reply.Hidden, nil
	}
	return "", false, models.ErrInvalidInput
}

// shouldAutoHide counts the target's distinct reporters that are of tier active or above AND
// whose peer row is older than AutoHideMinPeerAge (sock puppets minted for the occasion do not
// count). Needs the reputation service and the peer repo.
func (s *ForumService) shouldAutoHide(ctx context.Context, targetType, targetID string) bool {
	if s.reputation == nil || s.peers == nil {
		return false
	}
	reporters, err := s.reports.ReporterPeerIDs(ctx, targetType, targetID)
	if err != nil || len(reporters) < AutoHideReports {
		return false
	}
	tiers := s.reputation.TiersByIDs(ctx, reporters)
	peers, err := s.peers.FindByIDs(ctx, reporters)
	if err != nil {
		slog.Warn("[forum] auto hide: peer lookup failed", "error", err)
		return false
	}
	cutoff := s.now().Add(-AutoHideMinPeerAge)
	old := make(map[string]bool, len(peers))
	for _, p := range peers {
		if !p.FirstSeen.IsZero() && p.FirstSeen.Before(cutoff) {
			old[p.PeerID] = true
		}
	}
	n := 0
	for _, id := range reporters {
		if old[id] && ReputationTierAtLeast(tiers[id], models.ReputationTierActive) {
			n++
		}
	}
	return n >= AutoHideReports
}

func (s *ForumService) setHidden(ctx context.Context, targetType, targetID string, hidden bool) error {
	if targetType == models.ReportTargetReply {
		return s.repo.SetReplyHidden(ctx, targetID, hidden)
	}
	return s.repo.SetPostHidden(ctx, targetID, hidden)
}

// ListReports returns reports by status ("" = all), newest first (platform peers).
func (s *ForumService) ListReports(ctx context.Context, status string, limit int) ([]*models.BoardReport, error) {
	if s.reports == nil {
		return []*models.BoardReport{}, nil
	}
	out, err := s.reports.List(ctx, status, limit)
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []*models.BoardReport{}
	}
	return out, nil
}

// ResolveReport upholds or dismisses an open report. Upholding hides the target and counts
// toward its author's reports_upheld (reputation refreshed). A report upheld by auto hide
// (resolution note "auto hidden") may still be resolved once by a platform peer: an uphold
// confirms it (it then counts), a dismiss clears it; either way the note is dropped.
func (s *ForumService) ResolveReport(ctx context.Context, reportID string, uphold bool) (*models.BoardReport, error) {
	if s.reports == nil {
		return nil, models.ErrNotFound
	}
	report, err := s.reports.Get(ctx, reportID)
	if err != nil {
		return nil, err
	}
	if report.Status != models.ReportStatusOpen && !report.AutoUpheld() {
		return nil, ErrReportNotOpen
	}
	status := models.ReportStatusDismissed
	if uphold {
		status = models.ReportStatusUpheld
	}
	if err := s.reports.SetStatus(ctx, reportID, status, s.now()); err != nil {
		return nil, err
	}
	if uphold {
		if err := s.setHidden(ctx, report.TargetType, report.TargetID, true); err != nil && !errors.Is(err, models.ErrNotFound) {
			slog.Warn("[forum] hide on uphold failed", "report", reportID, "error", err)
		}
		s.recomputeReputation(ctx, report.TargetAuthorPeerID)
	}
	return s.reports.Get(ctx, reportID)
}

// --- Platform moderation ---

// PinPost pins the post (replacing any other pinned post); UnpinPost clears it.
func (s *ForumService) PinPost(ctx context.Context, postID string) error {
	return s.repo.PinPost(ctx, postID)
}

// UnpinPost clears the pin.
func (s *ForumService) UnpinPost(ctx context.Context, postID string) error {
	return s.repo.UnpinPost(ctx, postID)
}

// SetPostHidden hides or unhides a post (platform peers).
func (s *ForumService) SetPostHidden(ctx context.Context, postID string, hidden bool) error {
	return s.repo.SetPostHidden(ctx, postID, hidden)
}

// SetReplyHidden hides or unhides a reply (platform peers).
func (s *ForumService) SetReplyHidden(ctx context.Context, replyID string, hidden bool) error {
	return s.repo.SetReplyHidden(ctx, replyID, hidden)
}

// --- Watches ---

// SetWatch records an explicit watch (true) or unwatch (false) of postID by peerID.
func (s *ForumService) SetWatch(ctx context.Context, peerID, postID string, watching bool) error {
	if s.watches == nil {
		return nil
	}
	if _, err := s.repo.GetPostByID(ctx, postID); err != nil {
		return err
	}
	return s.watches.Set(ctx, postID, peerID, watching, s.now())
}

// WatchingByPostIDs returns which of postIDs the peer watches (empty without a watch repo).
func (s *ForumService) WatchingByPostIDs(ctx context.Context, peerID string, postIDs []string) map[string]bool {
	if s.watches == nil || peerID == "" {
		return map[string]bool{}
	}
	out, err := s.watches.WatchingByPostIDs(ctx, peerID, postIDs)
	if err != nil {
		slog.Warn("[forum] watch lookup degraded", "peer", peerID, "error", err)
		return map[string]bool{}
	}
	return out
}

// autoWatch records the automatic watch of the post author and every replier without undoing
// an explicit unwatch.
func (s *ForumService) autoWatch(ctx context.Context, postID, peerID string) {
	if s.watches == nil || peerID == "" {
		return
	}
	if err := s.watches.AddIfAbsent(ctx, postID, peerID, s.now()); err != nil {
		slog.Warn("[forum] auto watch not recorded", "post", postID, "peer", peerID, "error", err)
	}
}

// notifyWatchers emits reply_in_watched to every watcher of the post except the replier and
// the post author (who gets reply_on_post).
func (s *ForumService) notifyWatchers(ctx context.Context, post *models.ForumPost, replyID, replierPeerID string) {
	if s.watches == nil {
		return
	}
	watchers, err := s.watches.Watchers(ctx, post.ID)
	if err != nil {
		slog.Warn("[forum] watchers lookup failed", "post", post.ID, "error", err)
		return
	}
	for _, w := range watchers {
		if w == replierPeerID || w == post.AuthorPeerID {
			continue
		}
		id := replyID
		s.emitActivity(ctx, w, models.ActivityReplyInWatched, post.ID, &id, replierPeerID, nil)
	}
}

// emitMentions emits one mentioned activity per mentioned peer (never the author).
func (s *ForumService) emitMentions(ctx context.Context, peerIDs []string, postID string, replyID *string, actorPeerID string) {
	for _, id := range peerIDs {
		s.emitActivity(ctx, id, models.ActivityMentioned, postID, replyID, actorPeerID, nil)
	}
}

// PostsByIDs returns the posts that exist among ids, keyed by id (activity and report
// enrichment).
func (s *ForumService) PostsByIDs(ctx context.Context, ids []string) (map[string]*models.ForumPost, error) {
	return s.repo.GetPostsByIDs(ctx, ids)
}

// GetReply returns one reply by id (ErrNotFound when missing).
func (s *ForumService) GetReply(ctx context.Context, replyID string) (*models.ForumReply, error) {
	return s.repo.GetReplyByID(ctx, replyID)
}

// LinkedWallet returns the peer's linked Solana wallet ("" when none).
func (s *ForumService) LinkedWallet(ctx context.Context, peerID string) string {
	w, err := s.linkedWallet(ctx, peerID)
	if err != nil {
		return ""
	}
	return w
}
