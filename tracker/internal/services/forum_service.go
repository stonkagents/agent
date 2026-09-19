// Package services: Forum business logic (posts, replies, upvotes).
// Feature: F-031 (Token Data Persistence)
// Story: US-031-05 (Community Board End-to-End)
// Purpose: Forum service with rich post creation (bounty, token offer, CID, category, tags, view count)
package services

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/stonkagents/agent/tracker/internal/clock"
	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/presence"
	"github.com/stonkagents/agent/tracker/internal/repository"
)

// Bounty lifecycle errors.
var (
	ErrBountyNotClaimable  = errors.New("bounty is not available for award")
	ErrBountyOwnPost       = errors.New("cannot award bounty to yourself")
	ErrBountyNotAuthor     = errors.New("only the post author can do this")
	ErrInsufficientCredits = errors.New("insufficient credits for bounty escrow")
	// ErrBountyNotOpen: the bounty is completed or expired (extend needs an open one).
	ErrBountyNotOpen = errors.New("bounty is not open")
	// ErrBountyAlreadyExtended: a post gets one extension.
	ErrBountyAlreadyExtended = errors.New("bounty was already extended")
)

// CreditRefunder returns an escrow to the buckets it came from (CreditService.RefundEscrow).
type CreditRefunder interface {
	RefundEscrow(ctx context.Context, accountID string, fromFree, fromPaid int, reason, originalRequestID string) error
}

// Agent Autopilot errors (auto replies only).
var (
	// ErrAutoReplyLimit: the peer already auto-replied on this post, or hit the daily cap.
	ErrAutoReplyLimit = errors.New("autopilot reply limit reached")
	// ErrAutoReplyNotAllowed: token-offer post or the peer's own post.
	ErrAutoReplyNotAllowed = errors.New("autopilot reply not allowed on this post")
)

const (
	// MaxBountyAmount caps the bounty amount to prevent abuse.
	MaxBountyAmount = 10000
	// MaxBountyDays caps the bounty deadline.
	MaxBountyDays = 90
	// DefaultBountyDays is the deadline when the poster gives none, so every escrow expires.
	DefaultBountyDays = 7
	// DefaultAutopilotMaxRepliesPerDay is the per-peer UTC-day cap on auto replies (AUTOPILOT_MAX_REPLIES_PER_DAY).
	DefaultAutopilotMaxRepliesPerDay = 10
	// AutopilotOutcomeWindow is how far back GET /autopilot/outcomes looks.
	AutopilotOutcomeWindow = 30 * 24 * time.Hour
	// BountyExtension is how far one extend call pushes bounty_expires_at.
	BountyExtension = 7 * 24 * time.Hour
	// BountyExpiringWindow is how close to expiry an open bounty triggers a bounty_expiring activity.
	BountyExpiringWindow = 24 * time.Hour
	// BountyRefundReason is the credit transaction reason for a returned escrow.
	BountyRefundReason = "bounty_refund"
	// BoardCountsTTL is how long GET /api/board/counts is served from memory.
	BoardCountsTTL = 30 * time.Second
	// MaxSearchQueryLen caps the q= parameter.
	MaxSearchQueryLen = 200
)

// validCategories is the set of allowed post categories.
var validCategories = map[string]bool{
	"general":     true,
	"request":     true,
	"bounty":      true,
	"token-offer": true,
	"discovery":   true,
}

// CreatePostInput carries all fields for creating a rich forum post.
type CreatePostInput struct {
	Title            string
	Description      string
	Category         string
	Tags             []string
	BountyAmount     *int
	BountyCurrency   *string
	BountyDays       *int
	TokenOfferAmount *int
	TokenOfferToken  *string
	CID              *string
	// Phase 1 token offer that settles wallet to wallet: the launch mint, the per-reply amount
	// in raw units and how many replies can be paid. A non-empty mint takes this path and
	// ignores TokenOfferAmount / TokenOfferToken.
	TokenOfferMint      string
	TokenOfferAmountRaw int64
	TokenOfferMax       int
	// RoomMint (phase 2) posts into the token room of that launch mint; the poster must be the
	// token's agent or hold the mint (ErrRoomNotHolder otherwise).
	RoomMint string
	// Auto marks a post the agent made on its own (Agent Autopilot, the weekly digest).
	Auto bool
}

// ForumService handles forum posts, replies, upvotes, and bounty lifecycle.
type ForumService struct {
	repo        repository.ForumRepository
	accountRepo repository.AccountRepository
	creditRepo  repository.CreditRepository
	clock       clock.Clock
	// activity is the per-peer activity feed; nil disables fan-out.
	activity repository.BoardActivityRepository
	// credits returns escrow on bounty expiry; nil means expired bounties only flip status.
	credits CreditRefunder

	// AutopilotMaxRepliesPerDay caps auto replies per peer per UTC day; 0 disables the daily cap.
	AutopilotMaxRepliesPerDay int

	// Phase 1 (all optional; nil disables the feature that needs it, see forum_service_phase1.go).
	reputation    *ReputationService
	reports       repository.BoardReportRepository
	watches       repository.BoardWatchRepository
	peers         repository.PeerRepository
	platformPeers map[string]bool
	launches      repository.LaunchRepository
	wallets       repository.WalletRepository
	payments      repository.TokenOfferPaymentRepository
	txVerifier    TokenTransferVerifier

	// Phase 2 (rooms and matchmaking; see forum_rooms.go, forum_routing.go, forum_digest.go).
	holders    TokenHolderChecker
	metrics    RoomMetricsProvider
	snapshots  repository.RoomHolderSnapshotRepository
	autopilot  repository.PeerAutopilotRepository
	presence   presence.PresenceStore
	quotes     repository.LaunchQuoteRepository
	raiseUnits float64
	portalURL  string
	holderMu   sync.Mutex
	holderCach map[string]holderCacheEntry

	// countsCached is keyed by room mint ("" = the main feed).
	countsMu     sync.Mutex
	countsCached map[string]*repository.PostCounts
	countsAt     map[string]time.Time

	// Round 2 (see forum_round2.go; every repo optional, settings default when zero).
	edits               repository.BoardEditHistoryRepository
	rooms               repository.BoardRoomRepository
	visits              repository.BoardVisitRepository
	prefs               repository.BoardNotificationPrefRepository
	editWindow          time.Duration
	maxUpvotesPerDay    int
	autopilotMaxPerRoom int
	autopilotPaused     bool
	minBounty           int
	maxBounty           int
	dupWindow           time.Duration
}

