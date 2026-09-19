// Package: tracker/internal/api
// Feature: F-007 (Centralized Tracker)
// Story: US-007-02 (Peer Registry with Redis Presence Tracking)
// Purpose: HTTP handlers for peer registration and discovery

package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/gorilla/mux"
	"github.com/stonkagents/agent/tracker/internal/geo"
	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/presence"
	"github.com/stonkagents/agent/tracker/internal/repository"
	"github.com/stonkagents/agent/tracker/internal/services"
)

// RegisterPeerDTO is the request body for peer registration.
type RegisterPeerDTO struct {
	PeerID     string   `json:"peer_id"`
	PublicKey  string   `json:"ed25519_pubkey"`
	Multiaddrs []string `json:"multiaddrs"`
}

// PeerResponseDTO is the response for peer data (list) and register.
type PeerResponseDTO struct {
	PeerID     string   `json:"peer_id"`
	PublicKey  string   `json:"ed25519_pubkey"`
	Multiaddrs []string `json:"multiaddrs"`
	FirstSeen  string   `json:"first_seen"`
	LastSeen   string   `json:"last_seen"`
	Online     bool     `json:"online,omitempty"`  // True if last_seen within TTL (list only; register omits)
	Trusted    bool     `json:"trusted,omitempty"` // True if actor (from X-API-Key) has trusted this peer
	APIKey     string   `json:"api_key,omitempty"` // Only on first register; client must store it
}

// PeerDetailDTO is the response for single peer (GET /peers/{peer_id}); includes stats and location.
type PeerDetailDTO struct {
	PeerID       string `json:"peer_id"`
	MaskedPeerID string `json:"masked_peer_id,omitempty"`
	// DisplayName is the owner-set agent name; omitted when unset.
	DisplayName             string   `json:"display_name,omitempty"`
	PublicKey               string   `json:"ed25519_pubkey"`
	Multiaddrs              []string `json:"multiaddrs"`
	FirstSeen               string   `json:"first_seen"`
	LastSeen                string   `json:"last_seen"`
	Country                 string   `json:"country,omitempty"`
	Region                  string   `json:"region,omitempty"`
	TotalUploadBytes        int64    `json:"total_upload_bytes"`
	TotalDownloadBytes      int64    `json:"total_download_bytes"`
	AverageSpeedBytesPerSec *int64   `json:"average_speed_bytes_per_sec,omitempty"`
	Online                  bool     `json:"online"`
}

// GoodbyeDTO is the request body for POST /goodbye (graceful shutdown).
type GoodbyeDTO struct {
	PeerID string `json:"peer_id"`
}

// UpdateMyWalletDTO is the request body for PATCH /peers/me (link Solana wallet to current peer).
type UpdateMyWalletDTO struct {
	WalletAddress string `json:"wallet_address"`
}

// PeerHandler handles peer-related HTTP endpoints.
type PeerHandler struct {
	service        *services.PeerService
	trustBlockRepo repository.PeerTrustBlockRepository
	apiKeyRepo     repository.PeerAPIKeyRepository
	presenceStore  presence.PresenceStore
	versionChecker VersionProvider // F-025: latest release version for heartbeat update fields (nil = no update checks)
	geoResolver    geo.Resolver    // GeoIP lookup for peer registration (nil = StubResolver fallback)
	// autopilotRepo stores the autopilot categories reported on heartbeats (phase 2; nil = ignored).
	autopilotRepo repository.PeerAutopilotRepository
	// autopilotRows remembers, per peer id, whether a peer_autopilot row exists (bool), so a
	// heartbeat without autopilot_categories does not delete on every beat.
	autopilotRows sync.Map
}

// SetAutopilotRepo enables storing the heartbeat's autopilot_categories (request routing).
func (h *PeerHandler) SetAutopilotRepo(repo repository.PeerAutopilotRepository) {
	h.autopilotRepo = repo
}

// NewPeerHandler creates a new PeerHandler.
func NewPeerHandler(svc *services.PeerService) *PeerHandler {
	return &PeerHandler{service: svc}
}

