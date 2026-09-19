// Package: tracker/internal/repository
// Feature: F-007 (Centralized Tracker)
// Story: US-007-01 (PostgreSQL Schema and Migrations)
// Purpose: Repository interfaces for tracker data access

package repository

import (
	"context"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/reputation"
)

// ListPeersOptions configures peer listing queries.
type ListPeersOptions struct {
	Limit  int
	Offset int
}

// CountryCount is a single row for online peer counts by country (ISO 3166-1 alpha-2).
type CountryCount struct {
	Country string
	Count   int
}

// PeerRepository defines the data access contract for peers.
type PeerRepository interface {
	Create(ctx context.Context, peer *models.Peer) error
	Upsert(ctx context.Context, peer *models.Peer) error
	FindByID(ctx context.Context, peerID string) (*models.Peer, error)
	List(ctx context.Context, opts ListPeersOptions) ([]*models.Peer, error)
	FindByIDs(ctx context.Context, peerIDs []string) ([]*models.Peer, error)
	Delete(ctx context.Context, peerID string) error
	UpdateTransferStats(ctx context.Context, peerID string, uploadBytes, downloadBytes int64, avgSpeed *int64) error
	ListPeersByUploadBytes(ctx context.Context, limit, offset int, hasAssets bool) ([]*models.Peer, error)
	ListPeersByDownloadBytes(ctx context.Context, limit, offset int, hasDownloads bool) ([]*models.Peer, error)
	Count(ctx context.Context) (int, error)
	// CountOnlineByCountry returns online peer counts grouped by country (last_seen > since). Empty country excluded.
	CountOnlineByCountry(ctx context.Context, since time.Time) ([]CountryCount, error)
	// RecentlyJoined returns peers ordered by first_seen DESC (newest first). (F-027, US-027-06)
	RecentlyJoined(ctx context.Context, limit int) ([]*models.Peer, error)
	// DisplayNamesByIDs returns peer_id -> display_name for the given peers in one query.
	// Peers that are unknown or have no display name are absent from the map.
	DisplayNamesByIDs(ctx context.Context, peerIDs []string) (map[string]string, error)
	// UpdateDisplayName sets the peer's display name; an empty name clears it
	// (unlike Upsert, which keeps the stored name when the new one is empty).
	UpdateDisplayName(ctx context.Context, peerID, displayName string) error
	// PeerIDsByDisplayNames returns lowercased display name -> peer_id for the names in the
	// list (matched case-insensitively) in one query. Unknown names are absent.
	PeerIDsByDisplayNames(ctx context.Context, lowerNames []string) (map[string]string, error)
	// PeerIDsSeenSince returns the peers with last_seen at or after since, most recent first,
	// at most limit (request routing candidates).
	PeerIDsSeenSince(ctx context.Context, since time.Time, limit int) ([]string, error)
	// SearchDisplayNames returns peers whose display name starts with prefix (case-insensitive),
	// shortest name first then alphabetical, at most limit.
	SearchDisplayNames(ctx context.Context, prefix string, limit int) ([]DisplayNameMatch, error)
}

// DisplayNameMatch is one autocomplete hit of PeerRepository.SearchDisplayNames.
type DisplayNameMatch struct {
	PeerID      string
	DisplayName string
}

// PeerAPIKeyRepository manages API keys for peers (issued on first register, used for forum mutations).
type PeerAPIKeyRepository interface {
	// Create generates and stores an API key for the peer; returns the key. Call only on first register.
	Create(ctx context.Context, peerID string) (apiKey string, err error)
	// GetByAPIKey returns the peer_id for the given API key, or ErrNotFound.
	GetByAPIKey(ctx context.Context, apiKey string) (peerID string, err error)
	// GetByPeerID returns the api_key for the given peer, or ErrNotFound. Used so daemon can re-receive key on re-register.
	GetByPeerID(ctx context.Context, peerID string) (apiKey string, err error)
	// ExistsForPeer returns true if the peer already has an API key.
	ExistsForPeer(ctx context.Context, peerID string) (bool, error)
}

// Keys are created with default credit; completions endpoint can validate and deduct later.
type GuestKeyRepository interface {
	// CreateGuestKey creates a new guest API key with default credit and returns it.
	CreateGuestKey(ctx context.Context, defaultCredits int) (apiKey string, err error)
}

// GuestKeyMappingRepository maps guest API keys to account IDs (unified credit ledger).
type GuestKeyMappingRepository interface {
	// Create stores api_key -> account_id for Agent completions auth.
	Create(ctx context.Context, apiKey, accountID string) error
	// GetAccountIDByAPIKey returns the account_id for the guest key, or ErrNotFound.
	GetAccountIDByAPIKey(ctx context.Context, apiKey string) (accountID string, err error)
}

// SearchAssetsOptions configures asset search queries.
type SearchAssetsOptions struct {
	Query        string   // keyword search on filename
	MimeType     string   // filter by MIME type
	ManifestType string   // "raw" or "vec"
	PeerIDs      []string // restrict to these peer IDs (for online-only)
	Limit        int
	Offset       int
}

// TrendingRow holds an asset and its download count within a time window (e.g. last 24h).
type TrendingRow struct {
	Asset         *models.Asset
	CountInWindow int
}

// DownloadEventRecord represents a download completion for activity feeds (F-027).
type DownloadEventRecord struct {
	CID      string
	Filename string
	At       time.Time
}

// AssetRepository defines the data access contract for assets.
type AssetRepository interface {
	Create(ctx context.Context, asset *models.Asset) error
	FindByCID(ctx context.Context, cid string) (*models.Asset, error)
	Search(ctx context.Context, opts SearchAssetsOptions) ([]*models.Asset, int, error)
	Delete(ctx context.Context, cid string) error
	SetQuarantined(ctx context.Context, cid string, quarantined bool) error
	IncrementDownloadCount(ctx context.Context, cid string) error
	// RecordDownloadEvent records a download completion for time-windowed trending (e.g. last 24h).
	RecordDownloadEvent(ctx context.Context, cid string) error
	ListTrending(ctx context.Context, limit, offset int) ([]*models.Asset, int, error)
	// ListTrendingSince returns assets with most downloads in the window [since, now], ordered by count desc. CountInWindow is the download count in that window.
	ListTrendingSince(ctx context.Context, since time.Time, limit, offset int) ([]TrendingRow, int, error)
	Count(ctx context.Context) (int, error)
	CountByPeerID(ctx context.Context, peerID string) (int, error)
	// CountByPeerIDs returns non-quarantined asset counts for each peer ID. Peers with 0 assets are omitted. (F-032, US-032-01)
	CountByPeerIDs(ctx context.Context, peerIDs []string) (map[string]int, error)
	// PeerIDsWithAssets returns peer IDs that have at least one non-quarantined asset (batch check).
	PeerIDsWithAssets(ctx context.Context, peerIDs []string) ([]string, error)
	// TopByDownloads returns the top N non-quarantined assets for a peer, ordered by download_count DESC. (F-032, US-032-01)
	TopByDownloads(ctx context.Context, peerID string, limit int) ([]*models.Asset, error)
	// RecentAnnouncements returns assets ordered by announced_at DESC (recent shares). (F-027, US-027-06)
	RecentAnnouncements(ctx context.Context, limit int) ([]*models.Asset, error)
	// RecentDownloadEvents returns recent download events with asset info. (F-027, US-027-06)
	RecentDownloadEvents(ctx context.Context, limit int) ([]DownloadEventRecord, error)
}

