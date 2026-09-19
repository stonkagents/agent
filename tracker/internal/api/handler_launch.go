// Package: tracker/internal/api
// Feature: StonkAgents launchpad (Raydium LaunchLab)
// Purpose: HTTP handlers for launch records — public record/read endpoints and the
//          authenticated claim that binds a launch to the creator's daemon peer.

package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/mr-tron/base58"

	"github.com/stonkagents/agent/tracker/internal/services"
)

const (
	launchBodyLimit      = 8 * 1024
	launchHandlerTimeout = 12 * time.Second // below the server's 15s WriteTimeout; RPC verification happens inside
	launchMaxSymbolBytes = 10
	launchMaxNameBytes   = 32
	launchMaxURLBytes    = 512
	launchPendingLimit   = 50
	// launchViewTimeout bounds the enriched single-launch read (its metrics lookup can
	// reach the launchpad RPC); well under the server's 15s WriteTimeout.
	launchViewTimeout = 6 * time.Second
)

// recordLaunchDTO is the body of POST /api/launch/record (camelCase, sent by the browser).
type recordLaunchDTO struct {
	Mint            string `json:"mint"`
	PoolID          string `json:"poolId"`
	CreatorWallet   string `json:"creatorWallet"`
	QuoteMint       string `json:"quoteMint"`
	Name            string `json:"name"`
	Symbol          string `json:"symbol"`
	ImageURL        string `json:"imageUrl"`
	ImageThumbURL   string `json:"imageThumbUrl"`
	MetadataURI     string `json:"metadataUri"`
	LaunchSignature string `json:"launchSignature"`
	FeeLamports     int64  `json:"feeLamports"`
	TransferFeeBps  *int   `json:"transferFeeBps"`
}

// claimLaunchDTO is the body of POST /api/launch/claim.
type claimLaunchDTO struct {
	Mint string `json:"mint"`
}

// claimLaunchResponse is the data payload of POST /api/launch/claim.
type claimLaunchResponse struct {
	Launch         interface{} `json:"launch"`
	CreditsGranted int         `json:"credits_granted"`
	AlreadyBound   bool        `json:"already_bound"`
	// PeerDisplayName is the bound agent name after the claim (token-derived unless the owner set one).
	PeerDisplayName string `json:"peer_display_name,omitempty"`
}

// LaunchHandler serves the launch record / claim endpoints.
type LaunchHandler struct {
	svc    *services.LaunchService
	logger *slog.Logger
}

// NewLaunchHandler creates a LaunchHandler.
func NewLaunchHandler(svc *services.LaunchService) *LaunchHandler {
	return &LaunchHandler{svc: svc, logger: slog.Default()}
}

// isBase58Bytes reports whether s decodes as base58 to exactly n bytes.
func isBase58Bytes(s string, n int) bool {
	if s == "" || len(s) > 2*n {
		return false
	}
	b, err := base58.Decode(s)
	return err == nil && len(b) == n
}

// isPubkey validates a base58 32-byte Solana public key.
func isPubkey(s string) bool { return isBase58Bytes(s, 32) }

// isTxSignature validates a base58 64-byte transaction signature.
func isTxSignature(s string) bool { return isBase58Bytes(s, 64) }