// NewPeerHandlerWithTrustBlock creates a PeerHandler with trust/block support.
func NewPeerHandlerWithTrustBlock(svc *services.PeerService, trustBlockRepo repository.PeerTrustBlockRepository) *PeerHandler {
	return &PeerHandler{service: svc, trustBlockRepo: trustBlockRepo}
}

// SetAPIKeyRepo sets the API key repo (for trusted-by-actor lookup in Discover).
func (h *PeerHandler) SetAPIKeyRepo(repo repository.PeerAPIKeyRepository) {
	h.apiKeyRepo = repo
}

// SetPresenceStore sets the presence store (for heartbeat handler).
func (h *PeerHandler) SetPresenceStore(store presence.PresenceStore) {
	h.presenceStore = store
}

// SetVersionChecker sets the version provider for heartbeat update fields (F-025).
func (h *PeerHandler) SetVersionChecker(vc VersionProvider) {
	h.versionChecker = vc
}

// SetGeoResolver sets the GeoIP resolver for peer registration (country/region from IP).
func (h *PeerHandler) SetGeoResolver(resolver geo.Resolver) {
	h.geoResolver = resolver
}

// HandleRegister handles POST /api/v1/tracker/register.
func (h *PeerHandler) HandleRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		SendError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Only POST allowed")
		return
	}

	// Parse request body with size limit (10MB max)
	limitedBody := io.LimitReader(r.Body, 10<<20)
	var dto RegisterPeerDTO
	if err := json.NewDecoder(limitedBody).Decode(&dto); err != nil {
		SendError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid JSON body")
		return
	}

	// Inject observed external IP from request RemoteAddr as a best-effort hint.
	// When peers are behind NAT, they only send private multiaddrs.
	// The tracker can see the peer's public IP and synthesize an additional multiaddr.
	dto.Multiaddrs = injectObservedAddr(r, dto.PeerID, dto.Multiaddrs)

	// Perform GeoIP lookup for location (uses ipdata.co when configured, else stub)
	remoteIP := extractIPFromRequest(r)
	resolver := h.geoResolver
	if resolver == nil {
		resolver = &geo.StubResolver{}
	}
	lookupResult, _ := resolver.Lookup(remoteIP)
	country, region := "", ""
	if lookupResult != nil {
		country = lookupResult.Country
		region = lookupResult.City // City used as region; ipdata region_name can be added later if needed
	}

	peer, err := h.service.Register(r.Context(), services.RegisterPeerRequest{
		PeerID:     dto.PeerID,
		PublicKey:  dto.PublicKey,
		Multiaddrs: dto.Multiaddrs,
		Country:    country,
		Region:     region,
	})
	if err == models.ErrInvalidInput {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Missing required fields: peer_id, ed25519_pubkey")
		return
	}
	if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to register peer")
		return
	}

	SendJSON(w, http.StatusCreated, PeerResponseDTO{
		PeerID:     peer.PeerID,
		PublicKey:  peer.PublicKey,
		Multiaddrs: peer.Multiaddrs,
		FirstSeen:  peer.FirstSeen.Format("2006-01-02T15:04:05Z"),
		LastSeen:   peer.LastSeen.Format("2006-01-02T15:04:05Z"),
		APIKey:     peer.APIKey, // Only set on first register; omitempty hides on subsequent
	})
}

// HandleGetPeer handles GET /api/v1/tracker/peers/{peer_id}. Returns single peer info for frontend (profile/detail).
func (h *PeerHandler) HandleGetPeer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		SendError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Only GET allowed")
		return
	}
	peerID := mux.Vars(r)["peer_id"]
	if peerID == "" {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Missing peer_id")
		return
	}

	peer, err := h.service.FindByID(r.Context(), peerID)
	if err == models.ErrNotFound {
		SendError(w, http.StatusNotFound, "NOT_FOUND", "Peer not found")
		return
	}
	if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to get peer")
		return
	}

	online, _ := h.service.IsOnline(r.Context(), peerID)

	SendJSON(w, http.StatusOK, PeerDetailDTO{
		PeerID:                  peer.PeerID,
		MaskedPeerID:            peer.MaskedPeerID,
		DisplayName:             peer.DisplayName,
		PublicKey:               peer.PublicKey,
		Multiaddrs:              peer.Multiaddrs,
		FirstSeen:               peer.FirstSeen.Format("2006-01-02T15:04:05Z"),
		LastSeen:                peer.LastSeen.Format("2006-01-02T15:04:05Z"),
		Country:                 peer.Country,
		Region:                  peer.Region,
		TotalUploadBytes:        peer.TotalUploadBytes,
		TotalDownloadBytes:      peer.TotalDownloadBytes,
		AverageSpeedBytesPerSec: peer.AverageSpeedBytesPerSec,
		Online:                  online,
	})
}