// PeerChunkAvailability represents per-peer chunk availability for a CID.
type PeerChunkAvailability struct {
	PeerID string
	Chunks []int
}

// AvailabilityRepository defines the data access contract for per-peer chunk availability.
type AvailabilityRepository interface {
	UpsertPeerChunks(ctx context.Context, cid string, peerID string, chunks []int) error
	GetPeerChunks(ctx context.Context, cid string) ([]PeerChunkAvailability, error)
}

// Post feed sorts for PostQuery.Sort.
const (
	PostSortRecent   = "recent"   // created_at desc
	PostSortTop      = "top"      // upvote_count desc, created_at desc
	PostSortBounties = "bounties" // open bounties only: bounty_amount desc, bounty_expires_at asc
)

// PostQuery.Mine scopes the feed to the viewer.
const (
	MinePosts    = "posts"    // posts I wrote
	MineReplies  = "replies"  // posts I replied in
	MineBounties = "bounties" // bounty posts I wrote or won
)

// PostQuery is a filtered, sorted page of the board feed. Zero values mean "no filter".
type PostQuery struct {
	Limit  int
	Offset int
	Sort   string // PostSortRecent (default), PostSortTop, PostSortBounties
	// Category filters on the stored category (general, request, bounty, token-offer, discovery).
	Category string
	// Search matches title and body (Postgres full text, memory substring).
	Search string
	// Mine is one of MinePosts, MineReplies, MineBounties and needs MinePeerID.
	Mine       string
	MinePeerID string
	// Now is the reference time for "open bounty" (expiry not passed); zero = time.Now().
	Now time.Time
	// Viewer is the requesting peer ("" = anonymous): hidden posts are left out unless the
	// viewer is their author or ShowHidden is set (platform peers).
	Viewer     string
	ShowHidden bool
	// Room scopes the feed to one token room (posts with that room_mint, room_pinned first).
	// Empty means the main feed, which leaves room posts out unless Mine is set (a poster's
	// Mine views include their room posts).
	Room string
	// HideAuto drops autopilot posts (auto = true), like ReplyQuery.HideAuto does for replies.
	HideAuto bool
	// Author scopes the feed to that peer's posts (the public agent activity view); like Mine,
	// room posts are included. Hidden posts follow the Viewer / ShowHidden rule above.
	Author string
	// Participant scopes the feed to the posts that peer replied in (room posts included).
	// Hidden replies only count for the participant themselves or with ShowHidden.
	Participant string
	// Before is the keyset cursor of the recent sort (round 2): only posts older than it (by
	// created_at, then id) are returned, so a page never repeats or skips a row when posts land
	// between two page reads. Ignored on the other sorts and when nil. Offset still applies to
	// the rows after the cursor (normally 0).
	Before *PostCursor
}

// PostCursor is where a keyset page ends: the last row's created_at and id.
type PostCursor struct {
	CreatedAt time.Time
	ID        string
}

// BoardStatsGuard tunes which events the reputation counters accept (round 2, sock puppets).
// Zero values mean every event counts.
type BoardStatsGuard struct {
	// VoterSince: an upvote or an accepted answer only counts when the peer who gave it has a
	// peer row older than this time (first_seen before it) and has posted or replied at least
	// once. A linked wallet shared with the beneficiary never counts either.
	VoterSince time.Time
}

// ReplyQuery is a page of a post's replies with the phase 1 visibility rules.
type ReplyQuery struct {
	Limit  int
	Offset int
	// HideAuto drops autopilot replies.
	HideAuto bool
	// Viewer ("" = anonymous) sees their own hidden replies; ShowHidden sees every hidden reply.
	Viewer     string
	ShowHidden bool
}

// BoardStats are the per-peer counters the board reputation score is computed from, read
// straight from the forum tables (so a full recompute is the source of truth).
type BoardStats struct {
	// BountiesWon and CreditsWon: completed bounties awarded to the peer and their total amount.
	BountiesWon int
	CreditsWon  int
	// AnswersAccepted: the peer's replies that are the accepted answer of their post.
	AnswersAccepted int
	// UpvotesReceived: current upvotes across the peer's posts (net of removed upvotes).
	UpvotesReceived int
	// FirstReplies1h: posts by others where the peer's manual reply was the first manual reply
	// and came within an hour of the post (autopilot replies never count).
	FirstReplies1h int
}

// PostCounts are the cheap board aggregates for GET /api/board/counts.
type PostCounts struct {
	All        int
	ByCategory map[string]int
	// OpenBounties counts bounty posts with status open whose expiry has not passed.
	OpenBounties int
}

// BoardActivity is a peer's public board footprint behind GET /api/peers/{id}/board-summary:
// visible (not hidden) posts and replies, main feed and rooms alike.
type BoardActivity struct {
	Posts   int
	Replies int
	// LastActiveAt is the newest visible post or reply by the peer (nil when there is none).
	LastActiveAt *time.Time
}

// RoomPostStats are the per-room feed counters behind GET /api/board/rooms.
type RoomPostStats struct {
	// PostsSince counts the room's visible posts created at or after the since time given.
	PostsSince int
	// LastPostAt is the newest visible post in the room (nil when the room has none).
	LastPostAt *time.Time
}

// RoomDigestThread is one of the top threads of a room digest.
type RoomDigestThread struct {
	ID      string
	Title   string
	Upvotes int
	Replies int
}

// RoomDigestStats are the counters of GET /api/board/rooms/{mint}/digest for one period.
type RoomDigestStats struct {
	// Posts and Replies created in the room during the period (hidden content left out).
	Posts   int
	Replies int
	// BountiesAwarded and CreditsAwarded: room bounties completed during the period and their sum.
	BountiesAwarded int
	CreditsAwarded  int
	// TopThreads are the room's posts created in the period by upvotes then replies (max 5).
	TopThreads []RoomDigestThread
}