// isSafeText rejects control characters (names/symbols are rendered in the portal).
func isSafeText(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// isAssetURL accepts http(s), ipfs and arweave URIs within the length cap.
func isAssetURL(s string) bool {
	if len(s) > launchMaxURLBytes || !isSafeText(s) || strings.ContainsAny(s, " \"'<>") {
		return false
	}
	for _, p := range []string{"https://", "http://", "ipfs://", "ar://"} {
		if strings.HasPrefix(s, p) && len(s) > len(p) {
			return true
		}
	}
	return false
}

// validateRecordDTO applies boundary validation; returns a message ("" = valid).
func validateRecordDTO(d *recordLaunchDTO) string {
	d.Mint = strings.TrimSpace(d.Mint)
	d.PoolID = strings.TrimSpace(d.PoolID)
	d.CreatorWallet = strings.TrimSpace(d.CreatorWallet)
	d.QuoteMint = strings.TrimSpace(d.QuoteMint)
	d.Name = strings.TrimSpace(d.Name)
	d.Symbol = strings.TrimSpace(d.Symbol)
	d.ImageURL = strings.TrimSpace(d.ImageURL)
	d.ImageThumbURL = strings.TrimSpace(d.ImageThumbURL)
	d.MetadataURI = strings.TrimSpace(d.MetadataURI)
	d.LaunchSignature = strings.TrimSpace(d.LaunchSignature)

	switch {
	case !isPubkey(d.Mint):
		return "mint must be a base58 32-byte public key"
	case d.PoolID != "" && !isPubkey(d.PoolID):
		return "poolId must be a base58 32-byte public key"
	case !isPubkey(d.CreatorWallet):
		return "creatorWallet must be a base58 32-byte public key"
	case !isPubkey(d.QuoteMint):
		return "quoteMint must be a base58 32-byte public key"
	case !isTxSignature(d.LaunchSignature):
		return "launchSignature must be a base58 64-byte transaction signature"
	case d.Name == "" || len(d.Name) > launchMaxNameBytes || !isSafeText(d.Name):
		return "name is required and must be at most 32 bytes"
	case d.Symbol == "" || len(d.Symbol) > launchMaxSymbolBytes || !isSafeText(d.Symbol):
		return "symbol is required and must be at most 10 bytes"
	case d.ImageURL != "" && !isAssetURL(d.ImageURL):
		return "imageUrl must be an http(s), ipfs or ar URI of at most 512 bytes"
	case d.ImageThumbURL != "" && !isAssetURL(d.ImageThumbURL):
		return "imageThumbUrl must be an http(s), ipfs or ar URI of at most 512 bytes"
	case d.MetadataURI != "" && !isAssetURL(d.MetadataURI):
		return "metadataUri must be an http(s), ipfs or ar URI of at most 512 bytes"
	case d.FeeLamports < 0:
		return "feeLamports must be >= 0"
	case d.TransferFeeBps != nil && (*d.TransferFeeBps < 0 || *d.TransferFeeBps > 10000):
		return "transferFeeBps must be between 0 and 10000"
	}
	return ""
}

// HandleRecord handles POST /api/launch/record (public; verified on chain before insert).
// 201 on first record, 200 when the same signature was already recorded.
func (h *LaunchHandler) HandleRecord(w http.ResponseWriter, r *http.Request) {
	var dto recordLaunchDTO
	if err := json.NewDecoder(io.LimitReader(r.Body, launchBodyLimit)).Decode(&dto); err != nil {
		SendError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid JSON body")
		return
	}
	if msg := validateRecordDTO(&dto); msg != "" {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", msg)
		return
	}
	in := services.RecordLaunchInput{
		Mint: dto.Mint, PoolID: dto.PoolID, CreatorWallet: dto.CreatorWallet, QuoteMint: dto.QuoteMint,
		Name: dto.Name, Symbol: dto.Symbol, ImageURL: dto.ImageURL, ImageThumbURL: dto.ImageThumbURL, MetadataURI: dto.MetadataURI,
		LaunchSignature: dto.LaunchSignature, FeeLamports: dto.FeeLamports, TransferFeeBps: services.DefaultTransferFeeBps,
	}
	if dto.TransferFeeBps != nil {
		in.TransferFeeBps = *dto.TransferFeeBps
	}

	ctx, cancel := context.WithTimeout(r.Context(), launchHandlerTimeout)
	defer cancel()
	launch, created, err := h.svc.Record(ctx, in)
	if err != nil {
		h.sendLaunchError(w, "HandleRecord", err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	SendJSON(w, status, DataEnvelope{Data: launch})
}

// HandleGet handles GET /api/launch/{mint} (public). The response carries the launch,
// its quote token and live metrics so a token page renders from one request.
func (h *LaunchHandler) HandleGet(w http.ResponseWriter, r *http.Request) {
	mint := mux.Vars(r)["mint"]
	// The mint is the resource path: a value that cannot be a mint is a launch that
	// does not exist, not a malformed request, so the portal sees one 404 contract.
	if !isPubkey(mint) {
		SendError(w, http.StatusNotFound, "NOT_FOUND", services.ErrLaunchNotFound.Error())
		return
	}
	// Metrics may hit the launchpad RPC; cap it well under the server write timeout.
	ctx, cancel := context.WithTimeout(r.Context(), launchViewTimeout)
	defer cancel()
	launch, err := h.svc.GetView(ctx, mint)
	if err != nil {
		h.sendLaunchError(w, "HandleGet", err)
		return
	}
	SendData(w, launch)
}

// HandleList handles GET /api/launches?creator=&limit=&offset=|cursor= (public, paginated).
// cursor is accepted as an alias for offset so the portal's cursor-style client works unchanged.
func (h *LaunchHandler) HandleList(w http.ResponseWriter, r *http.Request) {
	limit, offset := parsePagination(r, 20, 100)
	if c := r.URL.Query().Get("cursor"); c != "" && r.URL.Query().Get("offset") == "" {
		if parsed, err := strconv.Atoi(c); err == nil && parsed >= 0 {
			offset = parsed
		} else {
			SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "cursor must be a non-negative integer offset")
			return
		}
	}
	creator := strings.TrimSpace(r.URL.Query().Get("creator"))
	if creator != "" && !isPubkey(creator) {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "creator must be a base58 32-byte public key")
		return
	}
	items, total, err := h.svc.ListViews(r.Context(), creator, limit, offset)
	if err != nil {
		h.sendLaunchError(w, "HandleList", err)
		return
	}
	SendDataWithMeta(w, items, total, limit, offset)
}

// HandlePending handles GET /api/launch/pending?wallet= (public): unbound launches for a wallet.
func (h *LaunchHandler) HandlePending(w http.ResponseWriter, r *http.Request) {
	wallet := strings.TrimSpace(r.URL.Query().Get("wallet"))
	if !isPubkey(wallet) {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "wallet must be a base58 32-byte public key")
		return
	}
	items, err := h.svc.PendingViews(r.Context(), wallet, launchPendingLimit)
	if err != nil {
		h.sendLaunchError(w, "HandlePending", err)
		return
	}
	SendData(w, items)
}