// HandleDiscover handles GET /api/v1/tracker/peers.
func (h *PeerHandler) HandleDiscover(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		SendError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Only GET allowed")
		return
	}

	limit := 20
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 {
			limit = parsed
		}
	}

	onlineOnly := r.URL.Query().Get("online") == "true"

	peers, err := h.service.Discover(r.Context(), services.DiscoverOptions{Limit: limit, OnlineOnly: onlineOnly})
	if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to discover peers")
		return
	}

	// Resolve online set so each peer in the list shows online/offline correctly
	onlineIDs, _ := h.service.OnlinePeerIDs(r.Context())
	onlineSet := make(map[string]bool, len(onlineIDs))
	for _, id := range onlineIDs {
		onlineSet[id] = true
	}

	// Resolve trusted set if actor has API key
	trustedSet := make(map[string]bool)
	if h.trustBlockRepo != nil {
		if key := strings.TrimSpace(r.Header.Get("X-API-Key")); key != "" && h.apiKeyRepo != nil {
			if actorID, err := h.apiKeyRepo.GetByAPIKey(r.Context(), key); err == nil && actorID != "" {
				if ids, err := h.trustBlockRepo.TrustedByActor(r.Context(), actorID); err == nil {
					for _, id := range ids {
						trustedSet[id] = true
					}
				}
			}
		}
	}

	dtos := make([]PeerResponseDTO, len(peers))
	for i, p := range peers {
		dtos[i] = PeerResponseDTO{
			PeerID:     p.PeerID,
			PublicKey:  p.PublicKey,
			Multiaddrs: p.Multiaddrs,
			FirstSeen:  p.FirstSeen.Format("2006-01-02T15:04:05Z"),
			LastSeen:   p.LastSeen.Format("2006-01-02T15:04:05Z"),
			Online:     onlineSet[p.PeerID],
			Trusted:    trustedSet[p.PeerID],
		}
	}

	SendList(w, dtos, len(dtos))
}

// HandleGoodbye handles POST /api/v1/tracker/goodbye. Marks the peer as offline (graceful shutdown).
func (h *PeerHandler) HandleGoodbye(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		SendError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Only POST allowed")
		return
	}
	limitedBody := io.LimitReader(r.Body, 4<<10)
	var dto GoodbyeDTO
	if err := json.NewDecoder(limitedBody).Decode(&dto); err != nil {
		SendError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid JSON body")
		return
	}
	if dto.PeerID == "" {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Missing required field: peer_id")
		return
	}
	if err := h.service.Goodbye(r.Context(), dto.PeerID); err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to mark peer offline")
		return
	}
	SendJSON(w, http.StatusOK, map[string]interface{}{"peer_id": dto.PeerID, "status": "offline"})
}