// ForumRepository defines the data access contract for forum posts, replies, and upvotes.
type ForumRepository interface {
	// Posts
	CreatePost(ctx context.Context, post *models.ForumPost) error
	GetPostByID(ctx context.Context, postID string) (*models.ForumPost, error)
	ListPosts(ctx context.Context, limit, offset int, sortNewest bool) ([]*models.ForumPost, int, error)
	// QueryPosts is ListPosts with the board filters (category, search, mine, bounties sort).
	QueryPosts(ctx context.Context, q PostQuery) ([]*models.ForumPost, int, error)
	// GetPostsByIDs returns the posts that exist among ids, keyed by id.
	GetPostsByIDs(ctx context.Context, ids []string) (map[string]*models.ForumPost, error)
	// CountPosts returns the feed aggregates; now is the reference time for open bounties.
	// roomMint scopes the counts to that room; empty counts the main feed (room posts left out).
	CountPosts(ctx context.Context, now time.Time, roomMint string) (*PostCounts, error)
	// RoomStats returns, per room mint, the visible post count since the given time and the
	// newest post time (rooms without posts are absent).
	RoomStats(ctx context.Context, mints []string, since time.Time) (map[string]*RoomPostStats, error)
	// GetRoomAnnouncement returns the room's pinned announcement post, or ErrNotFound.
	GetRoomAnnouncement(ctx context.Context, roomMint string) (*models.ForumPost, error)
	// SetRoutedTo stores the peers a post was routed to.
	SetRoutedTo(ctx context.Context, postID string, peerIDs []string) error
	// ListPostsByAuthor returns the author's posts newest first, at most limit.
	ListPostsByAuthor(ctx context.Context, authorPeerID string, limit int) ([]*models.ForumPost, error)
	// PeersWithAcceptedReplySince returns the authors of accepted replies created at or after since.
	PeersWithAcceptedReplySince(ctx context.Context, since time.Time) (map[string]bool, error)
	// RoomDigest aggregates the room's activity between from and to (both inclusive).
	RoomDigest(ctx context.Context, roomMint string, from, to time.Time) (*RoomDigestStats, error)
	// CountRoutedPostsByAuthorSince counts the author's posts created at or after since that
	// were routed to at least one peer (the per-poster routing cap).
	CountRoutedPostsByAuthorSince(ctx context.Context, authorPeerID string, since time.Time) (int, error)
	// BoardActivity counts the peer's visible posts and replies and their newest time (the
	// agent activity view).
	BoardActivity(ctx context.Context, peerID string) (*BoardActivity, error)
	// Replies (flat, level-1 only)
	CreateReply(ctx context.Context, reply *models.ForumReply) error
	ListRepliesByPostID(ctx context.Context, postID string, limit, offset int) ([]*models.ForumReply, int, error)
	// ListManualRepliesByPostID is ListRepliesByPostID without the auto (autopilot) replies;
	// the returned total counts the manual replies only.
	ListManualRepliesByPostID(ctx context.Context, postID string, limit, offset int) ([]*models.ForumReply, int, error)
	// Autopilot caps and outcomes (auto replies only; manual replies never count).
	CountAutoRepliesByPeerOnPost(ctx context.Context, peerID, postID string) (int, error)
	CountAutoRepliesByPeerSince(ctx context.Context, peerID string, since time.Time) (int, error)
	// ListAutoReplyOutcomes returns the peer's auto replies created at or after since, newest
	// first, each joined with its post's category, upvotes, accepted answer and bounty state
	// and the reply's hidden flag (Reported is left to the caller).
	ListAutoReplyOutcomes(ctx context.Context, peerID string, since time.Time) ([]*models.AutoReplyOutcome, error)
	// CountAutoAsksByPeerOnPost counts the peer's auto replies on the post that carry an ask
	// (phase 3; a subset of CountAutoRepliesByPeerOnPost).
	CountAutoAsksByPeerOnPost(ctx context.Context, peerID, postID string) (int, error)
	// AskerPeerIDs returns the distinct authors of the post's replies that carry an ask.
	AskerPeerIDs(ctx context.Context, postID string) ([]string, error)
	// RaiseBounty claims the raise: one atomic conditional update that sets the post's bounty to
	// amount (open, currency credits) only while the post has no bounty or an open bounty below
	// amount, and returns the previous amount (0 for none). escrowRequestID and expiresAt only
	// apply to a post that had no bounty (an existing escrow id and deadline are kept).
	// ErrInvalidInput when no row qualified (bounty not open, or amount not above the current
	// one, including a concurrent raise that got there first); nothing is written then.
	RaiseBounty(ctx context.Context, postID string, amount int, escrowRequestID string, expiresAt time.Time) (prevAmount int, err error)
	// SetBountyEscrowPaid records the paid part of the escrow after a successful raise.
	SetBountyEscrowPaid(ctx context.Context, postID string, escrowPaid int) error
	// RevertBountyRaise undoes RaiseBounty when the escrow failed: the bounty goes back to
	// prevAmount, or is cleared entirely when prevAmount is 0 (the post had none).
	RevertBountyRaise(ctx context.Context, postID string, prevAmount int) error
	// Upvotes
	AddUpvote(ctx context.Context, peerID, postID string) error
	RemoveUpvote(ctx context.Context, peerID, postID string) error
	GetUpvoteCount(ctx context.Context, postID string) (int, error)
	HasUpvoted(ctx context.Context, peerID, postID string) (bool, error)
	// BatchHasUpvoted returns the set of postIDs (from the given slice) that peerID has upvoted.
	BatchHasUpvoted(ctx context.Context, peerID string, postIDs []string) (map[string]bool, error)
	// Reply count for a post (for list/single post)
	CountRepliesByPostID(ctx context.Context, postID string) (int, error)
	// View count — atomically increment and return new count
	IncrementViewCount(ctx context.Context, postID string) (int, error)
	// Bounty lifecycle
	// AwardBounty claims the award: only an open bounty flips to completed (ErrInvalidInput
	// otherwise), so concurrent awards and an award of an expired bounty fail closed.
	AwardBounty(ctx context.Context, postID, winnerPeerID string) error
	// RevertBountyAward reopens a completed bounty whose winner could not be credited.
	RevertBountyAward(ctx context.Context, postID string) error
	SetBountyEscrowRequestID(ctx context.Context, postID, requestID string) error
	// ListExpiredOpenBounties returns open bounties past their deadline and expired escrowed
	// bounties whose refund was never recorded.
	ListExpiredOpenBounties(ctx context.Context, now time.Time) ([]*models.ForumPost, error)
	// ExpireBounty flips an open bounty to expired; ErrInvalidInput when it is not open.
	ExpireBounty(ctx context.Context, postID string) error
	// SetBountyRefundedAt records when the expired bounty's escrow went back to the poster.
	SetBountyRefundedAt(ctx context.Context, postID string, at time.Time) error
	// ExtendBounty moves bounty_expires_at to expiresAt and marks the post extended. Returns
	// ErrInvalidInput when the bounty is not open or was already extended.
	ExtendBounty(ctx context.Context, postID string, expiresAt time.Time) error
	// ListBountiesExpiringBetween returns open bounties with from < bounty_expires_at <= to.
	ListBountiesExpiringBetween(ctx context.Context, from, to time.Time) ([]*models.ForumPost, error)

	// --- Phase 1: accepted answer, moderation, token offers, reputation counters ---

	// GetReplyByID returns one reply, or ErrNotFound.
	GetReplyByID(ctx context.Context, replyID string) (*models.ForumReply, error)
	// ListRepliesFiltered is ListRepliesByPostID with the visibility rules of ReplyQuery.
	ListRepliesFiltered(ctx context.Context, postID string, q ReplyQuery) ([]*models.ForumReply, int, error)
	// SetAcceptedReply records the accepted answer on a post (nil clears it).
	SetAcceptedReply(ctx context.Context, postID string, replyID *string) error
	// SetPostHidden / SetReplyHidden flip the hidden flag.
	SetPostHidden(ctx context.Context, postID string, hidden bool) error
	SetReplyHidden(ctx context.Context, replyID string, hidden bool) error
	// PinPost pins postID and unpins every other post; UnpinPost clears the flag.
	PinPost(ctx context.Context, postID string) error
	UnpinPost(ctx context.Context, postID string) error
	// IncrementTokenOfferPaid atomically bumps token_offer_paid while it is below token_offer_max
	// and returns the new count; ErrInvalidInput when the offer is exhausted (paid can never
	// exceed max, whatever the concurrency). DecrementTokenOfferPaid gives a slot back when the
	// payment could not be recorded after all.
	IncrementTokenOfferPaid(ctx context.Context, postID string) (int, error)
	DecrementTokenOfferPaid(ctx context.Context, postID string) error
	// BoardStats returns the reputation counters for one peer under the guard.
	BoardStats(ctx context.Context, peerID string, guard BoardStatsGuard) (*BoardStats, error)
	// BoardPeerIDs returns every peer that authored a post or a reply (nightly recompute).
	BoardPeerIDs(ctx context.Context) ([]string, error)

	// --- Round 2: edits, soft delete, duplicates, caps, disputes, visits ---

	// UpdatePostBody replaces the title and body of a post and stamps edited_at / edit_count.
	UpdatePostBody(ctx context.Context, postID, title, body, bodyHash string, at time.Time) error
	// UpdateReplyBody replaces the body of a reply and stamps edited_at / edit_count.
	UpdateReplyBody(ctx context.Context, replyID, body, bodyHash string, at time.Time) error
	// SoftDeletePost / SoftDeleteReply set deleted_at (the row stays; ErrNotFound when missing,
	// ErrInvalidInput when already deleted).
	SoftDeletePost(ctx context.Context, postID string, at time.Time) error
	SoftDeleteReply(ctx context.Context, replyID string, at time.Time) error
	// FindRecentPostByBodyHash returns the id of the author's newest post with that body hash
	// created at or after since (deleted ones excluded), or ErrNotFound.
	FindRecentPostByBodyHash(ctx context.Context, authorPeerID, bodyHash string, since time.Time) (string, error)
	// FindRecentReplyByBodyHash is the same for the author's replies on postID.
	FindRecentReplyByBodyHash(ctx context.Context, postID, authorPeerID, bodyHash string, since time.Time) (string, error)
	// CountUpvotesByPeerSince counts the upvotes peerID gave at or after since (the daily cap).
	CountUpvotesByPeerSince(ctx context.Context, peerID string, since time.Time) (int, error)
	// CountAutoRepliesByPeerInRoomSince counts the peer's auto replies on posts of that room
	// created at or after since (the per-room autopilot cap).
	CountAutoRepliesByPeerInRoomSince(ctx context.Context, peerID, roomMint string, since time.Time) (int, error)
	// SetRoutedReasons stores why each routed peer was picked.
	SetRoutedReasons(ctx context.Context, postID string, reasons map[string][]string) error
	// OpenBountyDispute records a dispute on a completed or expired bounty (one open dispute at
	// a time; ErrInvalidInput otherwise).
	OpenBountyDispute(ctx context.Context, postID, byPeerID, note string, at time.Time) error
	// ResolveBountyDispute closes the open dispute with status upheld or dismissed
	// (ErrInvalidInput when there is no open dispute).
	ResolveBountyDispute(ctx context.Context, postID, status string, at time.Time) error
	// ListDisputedBounties returns the posts with an open dispute, oldest first, at most limit.
	ListDisputedBounties(ctx context.Context, limit int) ([]*models.ForumPost, error)
	// CountPostsSince counts the visible posts of a scope ("" = the main feed, else a room
	// mint) created after since (exclusive), for unread counts per room.
	CountPostsSince(ctx context.Context, scope string, since time.Time) (int, error)
}