// NewForumService creates a new ForumService. accountRepo and creditRepo are optional (nil disables bounty escrow).
func NewForumService(repo repository.ForumRepository, accountRepo repository.AccountRepository, creditRepo repository.CreditRepository) *ForumService {
	return &ForumService{repo: repo, accountRepo: accountRepo, creditRepo: creditRepo, AutopilotMaxRepliesPerDay: DefaultAutopilotMaxRepliesPerDay}
}

// SetActivityRepo enables the activity feed fan-out (CreateReply, AwardBounty, ToggleUpvote, ExpireBounties).
func (s *ForumService) SetActivityRepo(repo repository.BoardActivityRepository) {
	s.activity = repo
}

// SetCreditRefunder enables escrow refunds on bounty expiry (normally the CreditService).
func (s *ForumService) SetCreditRefunder(c CreditRefunder) {
	s.credits = c
}

// BoardTextError is a 400 for board text that breaks a cap: its message names the field and
// the limit ("body must be at most 10000 characters", "tags must be at most 10 entries").
type BoardTextError struct {
	Field string
	Limit int
	// Unit is "characters" when empty.
	Unit string
	// Message, when set, is the whole text (link rules).
	Message string
}

func (e *BoardTextError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	unit := e.Unit
	if unit == "" {
		unit = "characters"
	}
	return fmt.Sprintf("%s must be at most %d %s", e.Field, e.Limit, unit)
}

// boardText sanitises free text and enforces its cap: ErrInvalidInput when nothing is left
// and required, a BoardTextError when the sanitised text is longer than maxRunes.
func boardText(field, raw string, maxRunes int, required bool) (string, error) {
	s := models.SanitizeBoardText(raw, 0)
	if s == "" && required {
		return "", models.ErrInvalidInput
	}
	if models.BoardTextTooLong(s, maxRunes) {
		return "", &BoardTextError{Field: field, Limit: maxRunes}
	}
	return s, nil
}

// boardTags sanitises the tags of a post: each a single line of at most MaxBoardTagRunes,
// empty and duplicate ones dropped, more than MaxBoardTags refused.
func boardTags(raw []string) ([]string, error) {
	tags := make([]string, 0, len(raw))
	seen := map[string]bool{}
	for _, t := range raw {
		t = models.SanitizeBoardLine(t, 0)
		if t == "" || models.BoardTextTooLong(t, models.MaxBoardTagRunes) {
			if t != "" {
				return nil, &BoardTextError{Field: "tag", Limit: models.MaxBoardTagRunes}
			}
			continue
		}
		if seen[strings.ToLower(t)] {
			continue
		}
		seen[strings.ToLower(t)] = true
		tags = append(tags, t)
	}
	if len(tags) > models.MaxBoardTags {
		return nil, &BoardTextError{Field: "tags", Limit: models.MaxBoardTags, Unit: "entries"}
	}
	return tags, nil
}

