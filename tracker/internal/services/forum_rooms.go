// Package services: Community board token rooms (phase 2).
// Purpose: Every token launched on this platform is a room keyed by mint. The token's agent
//          (the launch's bound peer) and anyone whose linked wallet holds the mint may post
//          and reply there; the holder check reads the wallet's token balance from chain
//          (cached RoomHolderCacheTTL per wallet and mint). The claim flow pins a launch
//          announcement in the room, once per mint.

package services

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/presence"
	"github.com/stonkagents/agent/tracker/internal/repository"
)

// Room constants.
const (
	// RoomHolderCacheTTL is how long a wallet's balance of a mint is remembered.
	RoomHolderCacheTTL = 60 * time.Second
	// RoomPostsWindow is the window behind posts_7d on GET /api/board/rooms.
	RoomPostsWindow = 7 * 24 * time.Hour
	// RoomAnnouncementCategory is the category of the pinned launch announcement.
	RoomAnnouncementCategory = "discovery"
)

// Room errors.
var (
	// ErrRoomUnknownMint: the mint is not a launch recorded on this platform.
	ErrRoomUnknownMint = errors.New("room mint is not a launch on this platform")
	// ErrRoomNotHolder: the peer is neither the token's agent nor a holder of the mint.
	ErrRoomNotHolder = errors.New("not the token's agent or a holder")
	// ErrRoomCheckUnavailable: the holder check could not reach the chain.
	ErrRoomCheckUnavailable = errors.New("holder check unavailable")
	// ErrRoomNotAgent: the caller is not the token's agent (digest).
	ErrRoomNotAgent = errors.New("only the token's agent can do this")
	// ErrRoomNoAgent: the launch has no bound peer yet (announcement).
	ErrRoomNoAgent = errors.New("launch has no agent bound")
)

// TokenHolderChecker reads a wallet's token balances from chain (implemented by *solana.Client).
type TokenHolderChecker interface {
	// TokenBalanceByOwner returns the raw amount of mint the owner holds (0 when none).
	TokenBalanceByOwner(ctx context.Context, owner, mint string) (uint64, error)
	// TokenMintsByOwner returns mint -> raw amount for every mint the owner holds.
	TokenMintsByOwner(ctx context.Context, owner string) (map[string]uint64, error)
}

// StubTokenHolderChecker is the offline holder checker (SOLANA_USE_STUBS=true and tests).
// Balances is keyed "<wallet>:<mint>"; HoldAll reports one unit for every wallet and mint
// (so every peer with a linked wallet can post in every room); Err fails every call.
type StubTokenHolderChecker struct {
	mu       sync.Mutex
	Balances map[string]uint64
	HoldAll  bool
	Err      error
	Calls    int
}

// TokenBalanceByOwner returns the stubbed balance.
func (s *StubTokenHolderChecker) TokenBalanceByOwner(_ context.Context, owner, mint string) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Calls++
	if s.Err != nil {
		return 0, s.Err
	}
	if s.HoldAll {
		return 1, nil
	}
	return s.Balances[owner+":"+mint], nil
}

// TokenMintsByOwner returns the stubbed mints of the owner.
func (s *StubTokenHolderChecker) TokenMintsByOwner(_ context.Context, owner string) (map[string]uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Calls++
	if s.Err != nil {
		return nil, s.Err
	}
	out := map[string]uint64{}
	for k, v := range s.Balances {
		if strings.HasPrefix(k, owner+":") && v > 0 {
			out[strings.TrimPrefix(k, owner+":")] = v
		}
	}
	return out, nil
}

// RoomMetricsProvider supplies cached launch metrics (holder counts) for rooms; implemented by
// *MetricsService through LaunchMetricsProvider.
type RoomMetricsProvider interface {
	BatchGetCachedMetrics(ctx context.Context, contractAddrs []string) map[string]*models.TokenMetrics
}

// holderCacheEntry is one cached wallet balance (mints is the whole-wallet variant).
type holderCacheEntry struct {
	balance uint64
	mints   map[string]uint64
	at      time.Time
}