// BoardEditHistoryRepository keeps what a post or reply said before each edit (board_edit_history).
type BoardEditHistoryRepository interface {
	// Add inserts one row; a missing ID is generated.
	Add(ctx context.Context, e *models.BoardEdit) error
	// List returns the target's edits newest first, at most limit.
	List(ctx context.Context, targetType, targetID string, limit int) ([]*models.BoardEdit, error)
}

// BoardRoomRepository stores the room controls of the token's agent (board_room_settings,
// board_room_mutes).
type BoardRoomRepository interface {
	// GetSettings returns the room's settings, the defaults when it has no row.
	GetSettings(ctx context.Context, mint string) (*models.RoomSettings, error)
	// SetSettings upserts the room's settings.
	SetSettings(ctx context.Context, s *models.RoomSettings) error
	// SettingsByMints returns the stored rows among mints (rooms without a row are absent).
	SettingsByMints(ctx context.Context, mints []string) (map[string]*models.RoomSettings, error)
	// Mute upserts a mute; Unmute removes it (ErrNotFound when there was none).
	Mute(ctx context.Context, m *models.RoomMute) error
	Unmute(ctx context.Context, mint, peerID string) error
	// IsMuted reports whether the peer is muted in the room.
	IsMuted(ctx context.Context, mint, peerID string) (bool, error)
	// ListMutes returns the room's mutes newest first.
	ListMutes(ctx context.Context, mint string) ([]*models.RoomMute, error)
}

// BoardVisitRepository records when a peer last opened a feed scope (board_visits).
type BoardVisitRepository interface {
	// Visit records a visit at the given time and returns the previous visit (nil for the first).
	Visit(ctx context.Context, peerID, scope string, at time.Time) (*time.Time, error)
	// LastVisits returns scope -> last visit for the scopes the peer has visited.
	LastVisits(ctx context.Context, peerID string, scopes []string) (map[string]time.Time, error)
}

// BoardNotificationPrefRepository stores which activity kinds a peer muted (board_notification_prefs).
type BoardNotificationPrefRepository interface {
	// Get returns the peer's preferences (no muted kinds when there is no row).
	Get(ctx context.Context, peerID string) (*models.NotificationPrefs, error)
	// Set upserts the peer's preferences.
	Set(ctx context.Context, p *models.NotificationPrefs) error
}

// BoardReputationRepository stores the board reputation per peer (table peer_reputation).
type BoardReputationRepository interface {
	// Upsert replaces the peer's row.
	Upsert(ctx context.Context, rep *models.PeerReputation) error
	// Get returns the peer's row, or ErrNotFound.
	Get(ctx context.Context, peerID string) (*models.PeerReputation, error)
	// GetByIDs returns the rows that exist among peerIDs, keyed by peer id (one query).
	GetByIDs(ctx context.Context, peerIDs []string) (map[string]*models.PeerReputation, error)
}

