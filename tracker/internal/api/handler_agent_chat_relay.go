// Package api: Agent chat relay — holder sends to tracker; owner's daemon picks up and responds with local LLM.
// Enables "each user uses their own LLM via daemon in their local" for token chat.

package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"github.com/stonkagents/agent/pkg/chatthread"
	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/repository"
	"github.com/stonkagents/agent/tracker/internal/services"
)

// AgentChatRelayStore holds pending chat requests and responses (in-memory; replace with Redis/DB for production).
type AgentChatRelayStore struct {
	mu      sync.RWMutex
	byID    map[string]*agentChatRelayRequest
	byOwner map[string][]string // owner_peer_id -> request_ids
}

type agentChatRelayRequest struct {
	RequestID            string           `json:"request_id"`
	OwnerPeerID          string           `json:"owner_peer_id"`
	HolderPeerID         string           `json:"holder_peer_id"`
	TokenContractAddress string           `json:"token_contract_address"`
	HolderUserID         string           `json:"holder_user_id"`
	Messages             []messagePayload `json:"messages"`
	SessionID            string           `json:"session_id,omitempty"`
	Status               string           `json:"status"` // "pending", "completed", or "expired"
	Response             string           `json:"response,omitempty"`
	CreatedAt            time.Time        `json:"created_at"`
	CompletedAt          time.Time        `json:"completed_at,omitempty"`
	ExpiresAt            time.Time        `json:"expires_at"`
}

const (
	relayRequestTTL        = 3 * time.Minute
	relayKeepAfterComplete = 10 * time.Minute
)

type messagePayload struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func NewAgentChatRelayStore() *AgentChatRelayStore {
	return &AgentChatRelayStore{
		byID:    make(map[string]*agentChatRelayRequest),
		byOwner: make(map[string][]string),
	}
}

func (s *AgentChatRelayStore) Add(req *agentChatRelayRequest) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked(time.Now().UTC())
	s.byID[req.RequestID] = req
	s.byOwner[req.OwnerPeerID] = append(s.byOwner[req.OwnerPeerID], req.RequestID)
}

func (s *AgentChatRelayStore) GetByID(id string) *agentChatRelayRequest {
	s.mu.RLock()
	defer s.mu.RUnlock()
	req := s.byID[id]
	if req == nil {
		return nil
	}
	now := time.Now().UTC()
	if req.Status == "pending" && now.After(req.ExpiresAt) {
		// Read path keeps behavior simple for callers; expiry transition happens in write paths.
		cp := *req
		cp.Status = "expired"
		return &cp
	}
	return req
}

func (s *AgentChatRelayStore) ListPendingByOwner(ownerPeerID string) []*agentChatRelayRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	s.cleanupLocked(now)
	ids := s.byOwner[ownerPeerID]
	var out []*agentChatRelayRequest
	for _, id := range ids {
		if r, ok := s.byID[id]; ok && r.Status == "pending" && !now.After(r.ExpiresAt) {
			out = append(out, r)
		}
	}
	return out
}

func (s *AgentChatRelayStore) Complete(requestID, response, ownerPeerID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	s.cleanupLocked(now)
	r, ok := s.byID[requestID]
	if !ok || r.Status != "pending" || r.OwnerPeerID != ownerPeerID || now.After(r.ExpiresAt) {
		return false
	}
	r.Status = "completed"
	r.Response = response
	r.CompletedAt = now
	return true
}

func (s *AgentChatRelayStore) cleanupLocked(now time.Time) {
	for id, req := range s.byID {
		if req.Status == "pending" && now.After(req.ExpiresAt) {
			req.Status = "expired"
			req.CompletedAt = now
		}
		if req.Status == "completed" && !req.CompletedAt.IsZero() && now.Sub(req.CompletedAt) > relayKeepAfterComplete {
			delete(s.byID, id)
		}
		if req.Status == "expired" && !req.CompletedAt.IsZero() && now.Sub(req.CompletedAt) > relayKeepAfterComplete {
			delete(s.byID, id)
		}
	}
	for ownerPeerID, ids := range s.byOwner {
		filtered := ids[:0]
		for _, id := range ids {
			if _, ok := s.byID[id]; ok {
				filtered = append(filtered, id)
			}
		}
		if len(filtered) == 0 {
			delete(s.byOwner, ownerPeerID)
			continue
		}
		s.byOwner[ownerPeerID] = filtered
	}
}

