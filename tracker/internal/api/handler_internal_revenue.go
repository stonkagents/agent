// Package: tracker/internal/api
// Feature: StonkAgents launchpad (Raydium LaunchLab)
// Purpose: POST /api/internal/revenue — the keeper's write path into the platform revenue
//          ledger, guarded by a shared secret header. Not a public endpoint: when the
//          secret is unset the route is not registered at all and callers get 404.

package api

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/services"
)

const (
	// InternalTokenHeader carries the shared secret for internal write endpoints.
	InternalTokenHeader = "X-Internal-Token"
	// internalRevenueBodyLimit caps the request body (meta is small by contract).
	internalRevenueBodyLimit = 8 * 1024
	// internalRevenueMaxMetaKeys caps the free-form meta object.
	internalRevenueMaxMetaKeys = 32
	// internalRevenueFutureSkew is how far ahead of now occurredAt may be (clock skew).
	internalRevenueFutureSkew = time.Hour
)

// recordRevenueDTO is the body of POST /api/internal/revenue (camelCase, written by the keeper).
type recordRevenueDTO struct {
	Kind       string                 `json:"kind"`
	QuoteMint  string                 `json:"quoteMint"`
	AmountRaw  float64                `json:"amountRaw"`
	AmountUSD  *float64               `json:"amountUsd"`
	Signature  string                 `json:"signature"`
	Mint       string                 `json:"mint"`
	OccurredAt string                 `json:"occurredAt"`
	Meta       map[string]interface{} `json:"meta"`
}

// recordRevenueResponse is the data payload of POST /api/internal/revenue.
type recordRevenueResponse struct {
	ID        int64  `json:"id"`
	Kind      string `json:"kind"`
	Signature string `json:"signature"`
	// Created is false when an entry with this signature was already in the ledger.
	Created bool `json:"created"`
}

// InternalRevenueHandler serves the keeper's ledger write endpoint.
type InternalRevenueHandler struct {
	svc    *services.RevenueService
	token  string
	logger *slog.Logger
}

// NewInternalRevenueHandler creates the handler. An empty token disables the endpoint:
// every request answers 404, and bootstrap does not register the route at all.
func NewInternalRevenueHandler(svc *services.RevenueService, token string) *InternalRevenueHandler {
	return &InternalRevenueHandler{svc: svc, token: strings.TrimSpace(token), logger: slog.Default()}
}

// Enabled reports whether a shared secret is configured.
func (h *InternalRevenueHandler) Enabled() bool { return h != nil && h.token != "" && h.svc != nil }

// authorized compares the request header to the shared secret in constant time.
func (h *InternalRevenueHandler) authorized(r *http.Request) bool {
	got := r.Header.Get(InternalTokenHeader)
	return subtle.ConstantTimeCompare([]byte(got), []byte(h.token)) == 1
}

// HandleRecord handles POST /api/internal/revenue.
// 201 when the entry was written, 200 when the signature was already recorded (idempotent).
func (h *InternalRevenueHandler) HandleRecord(w http.ResponseWriter, r *http.Request) {
	if !h.Enabled() {
		SendError(w, http.StatusNotFound, "NOT_FOUND", "Route not found")
		return
	}
	if !h.authorized(r) {
		SendError(w, http.StatusUnauthorized, "UNAUTHORIZED", "Invalid or missing internal token")
		return
	}

	var dto recordRevenueDTO
	if err := json.NewDecoder(io.LimitReader(r.Body, internalRevenueBodyLimit)).Decode(&dto); err != nil {
		SendError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid JSON body")
		return
	}
	entry, msg := buildRevenueEntry(&dto)
	if msg != "" {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", msg)
		return
	}

	created, err := h.svc.Record(r.Context(), entry)
	if err != nil {
		if errors.Is(err, services.ErrRevenueInvalidKind) {
			SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", err.Error())
			return
		}
		h.logger.Error("[InternalRevenueHandler.HandleRecord] insert failed", "signature", entry.Signature, "error", err)
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to record revenue entry")
		return
	}

	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	SendJSON(w, status, DataEnvelope{Data: recordRevenueResponse{
		ID: entry.ID, Kind: entry.Kind, Signature: entry.Signature, Created: created,
	}})
}

// buildRevenueEntry validates the DTO and converts it to a ledger entry.
// Returns a non-empty message when the body is invalid.
func buildRevenueEntry(d *recordRevenueDTO) (*models.PlatformRevenue, string) {
	d.Kind = strings.TrimSpace(d.Kind)
	d.QuoteMint = strings.TrimSpace(d.QuoteMint)
	d.Signature = strings.TrimSpace(d.Signature)
	d.Mint = strings.TrimSpace(d.Mint)
	d.OccurredAt = strings.TrimSpace(d.OccurredAt)

	switch {
	case !models.IsValidRevenueKind(d.Kind):
		return nil, "kind must be one of: " + strings.Join(models.RevenueKinds, ", ")
	case !isPubkey(d.QuoteMint):
		return nil, "quoteMint must be a base58 32-byte public key"
	case !isTxSignature(d.Signature):
		return nil, "signature must be a base58 64-byte transaction signature"
	case d.Mint != "" && !isPubkey(d.Mint):
		return nil, "mint must be a base58 32-byte public key"
	case math.IsNaN(d.AmountRaw) || math.IsInf(d.AmountRaw, 0) || d.AmountRaw < 0:
		return nil, "amountRaw must be a finite number >= 0"
	case d.AmountUSD != nil && (math.IsNaN(*d.AmountUSD) || math.IsInf(*d.AmountUSD, 0) || *d.AmountUSD < 0):
		return nil, "amountUsd must be a finite number >= 0"
	case len(d.Meta) > internalRevenueMaxMetaKeys:
		return nil, "meta must have at most 32 keys"
	}

	occurred, err := time.Parse(time.RFC3339, d.OccurredAt)
	if err != nil {
		return nil, "occurredAt must be an RFC3339 timestamp"
	}
	if occurred.After(time.Now().UTC().Add(internalRevenueFutureSkew)) {
		return nil, "occurredAt must not be in the future"
	}

	return &models.PlatformRevenue{
		Kind:       d.Kind,
		QuoteMint:  d.QuoteMint,
		AmountRaw:  d.AmountRaw,
		AmountUSD:  d.AmountUSD,
		Signature:  d.Signature,
		Mint:       d.Mint,
		OccurredAt: occurred.UTC(),
		Meta:       d.Meta,
	}, ""
}