// BoardReportRepository stores reports on posts and replies (table board_reports).
type BoardReportRepository interface {
	// Create inserts a report; ErrAlreadyExists when the reporter already reported the target.
	Create(ctx context.Context, report *models.BoardReport) error
	// Get returns one report, or ErrNotFound.
	Get(ctx context.Context, id string) (*models.BoardReport, error)
	// List returns reports with the given status, newest first, at most limit.
	List(ctx context.Context, status string, limit int) ([]*models.BoardReport, error)
	// ReporterPeerIDs returns the distinct reporters of a target whose report was not dismissed.
	ReporterPeerIDs(ctx context.Context, targetType, targetID string) ([]string, error)
	// SetStatus resolves a report (upheld or dismissed) at the given time; a platform decision, so the
	// resolution note is cleared.
	SetStatus(ctx context.Context, id, status string, at time.Time) error
	// ResolveOpenForTarget resolves every open report on the target with the given status
	// and resolution note (auto hide); returns how many rows changed.
	ResolveOpenForTarget(ctx context.Context, targetType, targetID, status, note string, at time.Time) (int, error)
	// CountUpheldAgainst returns how many reports a platform peer upheld against content by authorPeerID
	// (auto-upheld ones, resolution note set, never count).
	CountUpheldAgainst(ctx context.Context, authorPeerID string) (int, error)
	// ReportedTargetIDs returns the subset of targetIDs (of targetType) that have an open or
	// upheld report.
	ReportedTargetIDs(ctx context.Context, targetType string, targetIDs []string) (map[string]bool, error)
}

// BoardWatchRepository stores who watches which thread (table board_watches).
type BoardWatchRepository interface {
	// Set records an explicit watch or unwatch.
	Set(ctx context.Context, postID, peerID string, watching bool, at time.Time) error
	// AddIfAbsent records an automatic watch (author, replier) unless the peer already has a
	// row, so an explicit unwatch is never undone.
	AddIfAbsent(ctx context.Context, postID, peerID string, at time.Time) error
	// IsWatching reports whether peerID watches postID.
	IsWatching(ctx context.Context, postID, peerID string) (bool, error)
	// Watchers returns the peers watching postID.
	Watchers(ctx context.Context, postID string) ([]string, error)
	// WatchingByPostIDs returns the subset of postIDs that peerID watches.
	WatchingByPostIDs(ctx context.Context, peerID string, postIDs []string) (map[string]bool, error)
}

// TokenOfferPaymentRepository stores verified token offer payments (table token_offer_payments).
type TokenOfferPaymentRepository interface {
	// Create inserts a payment; ErrAlreadyExists when the signature or the reply was already paid.
	Create(ctx context.Context, payment *models.TokenOfferPayment) error
	// HasSignature reports whether the transaction signature was already used.
	HasSignature(ctx context.Context, signature string) (bool, error)
	// PaidReplyIDs returns the set of reply ids of postID that were paid.
	PaidReplyIDs(ctx context.Context, postID string) (map[string]bool, error)
}

// ActivityQuery is a page of a peer's activity feed. Zero values mean "no filter".
type ActivityQuery struct {
	// Since keeps rows created after it (exclusive) when non-nil.
	Since *time.Time
	Limit int
	// Kinds keeps rows of these kinds only (empty = every kind).
	Kinds []string
	// Unread keeps rows without read_at.
	Unread bool
	// ExcludeKinds drops rows of these kinds (the peer's muted notification kinds).
	ExcludeKinds []string
}

// PeerAutopilotRepository stores the autopilot categories each daemon reports (table
// peer_autopilot, migration 025).
type PeerAutopilotRepository interface {
	// Set replaces the peer's categories (an empty list is stored as such).
	Set(ctx context.Context, peerID string, categories []string, at time.Time) error
	// Clear removes the peer's row (the heartbeat carried no autopilot_categories).
	Clear(ctx context.Context, peerID string) error
	// CategoriesByIDs returns peer id -> categories for the peers that have a row.
	CategoriesByIDs(ctx context.Context, peerIDs []string) (map[string][]string, error)
}

// RoomHolderSnapshotRepository stores daily holder counts per room (table room_holder_snapshots).
type RoomHolderSnapshotRepository interface {
	// Record upserts the room's holder count for the UTC day of takenOn.
	Record(ctx context.Context, mint string, takenOn time.Time, holders int) error
	// LatestAtOrBefore returns the newest snapshot taken on or before the UTC day of at, or ErrNotFound.
	LatestAtOrBefore(ctx context.Context, mint string, at time.Time) (*models.RoomHolderSnapshot, error)
}

// BoardActivityRepository stores the per-peer board activity feed (notifications).
type BoardActivityRepository interface {
	// Create inserts one row; a missing ID is generated.
	Create(ctx context.Context, a *models.BoardActivity) error
	// Exists reports whether peerID already has a row of kind for postID from actorPeerID.
	Exists(ctx context.Context, peerID, kind, postID, actorPeerID string) (bool, error)
	// ExistsAfter is Exists restricted to rows created at or after the given time.
	ExistsAfter(ctx context.Context, peerID, kind, postID, actorPeerID string, after time.Time) (bool, error)
	// List returns peerID's rows newest first under the ActivityQuery filters.
	List(ctx context.Context, peerID string, q ActivityQuery) ([]*models.BoardActivity, error)
	// CountUnread returns how many of peerID's rows have no read_at.
	CountUnread(ctx context.Context, peerID string) (int, error)
	// CountUnreadExcluding is CountUnread without rows of the given kinds (muted kinds).
	CountUnreadExcluding(ctx context.Context, peerID string, kinds []string) (int, error)
	// MarkRead sets read_at on peerID's rows with the given ids (rows already read are left alone).
	MarkRead(ctx context.Context, peerID string, ids []string, at time.Time) error
	// MarkAllRead sets read_at on all of peerID's unread rows.
	MarkAllRead(ctx context.Context, peerID string, at time.Time) error
}

// PeerTrustBlockRepository defines the data access contract for peer trust and block.
type PeerTrustBlockRepository interface {
	// Trust adds actor->target trust. Idempotent.
	Trust(ctx context.Context, actorPeerID, targetPeerID string) error
	// Untrust removes actor->target trust.
	Untrust(ctx context.Context, actorPeerID, targetPeerID string) error
	// Block adds actor->target block. Idempotent.
	Block(ctx context.Context, actorPeerID, targetPeerID string) error
	// Unblock removes actor->target block.
	Unblock(ctx context.Context, actorPeerID, targetPeerID string) error
	// IsTrusted returns true if actor has trusted target.
	IsTrusted(ctx context.Context, actorPeerID, targetPeerID string) (bool, error)
	// IsBlocked returns true if actor has blocked target.
	IsBlocked(ctx context.Context, actorPeerID, targetPeerID string) (bool, error)
	// TrustedByActor returns peer IDs that actor has trusted.
	TrustedByActor(ctx context.Context, actorPeerID string) ([]string, error)
	// BlockedByActor returns peer IDs that actor has blocked.
	BlockedByActor(ctx context.Context, actorPeerID string) ([]string, error)
	// CountTrustsReceived returns how many distinct actors have trusted the target peer. (F-032, US-032-02)
	CountTrustsReceived(ctx context.Context, targetPeerID string) (int, error)
}