// AgentChatRelayHandler handles forward, pending, response, result for token chat relay, and owner lookup for direct P2P.
type AgentChatRelayHandler struct {
	store        *AgentChatRelayStore
	tokenRepo    repository.TokenRepository
	apiKeyRepo   repository.PeerAPIKeyRepository
	accountRepo  repository.AccountRepository
	guestKeyRepo repository.GuestKeyMappingRepository
	historyRepo  repository.AgentChatHistoryRepository
	creditSvc    *services.CreditService
	peerService  *services.PeerService
}

func NewAgentChatRelayHandler(store *AgentChatRelayStore, tokenRepo repository.TokenRepository, apiKeyRepo repository.PeerAPIKeyRepository, accountRepo repository.AccountRepository, guestKeyRepo repository.GuestKeyMappingRepository, historyRepo repository.AgentChatHistoryRepository, creditSvc *services.CreditService, peerService *services.PeerService) *AgentChatRelayHandler {
	return &AgentChatRelayHandler{store: store, tokenRepo: tokenRepo, apiKeyRepo: apiKeyRepo, accountRepo: accountRepo, guestKeyRepo: guestKeyRepo, historyRepo: historyRepo, creditSvc: creditSvc, peerService: peerService}
}

// OwnerInfoDTO is the response for GET /api/v1/agent/chat/owner (holder resolves token → owner for direct libp2p).
type OwnerInfoDTO struct {
	OwnerPeerID        string   `json:"owner_peer_id"`
	OwnerWalletAddress string   `json:"owner_wallet_address,omitempty"`
	Multiaddrs         []string `json:"multiaddrs"`
	Online             bool     `json:"online"`
}

// HandleGetOwner handles GET /api/v1/agent/chat/owner?token_contract_address=... (requires X-API-Key).
func (h *AgentChatRelayHandler) HandleGetOwner(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		SendError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "GET required")
		return
	}
	if peerIDFromRequest(r, h.apiKeyRepo) == "" {
		SendError(w, http.StatusUnauthorized, "UNAUTHORIZED", "X-API-Key required")
		return
	}
	tokenAddr := strings.TrimSpace(r.URL.Query().Get("token_contract_address"))
	if tokenAddr == "" {
		SendError(w, http.StatusBadRequest, "MISSING_FIELDS", "token_contract_address required")
		return
	}
	token, err := h.tokenRepo.GetByContractAddress(r.Context(), tokenAddr)
	if err != nil || token == nil {
		SendError(w, http.StatusNotFound, "TOKEN_NOT_FOUND", "Token not found")
		return
	}
	ownerPeerID := token.PeerID
	if h.peerService == nil {
		SendJSON(w, http.StatusOK, OwnerInfoDTO{OwnerPeerID: ownerPeerID, Multiaddrs: nil, Online: false})
		return
	}
	peer, err := h.peerService.FindByID(r.Context(), ownerPeerID)
	multiaddrs := []string(nil)
	ownerWallet := ""
	if err == nil && peer != nil {
		multiaddrs = peer.Multiaddrs
		if multiaddrs == nil {
			multiaddrs = []string{}
		}
		ownerWallet = strings.TrimSpace(peer.WalletAddress)
	}
	online, _ := h.peerService.IsOnline(r.Context(), ownerPeerID)
	slog.Info("[agent-chat] phase=owner_lookup holder_resolved_token_to_owner",
		"token_contract_address", tokenAddr, "owner_peer_id", ownerPeerID, "online", online)
	SendJSON(w, http.StatusOK, OwnerInfoDTO{OwnerPeerID: ownerPeerID, OwnerWalletAddress: ownerWallet, Multiaddrs: multiaddrs, Online: online})
}