// CreatePost creates a new post with rich fields. authorPeerID is set by caller (from API key).
// Title, body, tags and the legacy token offer label are sanitised (models.SanitizeBoardText)
// and capped; the bounty currency must be credits.
func (s *ForumService) CreatePost(ctx context.Context, authorPeerID string, input CreatePostInput) (*models.ForumPost, error) {
	title := models.SanitizeBoardLine(input.Title, models.MaxBoardTitleRunes)
	if title == "" {
		return nil, models.ErrInvalidInput
	}
	description, err := boardText("body", input.Description, models.MaxBoardBodyRunes, true)
	if err != nil {
		return nil, err
	}
	if err := checkBodyLinks(description); err != nil {
		return nil, err
	}
	s.round2Defaults()
	bodyHash := models.BoardBodyHash(description)
	if s.duplicatePost(ctx, authorPeerID, bodyHash) != "" {
		return nil, ErrDuplicatePost
	}

	category := strings.TrimSpace(input.Category)
	if category == "" {
		category = "general"
	}
	if !validCategories[category] {
		return nil, models.ErrInvalidInput
	}

	tags, err := boardTags(input.Tags)
	if err != nil {
		return nil, err
	}

	now := s.now()
	post := &models.ForumPost{
		AuthorPeerID: authorPeerID,
		Title:        title,
		Description:  description,
		Category:     category,
		Tags:         tags,
		CreatedAt:    now,
		UpdatedAt:    now,
		UpvoteCount:  0,
		ReplyCount:   0,
		ViewCount:    0,
		Auto:         input.Auto,
		BodyHash:     bodyHash,
	}

	// Bounty fields, validated as a group: within the configured bounds.
	if input.BountyAmount != nil && *input.BountyAmount > 0 {
		if *input.BountyAmount > s.maxBounty {
			return nil, models.ErrInvalidInput
		}
		if *input.BountyAmount < s.minBounty {
			return nil, ErrBountyBelowMin
		}
		post.BountyAmount = input.BountyAmount
		// Credits are the only bounty currency; the escrow below is in credits whatever the
		// client wrote, so any other label would be a lie on the card.
		currency := "credits"
		if input.BountyCurrency != nil && strings.TrimSpace(*input.BountyCurrency) != "" && !strings.EqualFold(strings.TrimSpace(*input.BountyCurrency), currency) {
			return nil, models.ErrInvalidInput
		}
		post.BountyCurrency = &currency
		// Every bounty gets a deadline so its escrow always comes back: DefaultBountyDays when
		// the poster gives none.
		days := DefaultBountyDays
		if input.BountyDays != nil && *input.BountyDays > 0 {
			days = *input.BountyDays
		}
		if days > MaxBountyDays {
			return nil, models.ErrInvalidInput
		}
		exp := now.Add(time.Duration(days) * 24 * time.Hour)
		post.BountyExpiresAt = &exp
	}

	// Token offer fields — validated as a group.
	// Phase 1 (a mint is given): the mint must be a launch on this platform and the poster must
	// have a linked wallet (the token's agent included); symbol and decimals come from the launch.
	// Legacy (no mint): TokenOfferToken is a user-declared display name (e.g. "$STNK"), not
	// validated against any on-chain registry. The backend stores it as-is; the frontend displays it.
	if strings.TrimSpace(input.TokenOfferMint) != "" {
		if err := s.applyTokenOffer(ctx, post, authorPeerID, input); err != nil {
			return nil, err
		}
	} else if input.TokenOfferAmount != nil && *input.TokenOfferAmount > 0 {
		post.TokenOfferAmount = input.TokenOfferAmount
		if input.TokenOfferToken != nil {
			if label := models.SanitizeBoardLine(*input.TokenOfferToken, models.MaxTokenOfferLabelRunes); label != "" {
				post.TokenOfferToken = &label
			}
		}
	}
	post.MentionPeerIDs = ResolveMentions(ctx, s.peers, title+"\n"+description, authorPeerID)

	// Token room (phase 2): the poster must be the token's agent or hold the mint.
	if mint := strings.TrimSpace(input.RoomMint); mint != "" {
		launch, err := s.roomLaunch(ctx, mint)
		if err != nil {
			return nil, err
		}
		if _, err := s.RoomAccess(ctx, launch, authorPeerID); err != nil {
			return nil, err
		}
		post.RoomMint = &mint
	}

	// CID (optional — link to a shared file). Must be a valid IPFS CIDv0 ("Qm...") or CIDv1 ("bafy...").
	if input.CID != nil && *input.CID != "" {
		cid := strings.TrimSpace(*input.CID)
		if !isValidCID(cid) {
			return nil, models.ErrInvalidInput
		}
		post.CID = &cid
	}

	// Bounty escrow: deduct credits from author before creating post. A bounty starts open
	// whether or not escrow is wired (matches the Postgres column default).
	if post.HasBounty() {
		post.BountyStatus = "open"
	}
	if post.HasBounty() && s.accountRepo != nil && s.creditRepo != nil {
		account, err := s.accountRepo.GetByPeerID(ctx, authorPeerID)
		if err != nil {
			return nil, fmt.Errorf("bounty escrow: resolve account: %w", err)
		}
		requestID := fmt.Sprintf("bounty_escrow:%s:%d", authorPeerID, now.UnixNano())
		_, fromPaid, err := s.creditRepo.SpendSplit(ctx, account.ID, *post.BountyAmount, "bounty_escrow", requestID)
		if err != nil {
			return nil, ErrInsufficientCredits
		}
		post.BountyEscrowRequestID = &requestID
		post.BountyEscrowPaid = fromPaid
	}

	if err := s.repo.CreatePost(ctx, post); err != nil {
		return nil, err
	}
	s.autoWatch(ctx, post.ID, authorPeerID)
	s.emitMentions(ctx, post.MentionPeerIDs, post.ID, nil, authorPeerID)
	s.routeRequest(ctx, post)
	return post, nil
}

// GetPost returns a post by ID.
func (s *ForumService) GetPost(ctx context.Context, postID string) (*models.ForumPost, error) {
	return s.repo.GetPostByID(ctx, postID)
}

// GetPostAndIncrementViews returns a post and atomically increments its view count.
func (s *ForumService) GetPostAndIncrementViews(ctx context.Context, postID string) (*models.ForumPost, error) {
	post, err := s.repo.GetPostByID(ctx, postID)
	if err != nil {
		return nil, err
	}
	viewCount, err := s.repo.IncrementViewCount(ctx, postID)
	if err != nil {
		return nil, err
	}
	post.ViewCount = viewCount
	return post, nil
}

// IncrementViewCount atomically increments and returns the view count for a post.
func (s *ForumService) IncrementViewCount(ctx context.Context, postID string) (int, error) {
	return s.repo.IncrementViewCount(ctx, postID)
}