// DMCARepository defines the data access contract for DMCA notices.
type DMCARepository interface {
	Create(ctx context.Context, notice *models.DMCANotice) error
	FindByCID(ctx context.Context, cid string) ([]*models.DMCANotice, error)
	FindByID(ctx context.Context, id string) (*models.DMCANotice, error)
	List(ctx context.Context) ([]*models.DMCANotice, error)
	UpdateStatus(ctx context.Context, id string, status string) error
}

// --- F-031: Token Identity repositories ---

// TokenRepository defines the data access contract for peer token identity.
type TokenRepository interface {
	// Create persists a new peer token. Returns ErrAlreadyExists if peer_id already has a token.
	Create(ctx context.Context, token *models.PeerToken) error
	// GetByPeerID returns the token for a peer, or ErrNotFound.
	GetByPeerID(ctx context.Context, peerID string) (*models.PeerToken, error)
	// GetByContractAddress returns the token with this contract address, or ErrNotFound.
	GetByContractAddress(ctx context.Context, contractAddr string) (*models.PeerToken, error)
	// UpdateImageURL sets the token_image_url for a peer's token.
	UpdateImageURL(ctx context.Context, peerID, imageURL string) error
	// List returns peer tokens ordered by launched_at DESC with pagination.
	// Returns tokens slice + total count.
	List(ctx context.Context, limit, offset int) ([]*models.PeerToken, int, error)
	// Upsert inserts the token or replaces the peer's existing row (used when a LaunchLab launch is claimed).
	// Returns ErrAlreadyExists if the contract address belongs to a different peer.
	Upsert(ctx context.Context, token *models.PeerToken) error
}

// --- F-013: Credits & Identity repositories ---

// AccountRepository manages account lifecycle.
type AccountRepository interface {
	// Create inserts a new account. Returns ErrAlreadyExists if peer_id is taken.
	Create(ctx context.Context, account *models.Account) error
	// GetByID returns the account by UUID, or ErrNotFound.
	GetByID(ctx context.Context, id string) (*models.Account, error)
	// GetByPeerID returns the account for a peer, or ErrNotFound.
	GetByPeerID(ctx context.Context, peerID string) (*models.Account, error)
	// GetOrCreateForPeer returns the account for the peer, creating it if needed. Second return is true if newly created.
	GetOrCreateForPeer(ctx context.Context, peerID string) (*models.Account, bool, error)
	// UpdateStatus sets the account status (active, recovered, suspended).
	UpdateStatus(ctx context.Context, id string, status string) error
}

// CreditRepository manages credit balances and transactions.
type CreditRepository interface {
	// CreateBalance initialises a zero-balance row for the account.
	CreateBalance(ctx context.Context, balance *models.CreditBalance) error
	// GetBalance returns the current balance, or ErrNotFound.
	GetBalance(ctx context.Context, accountID string) (*models.CreditBalance, error)
	// CreditFree adds free credits and resets the expiry clock.
	CreditFree(ctx context.Context, accountID string, amount int, reason, requestID string, expiresAt time.Time) error
	// CreditPaid adds paid credits and counts them as purchased (lifetime_purchased).
	CreditPaid(ctx context.Context, accountID string, amount int, reason, requestID string) error
	// CreditPaidNoPurchase adds paid credits that were not bought (a refund, a bounty reward):
	// same bucket, lifetime_purchased untouched.
	CreditPaidNoPurchase(ctx context.Context, accountID string, amount int, reason, requestID string) error
	// Spend atomically deducts credits (free first, then paid). Returns ErrInsufficientCredits if balance too low.
	Spend(ctx context.Context, accountID string, amount int, reason, requestID string) error
	// SpendSplit is Spend that also reports how much left each bucket (free first, then paid),
	// so an escrow can be returned to the buckets it came from. The free expiry is kept (cleared
	// when the free balance reaches 0). An idempotent replay reports (0, 0, nil).
	SpendSplit(ctx context.Context, accountID string, amount int, reason, requestID string) (fromFree, fromPaid int, err error)
	// CreditFreeKeepExpiry adds free credits without moving an existing free expiry; expiresAt
	// applies only when the account had no free balance (or no expiry) before the credit.
	CreditFreeKeepExpiry(ctx context.Context, accountID string, amount int, reason, requestID string, expiresAt time.Time) error
	// SpendPaidOnly deducts strictly from paid balance. Returns ErrInsufficientPaidCredits if paid balance < amount.
	// Used for detailed (gpt-5.4) mode where free credits are not eligible.
	SpendPaidOnly(ctx context.Context, accountID string, amount int, reason, requestID string) error
	// SetFreeBalance directly sets the free balance (used by expiry job).
	SetFreeBalance(ctx context.Context, accountID string, balance int) error
	// SetPaidBalance directly sets the paid balance (used by recovery).
	SetPaidBalance(ctx context.Context, accountID string, balance int) error
	// SetDetailedTrial sets both the trial counter and expiry timestamp.
	// Used by registration to seed new accounts with N trial calls.
	SetDetailedTrial(ctx context.Context, accountID string, remaining int, expiresAt time.Time) error
	// DecrementDetailedTrial atomically decrements the trial counter. Returns
	// ErrTrialExhausted if the counter is 0 or expired. Used when paid balance is
	// insufficient for a detailed call.
	DecrementDetailedTrial(ctx context.Context, accountID string, now time.Time) error
	// RestoreDetailedTrial increments the trial counter (used to refund a trial
	// call when the user aborts mid-flight via the Stop button).
	RestoreDetailedTrial(ctx context.Context, accountID string) error
	// GetTransactionByRequestID returns a transaction by its idempotency key, or ErrNotFound.
	GetTransactionByRequestID(ctx context.Context, accountID, requestID string) (*models.CreditTransaction, error)
	// ListTransactions returns transactions for an account, newest first.
	ListTransactions(ctx context.Context, accountID string, limit, offset int) ([]*models.CreditTransaction, error)
	// SumSpentByReasonsSince returns how many credits the account spent (debits only, as a
	// positive number) with one of the given reasons at or after since.
	SumSpentByReasonsSince(ctx context.Context, accountID string, reasons []string, since time.Time) (int, error)
	// ListExpirableBalances returns balances with free_balance > 0 and free_credits_expires_at before the given time.
	ListExpirableBalances(ctx context.Context, now time.Time) ([]*models.CreditBalance, error)
}