func peerIDFromRequest(r *http.Request, apiKeyRepo repository.PeerAPIKeyRepository) string {
	key := strings.TrimSpace(r.Header.Get("X-API-Key"))
	if key == "" {
		return ""
	}
	peerID, _ := apiKeyRepo.GetByAPIKey(r.Context(), key)
	return peerID
}

// ForwardRequestDTO is the body for POST /api/v1/agent/chat/forward.
type ForwardRequestDTO struct {
	TokenContractAddress string           `json:"token_contract_address"`
	UserID               string           `json:"user_id"`
	Messages             []messagePayload `json:"messages"`
	SessionID            string           `json:"session_id,omitempty"`
}

// ForwardResponseDTO is the response for POST /api/v1/agent/chat/forward.
type ForwardResponseDTO struct {
	RequestID string `json:"request_id"`
}

func generateRequestID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return time.Now().UTC().Format("20060102150405.000") + "-fallback"
	}
	return time.Now().UTC().Format("20060102150405") + "-" + hex.EncodeToString(b)
}

func (h *AgentChatRelayHandler) HandleForward(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		SendError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "POST required")
		return
	}
	holderPeerID := peerIDFromRequest(r, h.apiKeyRepo)
	if holderPeerID == "" {
		SendError(w, http.StatusUnauthorized, "UNAUTHORIZED", "X-API-Key required (holder's daemon)")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		SendError(w, http.StatusBadRequest, "INVALID_REQUEST", "Failed to read body")
		return
	}
	var dto ForwardRequestDTO
	if err := json.Unmarshal(body, &dto); err != nil {
		SendError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid JSON body")
		return
	}
	dto.TokenContractAddress = strings.TrimSpace(dto.TokenContractAddress)
	dto.UserID = strings.TrimSpace(dto.UserID)
	if dto.TokenContractAddress == "" || len(dto.Messages) == 0 || dto.UserID == "" {
		SendError(w, http.StatusBadRequest, "MISSING_FIELDS", "token_contract_address, user_id and messages are required")
		return
	}

	token, err := h.tokenRepo.GetByContractAddress(r.Context(), dto.TokenContractAddress)
	if err != nil || token == nil {
		SendError(w, http.StatusNotFound, "TOKEN_NOT_FOUND", "Token not found")
		return
	}
	ownerPeerID := token.PeerID
	if h.peerService == nil {
		SendError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "peer service unavailable for holder verification")
		return
	}
	holderPeer, err := h.peerService.FindByID(r.Context(), holderPeerID)
	if err != nil || holderPeer == nil {
		SendError(w, http.StatusUnauthorized, "UNAUTHORIZED", "holder peer not found")
		return
	}
	holderWallet := strings.TrimSpace(holderPeer.WalletAddress)
	if holderWallet == "" || !strings.EqualFold(holderWallet, dto.UserID) {
		SendError(w, http.StatusForbidden, "HOLDER_VERIFICATION_FAILED", "holder wallet does not match user_id")
		return
	}
	ownerPeer, err := h.peerService.FindByID(r.Context(), ownerPeerID)
	if err != nil || ownerPeer == nil {
		SendError(w, http.StatusNotFound, "OWNER_NOT_FOUND", "owner peer not found")
		return
	}
	ownerWallet := strings.TrimSpace(ownerPeer.WalletAddress)
	if ownerWallet != "" && strings.EqualFold(ownerWallet, dto.UserID) {
		SendError(w, http.StatusForbidden, "OWNER_SELF_CHAT_NOT_ALLOWED", "owner cannot chat with own token agent")
		return
	}

	requestID := generateRequestID()
	req := &agentChatRelayRequest{
		RequestID:            requestID,
		OwnerPeerID:          ownerPeerID,
		HolderPeerID:         holderPeerID,
		TokenContractAddress: dto.TokenContractAddress,
		HolderUserID:         dto.UserID,
		Messages:             dto.Messages,
		SessionID:            strings.TrimSpace(dto.SessionID),
		Status:               "pending",
		CreatedAt:            time.Now().UTC(),
		ExpiresAt:            time.Now().UTC().Add(relayRequestTTL),
	}
	h.store.Add(req)

	slog.Info("[agent-chat] phase=forward holder_sent_message_to_tracker",
		"request_id", requestID, "holder_peer_id", holderPeerID, "owner_peer_id", ownerPeerID,
		"token_contract_address", dto.TokenContractAddress, "messages_count", len(dto.Messages))
	SendJSON(w, http.StatusOK, ForwardResponseDTO{RequestID: requestID})
}