// HandleTrust handles POST /api/v1/tracker/peers/{peer_id}/trust. Requires X-API-Key.
func (h *PeerHandler) HandleTrust(w http.ResponseWriter, r *http.Request) {
	if h.trustBlockRepo == nil {
		SendError(w, http.StatusNotImplemented, "NOT_IMPLEMENTED", "Trust not configured")
		return
	}
	if r.Method != http.MethodPost {
		SendError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Only POST allowed")
		return
	}
	actorID := ForumPeerIDFromContext(r.Context())
	if actorID == "" {
		SendError(w, http.StatusUnauthorized, "UNAUTHORIZED", "API key required")
		return
	}
	targetID := mux.Vars(r)["peer_id"]
	if targetID == "" {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Missing peer_id")
		return
	}
	if actorID == targetID {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Cannot trust self")
		return
	}
	if err := h.trustBlockRepo.Trust(r.Context(), actorID, targetID); err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to trust peer")
		return
	}
	peer, err := h.service.FindByID(r.Context(), targetID)
	if err != nil {
		SendJSON(w, http.StatusOK, map[string]interface{}{"success": true, "peer_id": targetID})
		return
	}
	online, _ := h.service.IsOnline(r.Context(), targetID)
	SendJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"peer": PeerResponseDTO{
			PeerID:     peer.PeerID,
			PublicKey:  peer.PublicKey,
			Multiaddrs: peer.Multiaddrs,
			FirstSeen:  peer.FirstSeen.Format("2006-01-02T15:04:05Z"),
			LastSeen:   peer.LastSeen.Format("2006-01-02T15:04:05Z"),
			Online:     online,
		},
	})
}

// HandleBlock handles POST /api/v1/tracker/peers/{peer_id}/block. Requires X-API-Key.
func (h *PeerHandler) HandleBlock(w http.ResponseWriter, r *http.Request) {
	if h.trustBlockRepo == nil {
		SendError(w, http.StatusNotImplemented, "NOT_IMPLEMENTED", "Block not configured")
		return
	}
	if r.Method != http.MethodPost {
		SendError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Only POST allowed")
		return
	}
	actorID := ForumPeerIDFromContext(r.Context())
	if actorID == "" {
		SendError(w, http.StatusUnauthorized, "UNAUTHORIZED", "API key required")
		return
	}
	targetID := mux.Vars(r)["peer_id"]
	if targetID == "" {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Missing peer_id")
		return
	}
	if actorID == targetID {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Cannot block self")
		return
	}
	if err := h.trustBlockRepo.Block(r.Context(), actorID, targetID); err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to block peer")
		return
	}
	peer, err := h.service.FindByID(r.Context(), targetID)
	if err != nil {
		SendJSON(w, http.StatusOK, map[string]interface{}{"success": true, "peer_id": targetID})
		return
	}
	online, _ := h.service.IsOnline(r.Context(), targetID)
	SendJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"peer": PeerResponseDTO{
			PeerID:     peer.PeerID,
			PublicKey:  peer.PublicKey,
			Multiaddrs: peer.Multiaddrs,
			FirstSeen:  peer.FirstSeen.Format("2006-01-02T15:04:05Z"),
			LastSeen:   peer.LastSeen.Format("2006-01-02T15:04:05Z"),
			Online:     online,
		},
	})
}

// HandleUpdateMyWallet handles PATCH /api/v1/tracker/peers/me. Requires X-API-Key. Links Solana wallet to the current peer.
func (h *PeerHandler) HandleUpdateMyWallet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPatch {
		SendError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "PATCH required")
		return
	}
	peerID := ForumPeerIDFromContext(r.Context())
	if peerID == "" {
		SendError(w, http.StatusUnauthorized, "UNAUTHORIZED", "API key required")
		return
	}
	limitedBody := io.LimitReader(r.Body, 1024)
	var dto UpdateMyWalletDTO
	if err := json.NewDecoder(limitedBody).Decode(&dto); err != nil {
		SendError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid JSON body")
		return
	}
	walletAddress := strings.TrimSpace(dto.WalletAddress)
	if walletAddress == "" {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "wallet_address is required")
		return
	}
	if len(walletAddress) > 64 {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "wallet_address too long")
		return
	}
	if err := h.service.UpdateWalletAddress(r.Context(), peerID, walletAddress); err != nil {
		if err == models.ErrNotFound {
			SendError(w, http.StatusNotFound, "NOT_FOUND", "Peer not found")
			return
		}
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to update wallet")
		return
	}
	SendJSON(w, http.StatusOK, map[string]interface{}{"success": true, "peer_id": peerID, "wallet_address": walletAddress})
}