// NonceRepository manages challenge-response registration nonces.
type NonceRepository interface {
	// Upsert creates or replaces the active nonce for a peer_id.
	Upsert(ctx context.Context, nonce *models.RegistrationNonce) error
	// GetActiveByPeerID returns the unconsumed nonce for the peer, or ErrNotFound.
	GetActiveByPeerID(ctx context.Context, peerID string) (*models.RegistrationNonce, error)
	// MarkConsumed marks the nonce as consumed. Returns ErrNotFound if not found or already consumed.
	MarkConsumed(ctx context.Context, id string) error
}

// BlockRepository manages registration blocks (rate limiting persistence).
type BlockRepository interface {
	// IsBlocked returns true if a block exists for the given type+value that hasn't expired.
	IsBlocked(ctx context.Context, blockType, blockValue string, now time.Time) (bool, error)
	// Insert creates a registration block. Upserts on (block_type, block_value) conflict.
	Insert(ctx context.Context, block *models.RegistrationBlock) error
	// CleanExpired removes blocks that have expired before the given time.
	CleanExpired(ctx context.Context, now time.Time) (int, error)
}

// SocialRepository manages social platform connections.
type SocialRepository interface {
	// Insert adds a social connection. Returns ErrAlreadyExists on duplicate (account_id, platform).
	Insert(ctx context.Context, conn *models.SocialConnection) error
	// GetByAccountID returns all connections for an account.
	GetByAccountID(ctx context.Context, accountID string) ([]*models.SocialConnection, error)
	// GetByAccountAndPlatform returns a specific connection, or ErrNotFound.
	GetByAccountAndPlatform(ctx context.Context, accountID, platform string) (*models.SocialConnection, error)
	// Delete removes a social connection.
	Delete(ctx context.Context, accountID, platform string) error
}

// WalletRepository manages wallet linking and grant history.
type WalletRepository interface {
	// LinkWallet adds a wallet to an account. Returns ErrAlreadyExists on duplicate.
	LinkWallet(ctx context.Context, wallet *models.AccountWallet) error
	// GetByAccountID returns the wallet for an account on a given chain, or ErrNotFound.
	GetByAccountID(ctx context.Context, accountID, chain string) (*models.AccountWallet, error)
	// UpsertWalletForDisplay links a wallet to an account for display (no signature verification). Used by PATCH /peers/me.
	UpsertWalletForDisplay(ctx context.Context, accountID, walletAddress, chain string) error
	// HasGrantHistory returns true if this wallet already received a bonus.
	HasGrantHistory(ctx context.Context, walletAddress, chain string) (bool, error)
	// InsertGrantHistory records a wallet bonus grant.
	InsertGrantHistory(ctx context.Context, grant *models.WalletGrantHistory) error
}

// PurchaseRepository manages Solana purchase intents.
type PurchaseRepository interface {
	// CreateIntent inserts a new purchase intent.
	CreateIntent(ctx context.Context, intent *models.PurchaseIntent) error
	// GetIntent returns an intent by ID, or ErrNotFound.
	GetIntent(ctx context.Context, id string) (*models.PurchaseIntent, error)
	// MarkVerified updates intent status to verified with tx signature.
	MarkVerified(ctx context.Context, id, txSignature string, verifiedAt time.Time) error
	// ExpireStaleIntents marks pending intents past their expiry as expired. Returns count.
	ExpireStaleIntents(ctx context.Context, now time.Time) (int, error)
	// HasProcessedSignature returns true if this tx signature was already processed.
	HasProcessedSignature(ctx context.Context, txSignature string) (bool, error)
	// RecordProcessedSignature records a processed tx signature for replay prevention.
	RecordProcessedSignature(ctx context.Context, txSignature, intentID string) error
}

// PeerEventRepository manages peer activity events (F-032, US-032-02).
type PeerEventRepository interface {
	// Insert adds a new peer event.
	Insert(ctx context.Context, event *models.PeerEvent) error
	// ListByPeerID returns events for a peer, ordered by CreatedAt DESC. Returns (events, total, error).
	ListByPeerID(ctx context.Context, peerID string, limit, offset int) ([]*models.PeerEvent, int, error)
	// CountByPeerIDAndAction returns the count of events for a peer with the given action.
	CountByPeerIDAndAction(ctx context.Context, peerID, action string) (int, error)
}

// ReputationSnapshotRepository manages periodic reputation score snapshots (F-032, US-032-02).
type ReputationSnapshotRepository interface {
	// Upsert inserts or updates a snapshot for a peer.
	Upsert(ctx context.Context, snapshot *reputation.ReputationSnapshot) error
	// FindPrevious returns the most recent snapshot before the given time, or nil if none.
	FindPrevious(ctx context.Context, peerID string, before time.Time) (*reputation.ReputationSnapshot, error)
}

// AgentChatHistoryMessage represents a persisted token-chat message keyed by user + token_address.
// JSON tags align with append DTOs and daemon history parsing (lowercase / snake_case).
type AgentChatHistoryMessage struct {
	Seq             int       `json:"seq"`
	Role            string    `json:"role"`
	Content         string    `json:"content"`
	AgentID         string    `json:"agent_id"`
	CreditsDeducted int       `json:"credits_deducted"`
	CreatedAt       time.Time `json:"created_at"`
}

// AgentChatHistoryRepository persists token-chat history in tracker storage.
type AgentChatHistoryRepository interface {
	AppendMessages(ctx context.Context, userID, tokenAddress string, messages []*AgentChatHistoryMessage) error
	ListMessages(ctx context.Context, userID, tokenAddress string) ([]*AgentChatHistoryMessage, error)
	// PersonalSessionBounds returns whether the user has personal-agent messages (token_address '') and first/last message times.
	PersonalSessionBounds(ctx context.Context, userID string) (hasMessages bool, firstAt, lastAt time.Time, err error)
}

// ReplicationRepository persists S3 replication state and replication job attempts.
type ReplicationRepository interface {
	UpsertAssetReplication(ctx context.Context, replication *models.AssetReplication) error
	GetAssetReplication(ctx context.Context, cid string) (*models.AssetReplication, error)
	CreateReplicationJob(ctx context.Context, job *models.ReplicationJob) error
}

// LaunchSettingsRepository persists the live launch fee (StonkAgents launchpad, single row).
type LaunchSettingsRepository interface {
	// GetFee returns the persisted launch fee, or ErrNotFound if it was never priced.
	GetFee(ctx context.Context) (*models.LaunchFee, error)
	// UpsertFee replaces the persisted launch fee.
	UpsertFee(ctx context.Context, fee *models.LaunchFee) error
}

// LaunchQuoteRepository reads quote tokens a launch can raise in.
type LaunchQuoteRepository interface {
	// ListEnabled returns enabled quotes ordered by sort_order ASC.
	ListEnabled(ctx context.Context) ([]*models.LaunchQuote, error)
	// GetByMint returns a quote by mint (enabled or not), or ErrNotFound.
	GetByMint(ctx context.Context, quoteMint string) (*models.LaunchQuote, error)
}