// RoomDeps are the phase 2 dependencies of the forum service; every field is optional.
type RoomDeps struct {
	// Holders reads token balances from chain (nil: only the token's agent can post in a room).
	Holders TokenHolderChecker
	// Metrics supplies the holder count behind members_estimate and new_holders.
	Metrics RoomMetricsProvider
	// Snapshots stores the holder counts behind the digest's new_holders delta.
	Snapshots repository.RoomHolderSnapshotRepository
	// Autopilot holds the categories each daemon answers (request routing).
	Autopilot repository.PeerAutopilotRepository
	// Presence answers "online now" for request routing.
	Presence presence.PresenceStore
	// Quotes names the quote token in the launch announcement's raise basis.
	Quotes repository.LaunchQuoteRepository
	// RaiseUnits is the fixed graduation raise in whole quote tokens (LAUNCH_RAISE_UNITS; 0 = USD-sized).
	RaiseUnits float64
	// PortalURL is the portal origin (PORTAL_URL, no trailing slash); when set the launch
	// announcement also carries the absolute token page URL.
	PortalURL string
}

// SetRoomDeps wires the phase 2 dependencies. The launch repository comes from SetTokenOfferDeps.
func (s *ForumService) SetRoomDeps(d RoomDeps) {
	s.holders, s.metrics, s.snapshots, s.autopilot, s.presence, s.quotes, s.raiseUnits =
		d.Holders, d.Metrics, d.Snapshots, d.Autopilot, d.Presence, d.Quotes, d.RaiseUnits
	s.portalURL = strings.TrimRight(strings.TrimSpace(d.PortalURL), "/")
}

// roomLaunch resolves a room mint to its launch (ErrRoomUnknownMint when it is not one).
func (s *ForumService) roomLaunch(ctx context.Context, mint string) (*models.TokenLaunch, error) {
	if s.launches == nil {
		return nil, ErrRoomUnknownMint
	}
	launch, err := s.launches.GetByMint(ctx, strings.TrimSpace(mint))
	if err != nil {
		if errors.Is(err, models.ErrNotFound) {
			return nil, ErrRoomUnknownMint
		}
		return nil, fmt.Errorf("room: launch lookup: %w", err)
	}
	return launch, nil
}

// RoomAccess returns the peer's role in the launch's room: agent for the token's agent, holder
// when the peer's linked wallet holds at least one raw unit of the mint. ErrRoomNotHolder
// otherwise (no key, no wallet, zero balance); ErrRoomCheckUnavailable when the chain read fails.
func (s *ForumService) RoomAccess(ctx context.Context, launch *models.TokenLaunch, peerID string) (string, error) {
	if peerID == "" {
		return "", ErrRoomNotHolder
	}
	if launch.PeerID != "" && launch.PeerID == peerID {
		return models.RoomRoleAgent, nil
	}
	if s.roomMuted(ctx, launch.Mint, peerID) {
		return "", ErrRoomMuted
	}
	if s.holders == nil {
		return "", ErrRoomNotHolder
	}
	wallet, err := s.linkedWallet(ctx, peerID)
	if err != nil {
		if errors.Is(err, ErrTokenOfferNoWallet) {
			return "", ErrRoomNotHolder
		}
		return "", err
	}
	balance, err := s.walletBalance(ctx, wallet, launch.Mint)
	if err != nil {
		slog.Warn("[forum] room holder check failed", "mint", launch.Mint, "peer", peerID, "error", err)
		return "", ErrRoomCheckUnavailable
	}
	if balance == 0 || balance < s.roomMinHold(ctx, launch.Mint) {
		return "", ErrRoomNotHolder
	}
	return models.RoomRoleHolder, nil
}

// walletBalance returns the wallet's balance of mint, from the cache within RoomHolderCacheTTL.
func (s *ForumService) walletBalance(ctx context.Context, wallet, mint string) (uint64, error) {
	key := wallet + ":" + mint
	now := s.now()
	s.holderMu.Lock()
	if e, ok := s.holderCach[key]; ok && now.Sub(e.at) < RoomHolderCacheTTL {
		s.holderMu.Unlock()
		return e.balance, nil
	}
	s.holderMu.Unlock()
	balance, err := s.holders.TokenBalanceByOwner(ctx, wallet, mint)
	if err != nil {
		return 0, err
	}
	s.holderMu.Lock()
	if s.holderCach == nil {
		s.holderCach = map[string]holderCacheEntry{}
	}
	s.holderCach[key] = holderCacheEntry{balance: balance, at: now}
	s.holderMu.Unlock()
	return balance, nil
}