// injectObservedAddr adds a synthesized multiaddr from the HTTP request's RemoteAddr
// when the peer hasn't sent any public IP multiaddrs. This helps peers behind NAT
// become reachable by other peers on different networks.
func injectObservedAddr(r *http.Request, peerID string, multiaddrs []string) []string {
	remoteIP := extractIPFromRequest(r)
	if remoteIP == "" || !isPublicIP(remoteIP) {
		return multiaddrs
	}
	// If peer already includes relay circuit addresses, avoid synthesizing a direct endpoint.
	// Relay addresses are authoritative for NAT-restricted peers.
	for _, addr := range multiaddrs {
		if strings.Contains(addr, "/p2p-circuit/") {
			return multiaddrs
		}
	}

	// Check if peer already sent a multiaddr containing a public IP
	for _, addr := range multiaddrs {
		if containsPublicIP(addr) {
			return multiaddrs // Already has public addr, no injection needed
		}
	}

	// Extract TCP port — prefer from public-IP addresses, fall back to any addr.
	// Peers behind NAT bind 0.0.0.0:<port> so the listen port from a private addr
	// is the same port that's (potentially) NAT-mapped to the public IP.
	port := extractPortFromPublicMultiaddrs(multiaddrs)
	if port == "" {
		port = extractPortFromMultiaddrs(multiaddrs)
	}
	if port == "" {
		return multiaddrs
	}

	observed := fmt.Sprintf("/ip4/%s/tcp/%s/p2p/%s", remoteIP, port, peerID)
	return append(multiaddrs, observed)
}

// extractIPFromRequest extracts the client IP from the HTTP request.
// Checks X-Forwarded-For first (for proxies/ngrok), then falls back to RemoteAddr.
func extractIPFromRequest(r *http.Request) string {
	// Check X-Forwarded-For (set by reverse proxies like ngrok)
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// X-Forwarded-For can contain multiple IPs; take the first (client IP)
		parts := strings.SplitN(xff, ",", 2)
		ip := strings.TrimSpace(parts[0])
		if net.ParseIP(ip) != nil {
			return ip
		}
	}

	// Fall back to RemoteAddr
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return ""
	}
	if net.ParseIP(host) != nil {
		return host
	}
	return ""
}

// isPublicIP returns true if the IP is a globally routable public IP.
func isPublicIP(ipStr string) bool {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false
	}
	// Check for private/reserved ranges
	privateRanges := []string{
		"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16",
		"127.0.0.0/8", "169.254.0.0/16", "::1/128", "fe80::/10",
		"fc00::/7",
	}
	for _, cidr := range privateRanges {
		_, network, err := net.ParseCIDR(cidr)
		if err != nil {
			continue
		}
		if network.Contains(ip) {
			return false
		}
	}
	return true
}

// containsPublicIP checks if a multiaddr string contains a public IP address.
func containsPublicIP(addr string) bool {
	// Extract IP from multiaddr like "/ip4/1.2.3.4/tcp/5000/p2p/..."
	parts := strings.Split(addr, "/")
	for i, part := range parts {
		if (part == "ip4" || part == "ip6") && i+1 < len(parts) {
			if isPublicIP(parts[i+1]) {
				return true
			}
		}
	}
	return false
}

// extractPortFromMultiaddrs extracts the first TCP port from a list of multiaddrs.
func extractPortFromMultiaddrs(addrs []string) string {
	for _, addr := range addrs {
		parts := strings.Split(addr, "/")
		for i, part := range parts {
			if part == "tcp" && i+1 < len(parts) {
				return parts[i+1]
			}
		}
	}
	return ""
}

// extractPortFromPublicMultiaddrs extracts a TCP port from multiaddrs that already contain
// a public IP. Returns empty string when no public-address port is present.
func extractPortFromPublicMultiaddrs(addrs []string) string {
	for _, addr := range addrs {
		if !containsPublicIP(addr) {
			continue
		}
		if port := extractPortFromMultiaddrs([]string{addr}); port != "" {
			return port
		}
	}
	return ""
}