// ListPosts returns posts with total. sortNewest: true = newest first, false = top by upvotes.
func (s *ForumService) ListPosts(ctx context.Context, limit, offset int, sortNewest bool) ([]*models.ForumPost, int, error) {
	return s.repo.ListPosts(ctx, limit, offset, sortNewest)
}

// QueryPosts is the filtered board feed (category, search, mine, bounties sort). The search
// text is capped at MaxSearchQueryLen and the open-bounty reference time is the service clock.
func (s *ForumService) QueryPosts(ctx context.Context, q repository.PostQuery) ([]*models.ForumPost, int, error) {
	q.Search = TruncateRunes(strings.TrimSpace(q.Search), MaxSearchQueryLen)
	if q.Mine != "" && q.MinePeerID == "" {
		return nil, 0, models.ErrInvalidInput
	}
	q.Now = s.now()
	posts, total, err := s.repo.QueryPosts(ctx, q)
	if err != nil {
		return nil, 0, err
	}
	if posts == nil {
		posts = []*models.ForumPost{}
	}
	return posts, total, nil
}

// TruncateRunes cuts s to at most n runes (never inside a multibyte character).
func TruncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// BoardActivity is the peer's public board footprint (visible posts and replies, newest
// activity time) behind the agent activity view.
func (s *ForumService) BoardActivity(ctx context.Context, peerID string) (*repository.BoardActivity, error) {
	if strings.TrimSpace(peerID) == "" {
		return nil, models.ErrInvalidInput
	}
	return s.repo.BoardActivity(ctx, peerID)
}

// BoardCounts returns the feed aggregates, served from memory for BoardCountsTTL. roomMint
// scopes them to one token room ("" = the main feed, room posts left out).
func (s *ForumService) BoardCounts(ctx context.Context, roomMint string) (*repository.PostCounts, error) {
	now := s.now()
	s.countsMu.Lock()
	defer s.countsMu.Unlock()
	if s.countsCached == nil {
		s.countsCached = map[string]*repository.PostCounts{}
		s.countsAt = map[string]time.Time{}
	}
	if cached := s.countsCached[roomMint]; cached != nil && now.Sub(s.countsAt[roomMint]) < BoardCountsTTL {
		return cached, nil
	}
	counts, err := s.repo.CountPosts(ctx, now, roomMint)
	if err != nil {
		return nil, err
	}
	s.countsCached[roomMint] = counts
	s.countsAt[roomMint] = now
	return counts, nil
}

// ToggleUpvote adds or removes upvote; returns new upvote count and whether the user has upvoted.
// Adding an upvote notifies the post author once per upvoter (post_upvoted); removing never does.
func (s *ForumService) ToggleUpvote(ctx context.Context, peerID, postID string) (upvoteCount int, upvotedByMe bool, err error) {
	post, err := s.repo.GetPostByID(ctx, postID)
	if err != nil {
		return 0, false, err
	}
	if !s.CanSeePost(post, peerID) {
		// A hidden post is not there for anyone but its author and the platform.
		return 0, false, models.ErrNotFound
	}
	if post.IsDeleted() {
		return 0, false, ErrDeleted
	}
	if post.AuthorPeerID == peerID {
		return 0, false, ErrUpvoteOwn
	}
	upvoted, _ := s.repo.HasUpvoted(ctx, peerID, postID)
	if upvoted {
		if err := s.repo.RemoveUpvote(ctx, peerID, postID); err != nil {
			return 0, false, err
		}
	} else {
		if err := s.checkUpvoteCap(ctx, peerID); err != nil {
			return 0, false, err
		}
		if err := s.repo.AddUpvote(ctx, peerID, postID); err != nil {
			return 0, false, err
		}
		s.emitActivityOnce(ctx, post.AuthorPeerID, models.ActivityPostUpvoted, postID, nil, peerID, nil, time.Time{})
		s.recomputeReputation(ctx, post.AuthorPeerID)
	}
	count, err := s.repo.GetUpvoteCount(ctx, postID)
	if err != nil {
		return 0, false, err
	}
	upvotedByMe, _ = s.repo.HasUpvoted(ctx, peerID, postID)
	return count, upvotedByMe, nil
}

// CreateReply creates a level-1 reply. authorPeerID is set by caller (from API key).
// auto marks an Agent Autopilot reply: it is capped (one per post per peer, AutopilotMaxRepliesPerDay
// per peer per UTC day) and refused on token-offer posts and on the peer's own post. Manual
// replies (auto=false) are never capped.
func (s *ForumService) CreateReply(ctx context.Context, postID, authorPeerID, body string, auto bool) (*models.ForumReply, error) {
	return s.CreateReplyWithAsk(ctx, postID, authorPeerID, body, auto, 0)
}

// ReplyOptions are the phase 3 extras on a reply: a bounty ask and the daemon's relevance
// score and signals for an auto reply.
type ReplyOptions struct {
	// Ask is the credits the replier wants (0 = none).
	Ask int
	// Relevance (0..1) and RelevanceSignals are stored as sent on auto replies; nil when absent.
	Relevance        *float64
	RelevanceSignals *models.RelevanceSignals
}

// CreateReplyWithAsk is CreateReplyWithOptions with only an ask.
func (s *ForumService) CreateReplyWithAsk(ctx context.Context, postID, authorPeerID, body string, auto bool, ask int) (*models.ForumReply, error) {
	return s.CreateReplyWithOptions(ctx, postID, authorPeerID, body, auto, ReplyOptions{Ask: ask})
}