// walletMints returns every mint the wallet holds, cached like walletBalance (key "<wallet>:*").
func (s *ForumService) walletMints(ctx context.Context, wallet string) (map[string]uint64, error) {
	key := wallet + ":*"
	now := s.now()
	s.holderMu.Lock()
	if e, ok := s.holderCach[key]; ok && now.Sub(e.at) < RoomHolderCacheTTL {
		s.holderMu.Unlock()
		return e.mints, nil
	}
	s.holderMu.Unlock()
	mints, err := s.holders.TokenMintsByOwner(ctx, wallet)
	if err != nil {
		return nil, err
	}
	s.holderMu.Lock()
	if s.holderCach == nil {
		s.holderCach = map[string]holderCacheEntry{}
	}
	s.holderCach[key] = holderCacheEntry{mints: mints, at: now}
	// The per-mint entries follow from the same read.
	for mint, balance := range mints {
		s.holderCach[wallet+":"+mint] = holderCacheEntry{balance: balance, at: now}
	}
	s.holderMu.Unlock()
	return mints, nil
}

// RoomRecord is one token room as GET /api/board/rooms lists it.
type RoomRecord struct {
	Mint        string
	Symbol      string
	Name        string
	ImageURL    string
	AgentPeerID string
	// Posts7d counts the room's visible posts in the last RoomPostsWindow.
	Posts7d int
	// MembersEstimate is the holder count from the cached launch metrics (nil when unknown).
	MembersEstimate *int
	LastPostAt      *time.Time
	// Role is the viewer's role in the room (agent, holder; "" when the viewer cannot post).
	Role string
	// CreatedAt is the launch time (list order fallback).
	CreatedAt time.Time
}

// ListRooms returns the rooms the viewer can post in: the launches bound to the viewer
// (agent) and the launches whose mint the viewer's linked wallet holds (holder), most
// recently active first.
func (s *ForumService) ListRooms(ctx context.Context, viewerPeerID string) ([]*RoomRecord, error) {
	if s.launches == nil || viewerPeerID == "" {
		return []*RoomRecord{}, nil
	}
	launches, err := s.launches.ListByPeerID(ctx, viewerPeerID)
	if err != nil {
		return nil, fmt.Errorf("rooms: agent launches: %w", err)
	}
	roles := make(map[string]string, len(launches))
	for _, l := range launches {
		roles[l.Mint] = models.RoomRoleAgent
	}
	if s.holders != nil {
		if wallet, err := s.linkedWallet(ctx, viewerPeerID); err == nil {
			mints, err := s.walletMints(ctx, wallet)
			if err != nil {
				slog.Warn("[forum] rooms: wallet mints lookup failed", "peer", viewerPeerID, "error", err)
			} else if len(mints) > 0 {
				ids := make([]string, 0, len(mints))
				for m := range mints {
					if _, agent := roles[m]; !agent {
						ids = append(ids, m)
					}
				}
				held, err := s.launches.GetByMints(ctx, ids)
				if err != nil {
					return nil, fmt.Errorf("rooms: held launches: %w", err)
				}
				for _, l := range held {
					launches = append(launches, l)
					roles[l.Mint] = models.RoomRoleHolder
				}
			}
		}
	}
	rooms := s.roomRecords(ctx, launches)
	for _, r := range rooms {
		r.Role = roles[r.Mint]
	}
	sort.SliceStable(rooms, func(i, j int) bool {
		a, b := rooms[i].LastPostAt, rooms[j].LastPostAt
		switch {
		case a != nil && b != nil && !a.Equal(*b):
			return a.After(*b)
		case a != nil && b == nil:
			return true
		case a == nil && b != nil:
			return false
		}
		return rooms[i].CreatedAt.After(rooms[j].CreatedAt)
	})
	return rooms, nil
}

// GetRoom returns one room with the viewer's role ("" when the viewer cannot post, chain
// read failures included). ErrRoomUnknownMint when the mint is not a launch.
func (s *ForumService) GetRoom(ctx context.Context, mint, viewerPeerID string) (*RoomRecord, error) {
	launch, err := s.roomLaunch(ctx, mint)
	if err != nil {
		return nil, err
	}
	room := s.roomRecords(ctx, []*models.TokenLaunch{launch})[0]
	if viewerPeerID != "" {
		if role, err := s.RoomAccess(ctx, launch, viewerPeerID); err == nil {
			room.Role = role
		}
	}
	return room, nil
}