// HandleByWallet handles GET /api/launch/by-wallet?wallet= (public): every launch recorded for
// the creator wallet, claimed or not (unlike /api/launch/pending, which is unbound only).
func (h *LaunchHandler) HandleByWallet(w http.ResponseWriter, r *http.Request) {
	wallet := strings.TrimSpace(r.URL.Query().Get("wallet"))
	if !isPubkey(wallet) {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "wallet must be a base58 32-byte public key")
		return
	}
	items, err := h.svc.ByWalletViews(r.Context(), wallet, launchPendingLimit)
	if err != nil {
		h.sendLaunchError(w, "HandleByWallet", err)
		return
	}
	SendData(w, items)
}

// HandleClaim handles POST /api/launch/claim (authenticated via RequireAPIKey through the daemon proxy).
func (h *LaunchHandler) HandleClaim(w http.ResponseWriter, r *http.Request) {
	peerID := ForumPeerIDFromContext(r.Context())
	if peerID == "" {
		SendError(w, http.StatusUnauthorized, "UNAUTHORIZED", "Missing peer identity")
		return
	}
	var dto claimLaunchDTO
	if err := json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&dto); err != nil {
		SendError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid JSON body")
		return
	}
	dto.Mint = strings.TrimSpace(dto.Mint)
	if !isPubkey(dto.Mint) {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "mint must be a base58 32-byte public key")
		return
	}
	res, err := h.svc.Claim(r.Context(), peerID, dto.Mint)
	if err != nil {
		h.sendLaunchError(w, "HandleClaim", err)
		return
	}
	SendData(w, claimLaunchResponse{Launch: res.Launch, CreditsGranted: res.CreditsGranted, AlreadyBound: res.AlreadyBound, PeerDisplayName: res.PeerDisplayName})
}