// PendingItemDTO is one item in GET /api/v1/agent/chat/pending response.
type PendingItemDTO struct {
	RequestID            string           `json:"request_id"`
	TokenContractAddress string           `json:"token_contract_address"`
	HolderUserID         string           `json:"holder_user_id"`
	Messages             []messagePayload `json:"messages"`
	SessionID            string           `json:"session_id,omitempty"`
	CreatedAt            string           `json:"created_at"`
}

func (h *AgentChatRelayHandler) HandlePending(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		SendError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "GET required")
		return
	}
	ownerPeerID := peerIDFromRequest(r, h.apiKeyRepo)
	if ownerPeerID == "" {
		SendError(w, http.StatusUnauthorized, "UNAUTHORIZED", "X-API-Key required (owner's daemon)")
		return
	}

	pending := h.store.ListPendingByOwner(ownerPeerID)
	slog.Info("[agent-chat] phase=pending_poll owner_fetched_pending",
		"owner_peer_id", ownerPeerID, "pending_count", len(pending))
	items := make([]PendingItemDTO, 0, len(pending))
	for _, p := range pending {
		items = append(items, PendingItemDTO{
			RequestID:            p.RequestID,
			TokenContractAddress: p.TokenContractAddress,
			HolderUserID:         p.HolderUserID,
			Messages:             p.Messages,
			SessionID:            p.SessionID,
			CreatedAt:            p.CreatedAt.Format(time.RFC3339),
		})
	}
	type out struct {
		Pending []PendingItemDTO `json:"pending"`
	}
	SendJSON(w, http.StatusOK, out{Pending: items})
}

// ResponseRequestDTO is the body for POST /api/v1/agent/chat/response.
type ResponseRequestDTO struct {
	RequestID string `json:"request_id"`
	Response  string `json:"response"`
}

func (h *AgentChatRelayHandler) HandleResponse(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		SendError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "POST required")
		return
	}
	ownerPeerID := peerIDFromRequest(r, h.apiKeyRepo)
	if ownerPeerID == "" {
		SendError(w, http.StatusUnauthorized, "UNAUTHORIZED", "X-API-Key required (owner's daemon)")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 256*1024))
	if err != nil {
		SendError(w, http.StatusBadRequest, "INVALID_REQUEST", "Failed to read body")
		return
	}
	var dto ResponseRequestDTO
	if err := json.Unmarshal(body, &dto); err != nil {
		SendError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid JSON body")
		return
	}
	dto.RequestID = strings.TrimSpace(dto.RequestID)
	if dto.RequestID == "" {
		SendError(w, http.StatusBadRequest, "MISSING_REQUEST_ID", "request_id required")
		return
	}

	if !h.store.Complete(dto.RequestID, dto.Response, ownerPeerID) {
		SendError(w, http.StatusNotFound, "NOT_FOUND", "Request not found or already completed")
		return
	}
	slog.Info("[agent-chat] phase=response_submitted owner_posted_response",
		"request_id", dto.RequestID, "owner_peer_id", ownerPeerID, "response_length", len(dto.Response))
	w.WriteHeader(http.StatusNoContent)
}