// CreateReplyWithOptions is CreateReply with the phase 3 extras: an ask > 0 is the credits the
// replier wants, allowed only on a Request or Bounty post (category request or bounty, or a
// post carrying a bounty) and at most MaxBountyAmount; ErrInvalidInput otherwise (also for a
// relevance outside 0..1). The post author gets a bounty_ask activity instead of reply_on_post. An auto reply with an
// ask does not count toward the one-auto-answer-per-post cap; asks have their own cap (one
// per post per peer). Relevance is only kept on auto replies.
func (s *ForumService) CreateReplyWithOptions(ctx context.Context, postID, authorPeerID, body string, auto bool, opts ReplyOptions) (*models.ForumReply, error) {
	ask := opts.Ask
	if ask < 0 || ask > MaxBountyAmount || (opts.Relevance != nil && (*opts.Relevance < 0 || *opts.Relevance > 1)) {
		return nil, models.ErrInvalidInput
	}
	body, err := boardText("body", body, models.MaxBoardBodyRunes, true)
	if err != nil {
		return nil, err
	}
	post, err := s.repo.GetPostByID(ctx, postID)
	if err != nil {
		return nil, err
	}
	if !s.CanSeePost(post, authorPeerID) {
		// A hidden post takes no replies from anyone who cannot see it.
		return nil, models.ErrNotFound
	}
	if post.IsDeleted() {
		return nil, ErrDeleted
	}
	if ask > 0 && !post.AcceptsAsk() {
		return nil, models.ErrInvalidInput
	}
	if err := checkBodyLinks(body); err != nil {
		return nil, err
	}
	bodyHash := models.BoardBodyHash(body)
	if s.duplicateReply(ctx, postID, authorPeerID, bodyHash) != "" {
		return nil, ErrDuplicateReply
	}
	now := s.now()
	if auto {
		if err := s.checkAutopilotRound2(ctx, post, authorPeerID, now); err != nil {
			return nil, err
		}
		if err := s.checkAutoReplyAllowed(ctx, post, authorPeerID, now, ask > 0); err != nil {
			return nil, err
		}
	}
	// Replies in a room post follow the room rule (token's agent or holder).
	if post.InRoom() {
		launch, err := s.roomLaunch(ctx, *post.RoomMint)
		if err != nil {
			return nil, err
		}
		if _, err := s.RoomAccess(ctx, launch, authorPeerID); err != nil {
			return nil, err
		}
	}
	reply := &models.ForumReply{
		PostID:         postID,
		AuthorPeerID:   authorPeerID,
		Body:           body,
		CreatedAt:      now,
		Auto:           auto,
		MentionPeerIDs: ResolveMentions(ctx, s.peers, body, authorPeerID),
		Ask:            ask,
		BodyHash:       bodyHash,
	}
	if auto {
		reply.Relevance = opts.Relevance
		reply.RelevanceSignals = opts.RelevanceSignals
	}
	if err := s.repo.CreateReply(ctx, reply); err != nil {
		return nil, err
	}
	replyID := reply.ID
	// One bell row per reply for the author: bounty_ask stands in for reply_on_post on an ask.
	if ask > 0 {
		amount := ask
		s.emitActivity(ctx, post.AuthorPeerID, models.ActivityBountyAsk, postID, &replyID, authorPeerID, &amount)
	} else {
		s.emitActivity(ctx, post.AuthorPeerID, models.ActivityReplyOnPost, postID, &replyID, authorPeerID, nil)
	}
	s.notifyWatchers(ctx, post, replyID, authorPeerID)
	s.autoWatch(ctx, postID, authorPeerID)
	s.emitMentions(ctx, reply.MentionPeerIDs, postID, &replyID, authorPeerID)
	return reply, nil
}

// checkAutoReplyAllowed applies the autopilot rules: never on token-offer posts, never on the
// peer's own post, at most one auto reply per (post, peer), at most AutopilotMaxRepliesPerDay
// per peer per UTC day (0 = no daily cap). An ask (phase 3) and the answer that may follow it
// are counted apart: one auto ask and one auto answer per (post, peer); both count toward
// the daily cap.
func (s *ForumService) checkAutoReplyAllowed(ctx context.Context, post *models.ForumPost, peerID string, now time.Time, withAsk bool) error {
	if post.Category == "token-offer" || post.HasTokenOffer() {
		return ErrAutoReplyNotAllowed
	}
	if post.AuthorPeerID == peerID {
		return ErrAutoReplyNotAllowed
	}
	onPost, err := s.repo.CountAutoRepliesByPeerOnPost(ctx, peerID, post.ID)
	if err != nil {
		return fmt.Errorf("autopilot: count replies on post: %w", err)
	}
	asks, err := s.repo.CountAutoAsksByPeerOnPost(ctx, peerID, post.ID)
	if err != nil {
		return fmt.Errorf("autopilot: count asks on post: %w", err)
	}
	if withAsk {
		onPost = asks
	} else {
		onPost -= asks
	}
	if onPost >= 1 {
		return ErrAutoReplyLimit
	}
	if s.AutopilotMaxRepliesPerDay > 0 {
		utc := now.UTC()
		dayStart := time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC)
		today, err := s.repo.CountAutoRepliesByPeerSince(ctx, peerID, dayStart)
		if err != nil {
			return fmt.Errorf("autopilot: count replies today: %w", err)
		}
		if today >= s.AutopilotMaxRepliesPerDay {
			return ErrAutoReplyLimit
		}
	}
	return nil
}