// --- StonkAgents launchpad: LaunchLab launch records + platform revenue ledger ---

// ListLaunchesOptions filters and paginates launch listings.
type ListLaunchesOptions struct {
	CreatorWallet string // "" = all creators
	UnboundOnly   bool   // only launches with no peer bound yet
	Limit         int
	Offset        int
}

// LaunchRepository persists Raydium LaunchLab launches recorded by the portal.
type LaunchRepository interface {
	// Create inserts a launch. Returns ErrAlreadyExists if the mint or signature is already recorded.
	Create(ctx context.Context, launch *models.TokenLaunch) error
	// GetByMint returns the launch for a mint, or ErrNotFound.
	GetByMint(ctx context.Context, mint string) (*models.TokenLaunch, error)
	// GetBySignature returns the launch recorded from a launch transaction, or ErrNotFound.
	GetBySignature(ctx context.Context, signature string) (*models.TokenLaunch, error)
	// List returns launches ordered by created_at DESC plus the total matching count.
	List(ctx context.Context, opts ListLaunchesOptions) ([]*models.TokenLaunch, int, error)
	// Bind sets peer_id, status='bound' and bound_at on an unbound launch.
	// Returns ErrNotFound if the mint is unknown and ErrAlreadyExists if it is already bound.
	Bind(ctx context.Context, mint, peerID string, boundAt time.Time) error
	// GetByMints returns the launches that exist among mints, keyed by mint.
	GetByMints(ctx context.Context, mints []string) (map[string]*models.TokenLaunch, error)
	// ListByPeerID returns the launches bound to peerID, newest first.
	ListByPeerID(ctx context.Context, peerID string) ([]*models.TokenLaunch, error)
}

// RevenueRepository persists the platform revenue ledger.
type RevenueRepository interface {
	// Insert appends a ledger entry. Entries with a signature are idempotent: a duplicate
	// signature returns ErrAlreadyExists without writing.
	Insert(ctx context.Context, entry *models.PlatformRevenue) error
	// TotalsByKind returns count and sums per kind across the whole ledger.
	TotalsByKind(ctx context.Context) ([]*models.RevenueKindTotal, error)
	// DailyByKind returns per-day (UTC), per-kind aggregates for entries at or after since.
	DailyByKind(ctx context.Context, since time.Time) ([]*models.RevenueDailyRow, error)
	// Recent returns the newest ledger entries first, at most limit of them.
	Recent(ctx context.Context, limit int) ([]*models.PlatformRevenue, error)
}

// --- StonkAgents portal feedback ---

// ListFeedbackOptions pages feedback newest-first by id. BeforeID > 0 returns rows with a
// smaller id (the cursor is the last id of the previous page); 0 starts at the newest.
type ListFeedbackOptions struct {
	Limit    int
	BeforeID int64
}

// FeedbackRepository persists portal feedback (table feedback, migration 016).
type FeedbackRepository interface {
	// Create inserts a submission and fills ID and CreatedAt.
	Create(ctx context.Context, fb *models.Feedback) error
	// List returns submissions ordered by id DESC.
	List(ctx context.Context, opts ListFeedbackOptions) ([]*models.Feedback, error)
}

// --- StonkAgents roadmap interest ---

// ListInterestOptions pages interest newest-first by id (same cursor scheme as feedback).
type ListInterestOptions struct {
	Limit    int
	BeforeID int64
}

// InterestRepository persists capability interest (table agent_interest, migration 017).
type InterestRepository interface {
	// Create inserts a submission and fills ID and CreatedAt.
	Create(ctx context.Context, it *models.AgentInterest) error
	// List returns submissions ordered by id DESC.
	List(ctx context.Context, opts ListInterestOptions) ([]*models.AgentInterest, error)
	// CountByCapability returns, for each key, how many rows contain it (0 when none).
	CountByCapability(ctx context.Context, keys []string) (map[string]int64, error)
	// Summary rolls up every row by capability and priority.
	Summary(ctx context.Context) (*models.InterestSummary, error)
}

// --- StonkAgents launchpad: trade indexer (migration 018) ---

// TradeCursor is the keyset position of a newest-first trade page: the last row of the
// previous page. Rows strictly older by (block_time, slot, signature) come next.
type TradeCursor struct {
	BlockTime time.Time
	Slot      int64
	Signature string
}

// LaunchTradeRepository persists indexed pool swaps and the per-address index cursors.
type LaunchTradeRepository interface {
	// InsertTrades stores trades idempotently on signature; returns how many were new.
	InsertTrades(ctx context.Context, trades []*models.LaunchTrade) (int, error)
	// ListTrades returns a mint's trades newest first, at most limit, after the cursor (nil = newest).
	ListTrades(ctx context.Context, mint string, limit int, after *TradeCursor) ([]*models.LaunchTrade, error)
	// ListTradesAsc returns a mint's trades with block_time >= since, oldest first, at most limit.
	ListTradesAsc(ctx context.Context, mint string, since time.Time, limit int) ([]*models.LaunchTrade, error)
	// AggregateWindow returns, per mint, the volume and count in (from, to] plus the prices of
	// the last trade at or before from and at or before to. Mints without trades are omitted.
	AggregateWindow(ctx context.Context, mints []string, from, to time.Time) (map[string]*models.LaunchTradeWindow, error)
	// GetCursor returns the index cursor for key, or ErrNotFound.
	GetCursor(ctx context.Context, key string) (*models.LaunchIndexCursor, error)
	// UpsertCursor stores the index cursor for cursor.Key.
	UpsertCursor(ctx context.Context, cursor *models.LaunchIndexCursor) error
}

// LaunchBurnRepository persists burns of the network token.
type LaunchBurnRepository interface {
	// InsertBurns stores burns idempotently on signature; returns how many were new.
	InsertBurns(ctx context.Context, burns []*models.LaunchBurn) (int, error)
	// Summary returns the total burned and the burn count for mint (zero when none).
	Summary(ctx context.Context, mint string) (*models.LaunchBurnSummary, error)
	// Recent returns the newest burns first, at most limit.
	Recent(ctx context.Context, mint string, limit int) ([]*models.LaunchBurn, error)
}

// --- StonkAgents devnet drip ---

// DevDripRepository persists the per-wallet drip record (table dev_drips, migration 020).
type DevDripRepository interface {
	// Get returns the latest drip for wallet, or models.ErrNotFound.
	Get(ctx context.Context, wallet string) (*models.DevDrip, error)
	// Upsert inserts or replaces the wallet's row.
	Upsert(ctx context.Context, drip *models.DevDrip) error
	// ListByIPSince returns the dripped_at times of rows with ipHash at or after since, oldest first.
	ListByIPSince(ctx context.Context, ipHash string, since time.Time) ([]time.Time, error)
}
