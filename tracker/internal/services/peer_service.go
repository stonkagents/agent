// Package: tracker/internal/services
// Feature: F-007 (Centralized Tracker)
// Story: US-007-02 (Peer Registry with Redis Presence Tracking)
// Purpose: Business logic for peer registration, discovery, heartbeat

package services

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/stonkagents/agent/tracker/internal/geo"
	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/presence"
	"github.com/stonkagents/agent/tracker/internal/repository"
)

// DefaultPresenceTTL is the default TTL for peer presence heartbeats.
const DefaultPresenceTTL = 5 * time.Minute

// RegisterPeerRequest contains fields for peer registration.
type RegisterPeerRequest struct {
	PeerID     string
	PublicKey  string
	Multiaddrs []string
	Country    string
	Region     string
}

// DiscoverOptions configures peer discovery queries.
type DiscoverOptions struct {
	Limit      int
	Offset     int // F-032: pagination offset for enriched peer list
	OnlineOnly bool
}

// PeerService handles peer registration, discovery, and heartbeats.
type PeerService struct {
	repo       repository.PeerRepository
	presence   presence.PresenceStore
	apiKeyRepo repository.PeerAPIKeyRepository
	// For UpdateWalletAddress: wallet stored in account_wallets (consolidated from peers.wallet_address)
	accountRepo repository.AccountRepository
	creditRepo  repository.CreditRepository
	walletRepo  repository.WalletRepository
}

// PeerServiceDeps holds dependencies for NewPeerService. AccountRepo, CreditRepo, WalletRepo are optional for UpdateWalletAddress.
type PeerServiceDeps struct {
	Repo        repository.PeerRepository
	Presence    presence.PresenceStore
	APIKeyRepo  repository.PeerAPIKeyRepository
	AccountRepo repository.AccountRepository
	CreditRepo  repository.CreditRepository
	WalletRepo  repository.WalletRepository
}

// NewPeerService creates a new PeerService. apiKeyRepo is optional; if set, API key is issued on first register.
func NewPeerService(repo repository.PeerRepository, pres presence.PresenceStore, apiKeyRepo repository.PeerAPIKeyRepository) *PeerService {
	return &PeerService{repo: repo, presence: pres, apiKeyRepo: apiKeyRepo}
}

// NewPeerServiceWithDeps creates a PeerService with full deps (for UpdateWalletAddress via account_wallets).
func NewPeerServiceWithDeps(deps PeerServiceDeps) *PeerService {
	return &PeerService{
		repo:        deps.Repo,
		presence:    deps.Presence,
		apiKeyRepo:  deps.APIKeyRepo,
		accountRepo: deps.AccountRepo,
		creditRepo:  deps.CreditRepo,
		walletRepo:  deps.WalletRepo,
	}
}

// Register creates or updates a peer and sends a heartbeat.
func (s *PeerService) Register(ctx context.Context, req RegisterPeerRequest) (*models.Peer, error) {
	if req.PeerID == "" {
		return nil, models.ErrInvalidInput
	}
	if req.PublicKey == "" {
		return nil, models.ErrInvalidInput
	}

	now := time.Now()

	// Get existing peer to preserve FirstSeen and transfer stats
	existing, _ := s.repo.FindByID(ctx, req.PeerID)

	peer := &models.Peer{
		PeerID:       req.PeerID,
		PublicKey:    req.PublicKey,
		Multiaddrs:   req.Multiaddrs,
		LastSeen:     now,
		Country:      req.Country,
		Region:       req.Region,
		MaskedPeerID: geo.MaskPeerID(req.PeerID),
	}

	if existing != nil {
		peer.FirstSeen = existing.FirstSeen
		peer.TotalUptimeSeconds = existing.TotalUptimeSeconds
		peer.TotalUploadBytes = existing.TotalUploadBytes
		peer.TotalDownloadBytes = existing.TotalDownloadBytes
		peer.AverageSpeedBytesPerSec = existing.AverageSpeedBytesPerSec
		// Preserve location if new registration doesn't have it
		if peer.Country == "" {
			peer.Country = existing.Country
		}
		if peer.Region == "" {
			peer.Region = existing.Region
		}
	} else {
		peer.FirstSeen = now
	}

	if err := s.repo.Upsert(ctx, peer); err != nil {
		return nil, err
	}

	if err := s.presence.Heartbeat(ctx, req.PeerID, DefaultPresenceTTL); err != nil {
		return nil, err
	}

	// Issue or return API key: first register gets a new key; re-register gets existing key so daemon can re-save it (e.g. after restart).
	if s.apiKeyRepo != nil {
		exists, _ := s.apiKeyRepo.ExistsForPeer(ctx, req.PeerID)
		if !exists {
			key, err := s.apiKeyRepo.Create(ctx, req.PeerID)
			if err == nil {
				peer.APIKey = key
			}
		} else {
			key, _ := s.apiKeyRepo.GetByPeerID(ctx, req.PeerID)
			peer.APIKey = key
		}
	}

	return peer, nil
}

// Discover returns peers. If OnlineOnly is true, only online peers are returned.
// If OnlineOnly is false, all registered peers are returned.
func (s *PeerService) Discover(ctx context.Context, opts DiscoverOptions) ([]*models.Peer, error) {
	if opts.OnlineOnly {
		return s.discoverOnline(ctx, opts.Limit)
	}
	return s.discoverAll(ctx, opts.Limit, opts.Offset)
}