// launchErrorMap maps service errors to HTTP status + error code.
var launchErrorMap = []struct {
	err    error
	status int
	code   string
}{
	{services.ErrLaunchNotConfigured, http.StatusServiceUnavailable, "LAUNCH_NOT_CONFIGURED"},
	{services.ErrLaunchVerifyUnavailable, http.StatusServiceUnavailable, "VERIFICATION_UNAVAILABLE"},
	{services.ErrLaunchTxNotFound, http.StatusUnprocessableEntity, "TX_NOT_FOUND"},
	{services.ErrLaunchTxFailed, http.StatusUnprocessableEntity, "TX_FAILED"},
	{services.ErrLaunchCreatorNotSigner, http.StatusUnprocessableEntity, "CREATOR_NOT_SIGNER"},
	{services.ErrLaunchProgramMissing, http.StatusUnprocessableEntity, "LAUNCHLAB_INSTRUCTION_MISSING"},
	{services.ErrLaunchPoolMismatch, http.StatusUnprocessableEntity, "POOL_MISMATCH"},
	{services.ErrLaunchQuoteMismatch, http.StatusUnprocessableEntity, "QUOTE_MINT_MISMATCH"},
	{services.ErrLaunchQuoteNotAllowed, http.StatusUnprocessableEntity, LaunchQuoteNotAllowedCode},
	{services.ErrLaunchPlatformMismatch, http.StatusUnprocessableEntity, "PLATFORM_MISMATCH"},
	{services.ErrLaunchFeeMissing, http.StatusUnprocessableEntity, "FEE_TRANSFER_MISSING"},
	{services.ErrLaunchSignatureConflict, http.StatusConflict, "SIGNATURE_CONFLICT"},
	{services.ErrLaunchMintConflict, http.StatusConflict, "MINT_CONFLICT"},
	{services.ErrLaunchNotFound, http.StatusNotFound, "NOT_FOUND"},
	{services.ErrLaunchAlreadyBound, http.StatusConflict, "ALREADY_BOUND"},
	{services.ErrLaunchWalletNotLinked, http.StatusConflict, "WALLET_NOT_LINKED"},
	{services.ErrLaunchWalletMismatch, http.StatusForbidden, "WALLET_MISMATCH"},
}

// launchExistsEnvelope is the 409 body when the creator wallet already has a launch:
// {"error":{"code":"LAUNCH_EXISTS","message":...,"mint":<existing launch mint>}}.
type launchExistsEnvelope struct {
	Error launchExistsDetail `json:"error"`
}

type launchExistsDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Mint    string `json:"mint"`
}

func (h *LaunchHandler) sendLaunchError(w http.ResponseWriter, op string, err error) {
	var exists *services.LaunchExistsError
	if errors.As(err, &exists) {
		SendJSON(w, http.StatusConflict, launchExistsEnvelope{Error: launchExistsDetail{
			Code: "LAUNCH_EXISTS", Message: services.ErrLaunchWalletHasLaunch.Error(), Mint: exists.Mint,
		}})
		return
	}
	for _, m := range launchErrorMap {
		if errors.Is(err, m.err) {
			SendError(w, m.status, m.code, m.err.Error())
			return
		}
	}
	h.logger.Error("[LaunchHandler."+op+"] failed", "error", err)
	SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Launch operation failed")
}