// ResultResponseDTO is the response for GET /api/v1/agent/chat/result/:request_id.
type ResultResponseDTO struct {
	Status          string `json:"status"`
	Response        string `json:"response,omitempty"`
	CreditsDeducted int    `json:"credits_deducted,omitempty"`
}

type AgentChatHistoryAppendRequestDTO struct {
	UserID       string `json:"user_id"`
	TokenAddress string `json:"token_address"`
	Messages     []struct {
		Role            string `json:"role"`
		Content         string `json:"content"`
		AgentID         string `json:"agent_id,omitempty"`
		CreditsDeducted int    `json:"credits_deducted,omitempty"`
	} `json:"messages"`
}

const (
	agentChatRelayCost   = 10
	agentChatRelayReason = "agent_chat_completion"
)

func (h *AgentChatRelayHandler) HandleResult(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		SendError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "GET required")
		return
	}
	holderPeerID := peerIDFromRequest(r, h.apiKeyRepo)
	if holderPeerID == "" {
		SendError(w, http.StatusUnauthorized, "UNAUTHORIZED", "X-API-Key required")
		return
	}

	vars := mux.Vars(r)
	requestID := strings.TrimSpace(vars["request_id"])
	if requestID == "" {
		SendError(w, http.StatusBadRequest, "MISSING_REQUEST_ID", "request_id path required")
		return
	}

	req := h.store.GetByID(requestID)
	if req == nil {
		SendError(w, http.StatusNotFound, "NOT_FOUND", "Request not found")
		return
	}
	if req.HolderPeerID != holderPeerID {
		SendError(w, http.StatusForbidden, "FORBIDDEN", "Not your request")
		return
	}

	if req.Status == "completed" {
		charged := 0
		if h.creditSvc != nil && h.accountRepo != nil {
			accountID, err := ResolveAgentAccountID(r.Context(), r, h.apiKeyRepo, h.accountRepo, h.guestKeyRepo)
			if err != nil || accountID == "" {
				SendError(w, http.StatusUnauthorized, "UNAUTHORIZED", "Invalid or missing API key")
				return
			}
			spendReqID := "agent-chat:" + requestID
			if err := h.creditSvc.Spend(r.Context(), accountID, agentChatRelayCost, agentChatRelayReason, spendReqID); err != nil {
				if err == models.ErrInsufficientCredits {
					SendError(w, http.StatusPaymentRequired, "INSUFFICIENT_CREDITS", "Not enough credits")
					return
				}
				SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to deduct credits")
				return
			}
			charged = agentChatRelayCost
		}
		slog.Info("[agent-chat] phase=result_poll holder_got_completed",
			"request_id", requestID, "holder_peer_id", holderPeerID, "owner_peer_id", req.OwnerPeerID)
		SendJSON(w, http.StatusOK, ResultResponseDTO{Status: "completed", Response: req.Response, CreditsDeducted: charged})
		return
	}
	if req.Status == "expired" {
		SendError(w, http.StatusGone, "EXPIRED", "Request expired before owner response")
		return
	}
	slog.Debug("[agent-chat] phase=result_poll holder_polling_pending", "request_id", requestID, "holder_peer_id", holderPeerID)
	SendJSON(w, http.StatusAccepted, ResultResponseDTO{Status: "pending"})
}