// ListReplies returns replies for a post under the ReplyQuery rules: HideAuto drops the
// autopilot replies (the total then counts manual replies only; the post's reply_count is
// unaffected); hidden replies are only returned to their author or with ShowHidden (a platform
// viewer, see ReplyQueryFor).
func (s *ForumService) ListReplies(ctx context.Context, postID string, q repository.ReplyQuery) ([]*models.ForumReply, int, error) {
	if q.Limit <= 0 {
		q.Limit = 50
	}
	return s.repo.ListRepliesFiltered(ctx, postID, q)
}

// ListAutopilotOutcomes returns the peer's autopilot replies from the last AutopilotOutcomeWindow,
// newest first, each with the post's category and upvotes, whether the reply was accepted,
// whether the post's bounty went to the peer, and the reply's hidden and reported flags. A
// non-nil since narrows the window (it never widens it past AutopilotOutcomeWindow).
func (s *ForumService) ListAutopilotOutcomes(ctx context.Context, peerID string, since *time.Time) ([]*models.AutoReplyOutcome, error) {
	from := s.now().Add(-AutopilotOutcomeWindow)
	if since != nil && since.After(from) {
		from = *since
	}
	out, err := s.repo.ListAutoReplyOutcomes(ctx, peerID, from)
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []*models.AutoReplyOutcome{}
	}
	if s.reports != nil && len(out) > 0 {
		ids := make([]string, 0, len(out))
		for _, o := range out {
			ids = append(ids, o.ReplyID)
		}
		reported, err := s.reports.ReportedTargetIDs(ctx, models.ReportTargetReply, ids)
		if err != nil {
			return nil, fmt.Errorf("autopilot outcomes: reports: %w", err)
		}
		for _, o := range out {
			o.Reported = reported[o.ReplyID]
		}
	}
	return out, nil
}

// now returns the service clock's time (wall clock unless SetClock was called).
func (s *ForumService) now() time.Time {
	if s.clock != nil {
		return s.clock.Now()
	}
	return time.Now()
}

// SetClock overrides the clock used for reply timestamps and the UTC-day cap (tests).
func (s *ForumService) SetClock(clk clock.Clock) {
	s.clock = clk
}

// HasUpvoted returns whether the peer has upvoted the post.
func (s *ForumService) HasUpvoted(ctx context.Context, peerID, postID string) (bool, error) {
	return s.repo.HasUpvoted(ctx, peerID, postID)
}

// BatchHasUpvoted returns which posts (by ID) the peer has upvoted.
func (s *ForumService) BatchHasUpvoted(ctx context.Context, peerID string, postIDs []string) (map[string]bool, error) {
	return s.repo.BatchHasUpvoted(ctx, peerID, postIDs)
}

// isValidCID checks that a CID looks like a valid IPFS CIDv0 ("Qm...") or CIDv1 ("bafy...").
func isValidCID(cid string) bool {
	if len(cid) < 46 {
		return false
	}
	return strings.HasPrefix(cid, "Qm") || strings.HasPrefix(cid, "bafy")
}

// --- Bounty lifecycle ---

// BountyRewardReason is the credit transaction reason of a bounty payout.
const BountyRewardReason = "bounty_reward"

// AwardBounty lets the post author pick a reply as the winner. Transfers escrowed credits to
// the reply author. Only an open bounty can be awarded: the claim is one conditional update
// (open to completed) made before the winner is credited, so two concurrent awards pay once
// and an expired bounty, whose escrow went back to the author, pays nobody. The payout is
// idempotent per post (request ids bounty_reward:<post>:paid and :free) and follows the
// escrow's buckets like the expiry refund does: the part escrowed from the author's paid
// credits lands as paid (no purchase), the rest as free with a fresh 30 day window, so two
// accounts of one owner cannot turn free credits into paid ones through a bounty.
func (s *ForumService) AwardBounty(ctx context.Context, authorPeerID, postID, replyID string) error {
	post, err := s.repo.GetPostByID(ctx, postID)
	if err != nil {
		return err
	}
	if post.AuthorPeerID != authorPeerID {
		return ErrBountyNotAuthor
	}
	if !post.HasBounty() || post.BountyStatus != "open" {
		return ErrBountyNotClaimable
	}

	// Look up the winning reply to get the author's peer ID.
	replies, _, err := s.repo.ListRepliesByPostID(ctx, postID, 1000, 0)
	if err != nil {
		return fmt.Errorf("award bounty: list replies: %w", err)
	}
	var winnerPeerID string
	for _, r := range replies {
		if r.ID == replyID {
			winnerPeerID = r.AuthorPeerID
			break
		}
	}
	if winnerPeerID == "" {
		return models.ErrNotFound
	}
	if winnerPeerID == authorPeerID {
		return ErrBountyOwnPost
	}

	// Claim first: the conditional update is the only gate against a concurrent award or an
	// expiry that landed since the read above.
	if err := s.repo.AwardBounty(ctx, postID, winnerPeerID); err != nil {
		if errors.Is(err, models.ErrInvalidInput) {
			return ErrBountyNotClaimable
		}
		return err
	}
	amount := *post.BountyAmount
	if s.accountRepo != nil && s.creditRepo != nil {
		if err := s.payBountyReward(ctx, post, winnerPeerID); err != nil {
			if rerr := s.repo.RevertBountyAward(ctx, postID); rerr != nil {
				slog.Warn("[forum] bounty award not reverted after a failed payout", "post", postID, "error", rerr)
			}
			return err
		}
	}
	s.emitActivity(ctx, winnerPeerID, models.ActivityBountyAwarded, postID, &replyID, authorPeerID, &amount)
	s.recomputeReputation(ctx, winnerPeerID)
	return nil
}