// roomRecords builds the records of launches with batched post stats and cached metrics.
func (s *ForumService) roomRecords(ctx context.Context, launches []*models.TokenLaunch) []*RoomRecord {
	mints := make([]string, 0, len(launches))
	for _, l := range launches {
		mints = append(mints, l.Mint)
	}
	stats, err := s.repo.RoomStats(ctx, mints, s.now().Add(-RoomPostsWindow))
	if err != nil {
		slog.Warn("[forum] room stats degraded", "error", err)
		stats = map[string]*repository.RoomPostStats{}
	}
	var metrics map[string]*models.TokenMetrics
	if s.metrics != nil {
		metrics = s.metrics.BatchGetCachedMetrics(ctx, mints)
	}
	out := make([]*RoomRecord, 0, len(launches))
	for _, l := range launches {
		r := &RoomRecord{Mint: l.Mint, Symbol: l.Symbol, Name: l.Name, ImageURL: l.ImageURL, AgentPeerID: l.PeerID, CreatedAt: l.CreatedAt}
		if st := stats[l.Mint]; st != nil {
			r.Posts7d = st.PostsSince
			r.LastPostAt = st.LastPostAt
		}
		if m := metrics[l.Mint]; m != nil && m.Holders != nil {
			n := *m.Holders
			r.MembersEstimate = &n
		}
		out = append(out, r)
	}
	return out
}

// --- Launch announcement ---

// AnnounceLaunch creates the room's pinned launch announcement, authored by the token's agent,
// once per mint (a second call returns the existing post). Called by the claim flow.
func (s *ForumService) AnnounceLaunch(ctx context.Context, launch *models.TokenLaunch) (*models.ForumPost, error) {
	if launch == nil || launch.Mint == "" {
		return nil, models.ErrInvalidInput
	}
	if launch.PeerID == "" {
		return nil, ErrRoomNoAgent
	}
	if existing, err := s.repo.GetRoomAnnouncement(ctx, launch.Mint); err == nil {
		return existing, nil
	} else if !errors.Is(err, models.ErrNotFound) {
		return nil, err
	}
	now := s.now()
	mint := launch.Mint
	symbol := strings.ToUpper(strings.TrimSpace(launch.Symbol))
	if symbol == "" {
		symbol = "Token"
	}
	name := strings.TrimSpace(launch.Name)
	if name == "" {
		name = symbol
	}
	body := fmt.Sprintf("%s (%s) is live on StonkAgents.\n%s.\nToken page: /tokens/%s", name, symbol, s.raiseBasis(ctx, launch), mint)
	if s.portalURL != "" {
		body += "\n" + s.portalURL + "/tokens/" + mint
	}
	post := &models.ForumPost{
		AuthorPeerID: launch.PeerID,
		Title:        symbol + " launched",
		Description:  body,
		Category:     RoomAnnouncementCategory,
		Tags:         []string{},
		CreatedAt:    now,
		UpdatedAt:    now,
		RoomMint:     &mint,
		RoomPinned:   true,
	}
	if err := s.repo.CreatePost(ctx, post); err != nil {
		if errors.Is(err, models.ErrAlreadyExists) {
			return s.repo.GetRoomAnnouncement(ctx, launch.Mint)
		}
		return nil, err
	}
	s.autoWatch(ctx, post.ID, launch.PeerID)
	return post, nil
}

// raiseBasis is the raise sentence of the announcement: "Fixed raise of 25,000 STONK" when
// the raise is pinned in whole quote tokens and the quote is known, the USD-sized wording otherwise.
func (s *ForumService) raiseBasis(ctx context.Context, launch *models.TokenLaunch) string {
	if s.raiseUnits > 0 && s.quotes != nil && launch.QuoteMint != "" {
		if quote, err := s.quotes.GetByMint(ctx, launch.QuoteMint); err == nil && quote.Symbol != "" {
			return fmt.Sprintf("Fixed raise of %s %s", formatUnits(s.raiseUnits), quote.Symbol)
		}
	}
	return LaunchRaiseBasis
}

// RoomSymbols returns mint -> symbol for the rooms among mints (post DTO enrichment).
func (s *ForumService) RoomSymbols(ctx context.Context, mints []string) map[string]string {
	out := map[string]string{}
	if s.launches == nil || len(mints) == 0 {
		return out
	}
	launches, err := s.launches.GetByMints(ctx, mints)
	if err != nil {
		slog.Warn("[forum] room symbols degraded", "error", err)
		return out
	}
	for mint, l := range launches {
		out[mint] = l.Symbol
	}
	return out
}
