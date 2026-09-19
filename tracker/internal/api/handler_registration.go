// Package: tracker/internal/api
// Feature: F-013 (Credits & Identity)
// Story: US-013-01 (Account Registration)
// Purpose: HTTP handlers for challenge-response registration

package api

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"

	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/services"
)

// sanitizeDisplayName strips HTML angle brackets and control characters, trims
// whitespace, and truncates to 50 characters (runes). See models.SanitizeDisplayName;
// the daemon enforces the same limit before sending (internal/daemon/setup_identity.go).
func sanitizeDisplayName(s string) string { return models.SanitizeDisplayName(s) }

// ChallengeDTO is the request body for POST /api/v1/tracker/challenge.
type ChallengeDTO struct {
	PeerID string `json:"peer_id"`
}

// ChallengeResponseDTO is the response for a successful challenge.
type ChallengeResponseDTO struct {
	Nonce     string `json:"nonce"`
	ExpiresIn int    `json:"expires_in"`
}

// RegisterChallengeDTO is the request body for POST /api/v1/tracker/register (challenge-response).
type RegisterChallengeDTO struct {
	PeerID        string   `json:"peer_id"`
	Ed25519Pubkey string   `json:"ed25519_pubkey"`
	Signature     string   `json:"signature"`
	Multiaddrs    []string `json:"multiaddrs"`
	ClientVersion string   `json:"client_version"`
	DisplayName   string   `json:"display_name,omitempty"` // F-032: user-chosen name from daemon config
}

// RegisterResponseDTO is the response for a successful registration.
type RegisterResponseDTO struct {
	APIKey    string           `json:"api_key,omitempty"`
	AccountID string           `json:"account_id"`
	Credits   CreditSummaryDTO `json:"credits"`
}

// CreditSummaryDTO shows free/paid balances.
type CreditSummaryDTO struct {
	Free int `json:"free"`
	Paid int `json:"paid"`
}

// RegistrationHandler handles challenge-response registration endpoints.
type RegistrationHandler struct {
	service *services.RegistrationService
}

// NewRegistrationHandler creates a new RegistrationHandler.
func NewRegistrationHandler(svc *services.RegistrationService) *RegistrationHandler {
	return &RegistrationHandler{service: svc}
}

// HandleChallenge handles POST /api/v1/tracker/challenge.
func (h *RegistrationHandler) HandleChallenge(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		SendError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Only POST allowed")
		return
	}

	limitedBody := io.LimitReader(r.Body, 4<<10)
	var dto ChallengeDTO
	if err := json.NewDecoder(limitedBody).Decode(&dto); err != nil {
		SendError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid JSON body")
		return
	}

	if dto.PeerID == "" {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Missing required field: peer_id")
		return
	}

	clientIP := extractClientIP(r)
	resp, err := h.service.Challenge(r.Context(), services.ChallengeRequest{
		PeerID:   dto.PeerID,
		ClientIP: clientIP,
	})
	if err == services.ErrIPBlocked {
		SendError(w, http.StatusForbidden, "IP_BLOCKED", "Registration blocked for this IP")
		return
	}
	if err == models.ErrInvalidInput {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Missing required field: peer_id")
		return
	}
	if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to generate challenge")
		return
	}

	SendJSON(w, http.StatusOK, ChallengeResponseDTO{
		Nonce:     base64.StdEncoding.EncodeToString(resp.Nonce),
		ExpiresIn: resp.ExpiresIn,
	})
}

// HandleRegister handles POST /api/v1/tracker/register (challenge-response flow).
func (h *RegistrationHandler) HandleRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		SendError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Only POST allowed")
		return
	}

	limitedBody := io.LimitReader(r.Body, 10<<20)
	var dto RegisterChallengeDTO
	if err := json.NewDecoder(limitedBody).Decode(&dto); err != nil {
		SendError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid JSON body")
		return
	}

	// Sanitize display_name: strip HTML, trim whitespace, truncate to 50 chars
	dto.DisplayName = sanitizeDisplayName(dto.DisplayName)

	// Decode base64 fields
	pubkey, err := base64.StdEncoding.DecodeString(dto.Ed25519Pubkey)
	if err != nil {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Invalid base64 in ed25519_pubkey")
		return
	}
	sig, err := base64.StdEncoding.DecodeString(dto.Signature)
	if err != nil {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Invalid base64 in signature")
		return
	}

	clientIP := extractClientIP(r)
	resp, err := h.service.Register(r.Context(), services.RegisterRequest{
		PeerID:        dto.PeerID,
		Ed25519Pubkey: pubkey,
		Signature:     sig,
		Multiaddrs:    dto.Multiaddrs,
		ClientVersion: dto.ClientVersion,
		DisplayName:   dto.DisplayName,
		ClientIP:      clientIP,
	})
	if err == services.ErrIPBlocked {
		SendError(w, http.StatusForbidden, "IP_BLOCKED", "Registration blocked for this IP")
		return
	}
	if err == services.ErrInvalidSignature {
		SendError(w, http.StatusUnauthorized, "INVALID_SIGNATURE", "Ed25519 signature verification failed")
		return
	}
	if err == models.ErrNonceExpired {
		SendError(w, http.StatusUnauthorized, "NONCE_EXPIRED", "Challenge nonce has expired. Request a new challenge.")
		return
	}
	if err == models.ErrNotFound {
		SendError(w, http.StatusUnauthorized, "NO_CHALLENGE", "No active challenge for this peer. Request a challenge first.")
		return
	}
	if err == models.ErrInvalidInput {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Missing required fields")
		return
	}
	if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to register peer")
		return
	}

	SendJSON(w, http.StatusCreated, RegisterResponseDTO{
		APIKey:    resp.APIKey,
		AccountID: resp.AccountID,
		Credits: CreditSummaryDTO{
			Free: resp.Credits.Free,
			Paid: resp.Credits.Paid,
		},
	})
}

// extractClientIP extracts the client IP from the request, checking proxy headers.
func extractClientIP(r *http.Request) string {
	// Use the existing extractIPFromRequest which checks X-Forwarded-For
	return extractIPFromRequest(r)
}

// extractIPOnly strips the port from a host:port string.
func extractIPOnly(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}