// HandleAppendHistory persists token chat messages keyed by user_id + token_address.
func (h *AgentChatRelayHandler) HandleAppendHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		SendError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "POST required")
		return
	}
	if peerIDFromRequest(r, h.apiKeyRepo) == "" {
		SendError(w, http.StatusUnauthorized, "UNAUTHORIZED", "X-API-Key required")
		return
	}
	if h.historyRepo == nil {
		SendError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "History repository unavailable")
		return
	}
	var dto AgentChatHistoryAppendRequestDTO
	if err := json.NewDecoder(io.LimitReader(r.Body, 512*1024)).Decode(&dto); err != nil {
		SendError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid JSON body")
		return
	}
	dto.UserID = strings.TrimSpace(dto.UserID)
	dto.TokenAddress = strings.TrimSpace(dto.TokenAddress)
	if dto.UserID == "" {
		SendError(w, http.StatusBadRequest, "MISSING_FIELDS", "user_id is required")
		return
	}
	// Personal chat: omit token_address or use sentinel chatthread.PersonalContextTokenAddress ("0x").
	if dto.TokenAddress == "" {
		dto.TokenAddress = chatthread.PersonalContextTokenAddress
	}
	msgs := make([]*repository.AgentChatHistoryMessage, 0, len(dto.Messages))
	for _, m := range dto.Messages {
		role := strings.ToLower(strings.TrimSpace(m.Role))
		if role != "system" && role != "user" && role != "assistant" {
			role = "user"
		}
		content := strings.TrimSpace(m.Content)
		if content == "" {
			continue
		}
		msgs = append(msgs, &repository.AgentChatHistoryMessage{
			Role:            role,
			Content:         content,
			AgentID:         strings.TrimSpace(m.AgentID),
			CreditsDeducted: m.CreditsDeducted,
		})
	}
	if err := h.historyRepo.AppendMessages(r.Context(), dto.UserID, dto.TokenAddress, msgs); err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to persist history")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// HandleGetHistory returns token chat history from Postgres keyed by user_id + token_address.
func (h *AgentChatRelayHandler) HandleGetHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		SendError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "GET required")
		return
	}
	if peerIDFromRequest(r, h.apiKeyRepo) == "" {
		SendError(w, http.StatusUnauthorized, "UNAUTHORIZED", "X-API-Key required")
		return
	}
	if h.historyRepo == nil {
		SendError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "History repository unavailable")
		return
	}
	userID := strings.TrimSpace(r.URL.Query().Get("user_id"))
	tokenAddress := strings.TrimSpace(r.URL.Query().Get("token_address"))
	if userID == "" {
		SendError(w, http.StatusBadRequest, "MISSING_FIELDS", "user_id is required")
		return
	}
	outToken := tokenAddress
	if tokenAddress == "" {
		outToken = chatthread.PersonalContextTokenAddress
	}
	msgs, err := h.historyRepo.ListMessages(r.Context(), userID, tokenAddress)
	if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to load history")
		return
	}
	SendJSON(w, http.StatusOK, map[string]interface{}{
		"user_id":       userID,
		"token_address": outToken,
		"messages":      msgs,
	})
}

// HandleListPersonalSessions returns at most one session row for personal agent chat (tracker-backed), if any messages exist.
func (h *AgentChatRelayHandler) HandleListPersonalSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		SendError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "GET required")
		return
	}
	if peerIDFromRequest(r, h.apiKeyRepo) == "" {
		SendError(w, http.StatusUnauthorized, "UNAUTHORIZED", "X-API-Key required")
		return
	}
	if h.historyRepo == nil {
		SendError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "History repository unavailable")
		return
	}
	userID := strings.TrimSpace(r.URL.Query().Get("user_id"))
	if userID == "" {
		SendError(w, http.StatusBadRequest, "MISSING_FIELDS", "user_id is required")
		return
	}
	has, firstAt, lastAt, err := h.historyRepo.PersonalSessionBounds(r.Context(), userID)
	if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to list sessions")
		return
	}
	if !has {
		SendJSON(w, http.StatusOK, map[string]interface{}{"sessions": []any{}})
		return
	}
	sid := chatthread.StableID(userID, "")
	SendJSON(w, http.StatusOK, map[string]interface{}{
		"sessions": []map[string]interface{}{
			{
				"id":             sid,
				"user_id":        userID,
				"context_id":     chatthread.PersonalContextTokenAddress,
				"system_prompt":  "",
				"created_at":     firstAt.UTC().Format(time.RFC3339),
				"updated_at":     lastAt.UTC().Format(time.RFC3339),
				"token_address":  chatthread.PersonalContextTokenAddress,
				"session_source": "tracker_personal",
			},
		},
	})
}