func (s *PeerService) discoverOnline(ctx context.Context, limit int) ([]*models.Peer, error) {
	onlineIDs, err := s.presence.OnlinePeerIDs(ctx)
	if err != nil {
		return nil, err
	}
	if len(onlineIDs) == 0 {
		return []*models.Peer{}, nil
	}

	peers, err := s.repo.FindByIDs(ctx, onlineIDs)
	if err != nil {
		return nil, err
	}

	if limit > 0 && len(peers) > limit {
		peers = peers[:limit]
	}
	return peers, nil
}

func (s *PeerService) discoverAll(ctx context.Context, limit, offset int) ([]*models.Peer, error) {
	peers, err := s.repo.List(ctx, repository.ListPeersOptions{Limit: limit, Offset: offset})
	if err != nil {
		return nil, err
	}
	return peers, nil
}

// FindByID returns a peer by ID.
func (s *PeerService) FindByID(ctx context.Context, peerID string) (*models.Peer, error) {
	return s.repo.FindByID(ctx, peerID)
}

// IsOnline returns whether the peer is currently online (heartbeat within TTL).
func (s *PeerService) IsOnline(ctx context.Context, peerID string) (bool, error) {
	return s.presence.IsOnline(ctx, peerID)
}

// OnlinePeerIDs returns peer IDs currently considered online (last_seen within TTL).
func (s *PeerService) OnlinePeerIDs(ctx context.Context) ([]string, error) {
	return s.presence.OnlinePeerIDs(ctx)
}

// Heartbeat refreshes a peer's presence TTL.
func (s *PeerService) Heartbeat(ctx context.Context, peerID string) error {
	_, err := s.repo.FindByID(ctx, peerID)
	if err != nil {
		return err
	}
	return s.presence.Heartbeat(ctx, peerID, DefaultPresenceTTL)
}

// HeartbeatWithMetadata refreshes presence and optionally updates mutable peer fields
// reported by daemon heartbeat (e.g., multiaddrs, display name).
// Used to keep discovery addresses fresh between full registrations.
// displayName nil leaves the stored name alone; an explicit empty string clears it
// (identity reset from the portal); a non-empty name follows models.ResolveDisplayName,
// so a daemon placeholder never overwrites a real name the tracker or the owner set.
//
// The returned string is the display name the tracker holds for the peer after
// the update (the public one). The daemon adopts it when its own name is still
// a placeholder, so a name set on the tracker side (a token claim naming the
// agent <symbol>_agent, an operator backfill) shows up in Settings > Identity too.
func (s *PeerService) HeartbeatWithMetadata(ctx context.Context, peerID string, multiaddrs []string, displayName *string) (string, error) {
	peer, err := s.repo.FindByID(ctx, peerID)
	if err != nil {
		// Backward compatibility: if API key exists but peer row is missing in tests/legacy paths,
		// preserve old heartbeat behavior (presence-only).
		if err == models.ErrNotFound {
			if s.presence == nil {
				return "", nil
			}
			return "", s.presence.Heartbeat(ctx, peerID, DefaultPresenceTTL)
		}
		return "", err
	}

	if len(multiaddrs) > 0 {
		peer.Multiaddrs = multiaddrs
	}
	clearName := false
	if displayName != nil {
		if *displayName == "" {
			clearName = true
		} else {
			peer.DisplayName = models.ResolveDisplayName(peer.DisplayName, *displayName)
		}
	}
	peer.LastSeen = time.Now()

	if err := s.repo.Upsert(ctx, peer); err != nil {
		return "", err
	}
	// Upsert keeps the stored name when the new one is empty; clearing is explicit.
	storedName := peer.DisplayName
	if clearName {
		if err := s.repo.UpdateDisplayName(ctx, peerID, ""); err != nil && err != models.ErrNotFound {
			return "", err
		}
		storedName = ""
	}
	if s.presence == nil {
		return storedName, nil
	}
	return storedName, s.presence.Heartbeat(ctx, peerID, DefaultPresenceTTL)
}

// Goodbye marks the peer as offline (e.g. on graceful daemon shutdown). Calls presence.Remove.
func (s *PeerService) Goodbye(ctx context.Context, peerID string) error {
	return s.presence.Remove(ctx, peerID)
}

// UpdateWalletAddress sets the peer's linked Solana wallet address (from frontend connect-wallet flow).
// Wallet is stored in account_wallets; account is created if it does not exist.
func (s *PeerService) UpdateWalletAddress(ctx context.Context, peerID, walletAddress string) error {
	if s.accountRepo == nil || s.walletRepo == nil {
		return fmt.Errorf("wallet service not configured")
	}

	account, created, err := s.accountRepo.GetOrCreateForPeer(ctx, peerID)
	if err != nil {
		return fmt.Errorf("get or create account: %w", err)
	}

	if created && s.creditRepo != nil {
		now := time.Now().UTC()
		expiresAt := now.Add(30 * 24 * time.Hour)
		err := s.creditRepo.CreateBalance(ctx, &models.CreditBalance{
			AccountID:            account.ID,
			FreeCreditsExpiresAt: &expiresAt,
			UpdatedAt:            now,
		})
		if err != nil {
			// If balance already exists (race condition), that's acceptable
			if !errors.Is(err, models.ErrAlreadyExists) {
				return fmt.Errorf("create balance: %w", err)
			}
			// ErrAlreadyExists is fine - balance was created by concurrent request
		}
	}

	if err := s.walletRepo.UpsertWalletForDisplay(ctx, account.ID, walletAddress, "solana"); err != nil {
		return fmt.Errorf("upsert wallet: %w", err)
	}

	return nil
}