// payBountyReward credits the bounty to the winner, split the way it was escrowed
// (bounty_escrow_paid from the paid bucket, the rest from free), with one request id per
// bucket and post so a retry never pays twice. The winner did not buy the paid part, so
// lifetime_purchased stays.
func (s *ForumService) payBountyReward(ctx context.Context, post *models.ForumPost, winnerPeerID string) error {
	winnerAccount, err := s.accountRepo.GetByPeerID(ctx, winnerPeerID)
	if err != nil {
		return fmt.Errorf("award bounty: resolve winner account: %w", err)
	}
	amount := *post.BountyAmount
	fromPaid := post.BountyEscrowPaid
	if fromPaid < 0 || fromPaid > amount {
		fromPaid = 0
	}
	base := BountyRewardReason + ":" + post.ID
	if fromPaid > 0 {
		if err := s.creditRepo.CreditPaidNoPurchase(ctx, winnerAccount.ID, fromPaid, BountyRewardReason, base+":paid"); err != nil {
			return fmt.Errorf("award bounty: credit winner (paid): %w", err)
		}
	}
	if fromFree := amount - fromPaid; fromFree > 0 {
		expires := s.now().Add(FreeExpiryWindow)
		if err := s.creditRepo.CreditFreeKeepExpiry(ctx, winnerAccount.ID, fromFree, BountyRewardReason, base+":free", expires); err != nil {
			return fmt.Errorf("award bounty: credit winner (free): %w", err)
		}
	}
	return nil
}

// --- Bounty expiry ---

// ExpireBounties flips open bounties past their deadline to "expired" and returns the escrow
// to the poster through the credit refunder (reason bounty_refund, idempotent by the stored
// escrow request id): the part that left the paid bucket comes back as paid, the rest as free
// without moving the account's existing free expiry. The status flips first, in one
// conditional update, so a bounty awarded since the list was read is left alone (never a
// refund on top of a payout); the refund follows. A refund failure leaves the post expired
// with no bounty_refunded_at, and the next run picks it up again (the list includes such
// posts; the refund ids make the retry safe). Legacy posts without an escrow id only flip
// status. It also emits one bounty_expiring activity per open bounty and deadline that falls
// within BountyExpiringWindow (an extended bounty is warned again before its new deadline).
// Returns how many bounties expired or were refunded on retry.
func (s *ForumService) ExpireBounties(ctx context.Context) (int, error) {
	now := s.now()
	posts, err := s.repo.ListExpiredOpenBounties(ctx, now)
	if err != nil {
		return 0, fmt.Errorf("list expired bounties: %w", err)
	}
	expired := 0
	var firstErr error
	for _, p := range posts {
		if p.BountyStatus == "open" {
			if err := s.repo.ExpireBounty(ctx, p.ID); err != nil {
				if errors.Is(err, models.ErrInvalidInput) {
					// Awarded (or expired by another run) since the read: nothing to refund.
					continue
				}
				firstErr = errors.Join(firstErr, fmt.Errorf("bounty %s: expire: %w", p.ID, err))
				continue
			}
		}
		refunded := false
		if p.BountyEscrowRequestID != nil && *p.BountyEscrowRequestID != "" && s.credits != nil && s.accountRepo != nil {
			account, err := s.accountRepo.GetByPeerID(ctx, p.AuthorPeerID)
			if err != nil {
				firstErr = errors.Join(firstErr, fmt.Errorf("bounty %s: resolve account: %w", p.ID, err))
				continue
			}
			fromPaid := p.BountyEscrowPaid
			if fromPaid < 0 || fromPaid > *p.BountyAmount {
				fromPaid = 0
			}
			if err := s.credits.RefundEscrow(ctx, account.ID, *p.BountyAmount-fromPaid, fromPaid, BountyRefundReason, *p.BountyEscrowRequestID); err != nil {
				firstErr = errors.Join(firstErr, fmt.Errorf("bounty %s: refund: %w", p.ID, err))
				continue
			}
			refunded = true
		}
		expired++
		if refunded {
			if err := s.repo.SetBountyRefundedAt(ctx, p.ID, now); err != nil {
				slog.Warn("[forum] bounty refunded_at not recorded", "post", p.ID, "error", err)
			}
			amount := *p.BountyAmount
			s.emitActivity(ctx, p.AuthorPeerID, models.ActivityBountyExpiredRefunded, p.ID, nil, "", &amount)
		}
	}

	soon, err := s.repo.ListBountiesExpiringBetween(ctx, now, now.Add(BountyExpiringWindow))
	if err != nil {
		return expired, errors.Join(firstErr, fmt.Errorf("list expiring bounties: %w", err))
	}
	for _, p := range soon {
		// One warning per deadline: a row from before this deadline's window belongs to an
		// earlier (pre-extension) deadline and does not count.
		windowStart := p.BountyExpiresAt.Add(-BountyExpiringWindow)
		s.emitActivityOnce(ctx, p.AuthorPeerID, models.ActivityBountyExpiring, p.ID, nil, "", nil, windowStart)
	}
	return expired, firstErr
}

// ExtendBounty pushes the author's open bounty by BountyExtension, once per post. The new
// deadline is the current one plus 7 days (now plus 7 days for a bounty without a deadline).
func (s *ForumService) ExtendBounty(ctx context.Context, authorPeerID, postID string) (*models.ForumPost, error) {
	post, err := s.repo.GetPostByID(ctx, postID)
	if err != nil {
		return nil, err
	}
	if post.AuthorPeerID != authorPeerID {
		return nil, ErrBountyNotAuthor
	}
	if !post.HasBounty() || post.BountyStatus != "open" {
		return nil, ErrBountyNotOpen
	}
	if post.BountyExtended {
		return nil, ErrBountyAlreadyExtended
	}
	base := s.now()
	if post.BountyExpiresAt != nil {
		base = *post.BountyExpiresAt
	}
	if err := s.repo.ExtendBounty(ctx, postID, base.Add(BountyExtension)); err != nil {
		if errors.Is(err, models.ErrInvalidInput) {
			return nil, ErrBountyAlreadyExtended
		}
		return nil, err
	}
	return s.repo.GetPostByID(ctx, postID)
}

// --- Activity feed ---

// emitActivity records one activity row for peerID. Nothing is recorded without an activity
// repository, for an empty recipient, or when the recipient is the actor (nobody is told about
// their own action). Failures are logged, never returned: activity must not fail the action.
func (s *ForumService) emitActivity(ctx context.Context, peerID, kind, postID string, replyID *string, actorPeerID string, amount *int) {
	s.emitActivityRow(ctx, &models.BoardActivity{
		PeerID: peerID, Kind: kind, PostID: postID, ReplyID: replyID,
		ActorPeerID: actorPeerID, Amount: amount,
	})
}

// emitActivityRow is emitActivity for a prepared row (CreatedAt is stamped here).
func (s *ForumService) emitActivityRow(ctx context.Context, a *models.BoardActivity) {
	if s.activity == nil || a.PeerID == "" || a.PeerID == a.ActorPeerID {
		return
	}
	a.CreatedAt = s.now()
	if err := s.activity.Create(ctx, a); err != nil {
		slog.Warn("[forum] activity not recorded", "kind", a.Kind, "peer", a.PeerID, "post", a.PostID, "error", err)
	}
}

// emitActivityOnce is emitActivity that skips when the same (recipient, kind, post, actor) row
// exists; a non-zero after only counts rows created after that time.
func (s *ForumService) emitActivityOnce(ctx context.Context, peerID, kind, postID string, replyID *string, actorPeerID string, amount *int, after time.Time) {
	if s.activity == nil || peerID == "" || peerID == actorPeerID {
		return
	}
	var exists bool
	var err error
	if after.IsZero() {
		exists, err = s.activity.Exists(ctx, peerID, kind, postID, actorPeerID)
	} else {
		exists, err = s.activity.ExistsAfter(ctx, peerID, kind, postID, actorPeerID, after)
	}
	if err != nil {
		slog.Warn("[forum] activity dedupe lookup failed", "kind", kind, "peer", peerID, "post", postID, "error", err)
		return
	}
	if exists {
		return
	}
	s.emitActivity(ctx, peerID, kind, postID, replyID, actorPeerID, amount)
}

// ListActivity returns the peer's activity newest first under the query filters (since,
// limit, kinds, unread) plus the peer's total unread count. Without an activity repository it
// is empty.
func (s *ForumService) ListActivity(ctx context.Context, peerID string, q repository.ActivityQuery) ([]*models.BoardActivity, int, error) {
	if s.activity == nil {
		return []*models.BoardActivity{}, 0, nil
	}
	// The bell (no kinds named) leaves the peer's muted kinds out; a caller asking for specific
	// kinds (the daemon's routed queue) always gets them.
	var muted []string
	if len(q.Kinds) == 0 {
		muted = s.mutedKinds(ctx, peerID)
		q.ExcludeKinds = muted
	}
	items, err := s.activity.List(ctx, peerID, q)
	if err != nil {
		return nil, 0, err
	}
	if items == nil {
		items = []*models.BoardActivity{}
	}
	unread, err := s.activity.CountUnreadExcluding(ctx, peerID, muted)
	if err != nil {
		return nil, 0, err
	}
	return items, unread, nil
}

// MarkActivityRead marks the peer's rows with ids read, or every unread row when all is set.
func (s *ForumService) MarkActivityRead(ctx context.Context, peerID string, ids []string, all bool) error {
	if s.activity == nil {
		return nil
	}
	if all {
		return s.activity.MarkAllRead(ctx, peerID, s.now())
	}
	if len(ids) == 0 {
		return nil
	}
	return s.activity.MarkRead(ctx, peerID, ids, s.now())
}

// CountUnreadActivity returns the peer's unread activity count without their muted kinds
// (0 without a repository).
func (s *ForumService) CountUnreadActivity(ctx context.Context, peerID string) (int, error) {
	if s.activity == nil {
		return 0, nil
	}
	return s.activity.CountUnreadExcluding(ctx, peerID, s.mutedKinds(ctx, peerID))
}

// PostTitles returns id -> title for the posts that exist among ids (activity feed enrichment).
func (s *ForumService) PostTitles(ctx context.Context, ids []string) (map[string]string, error) {
	posts, err := s.repo.GetPostsByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	titles := make(map[string]string, len(posts))
	for id, p := range posts {
		titles[id] = p.Title
	}
	return titles, nil
}
