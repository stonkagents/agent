// Package: tracker/internal/api
// Feature: StonkAgents devnet drip
// Purpose: POST /api/dev/drip sends test SOL and test $STONK to a wallet connecting to the dev
//          portal. Registered only when the drip wallet is configured on devnet (see
//          routes_dev_drip.go and cmd/tracker/bootstrap_dev_drip.go).

package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/stonkagents/agent/tracker/internal/ratelimit"
	"github.com/stonkagents/agent/tracker/internal/services"
)

const (
	devDripBodyLimit = 4 * 1024
	// devDripRequestTimeout bounds one drip end to end (balance reads, send, ~30 s confirmation).
	devDripRequestTimeout = 45 * time.Second
	// DevDripEmptyCode is the error code when the drip wallet cannot cover a drip (503).
	DevDripEmptyCode = "drip_empty"
)

// DevDripper is the service the handler drives (*services.DevDripService).
type DevDripper interface {
	Drip(ctx context.Context, wallet, ip string) (*services.DevDripResult, error)
	Config() services.DevDripConfig
}

// devDripRequestDTO is the body of POST /api/dev/drip.
type devDripRequestDTO struct {
	Wallet string `json:"wallet"`
}

// DevDripResponse is the 200 body of POST /api/dev/drip.
type DevDripResponse struct {
	// Signature is empty when the wallet already held enough of both and nothing was sent.
	Signature string `json:"signature"`
	// Sol and Stonk are the amounts actually sent, in whole units (0 when that leg was skipped).
	Sol   float64 `json:"sol"`
	Stonk float64 `json:"stonk"`
	// Explorer links the transaction (empty when nothing was sent).
	Explorer  string `json:"explorer"`
	SentSol   bool   `json:"sentSol"`
	SentStonk bool   `json:"sentStonk"`
}

// devDripLimitedResponse is the 429 body: the error envelope plus when to try again.
type devDripLimitedResponse struct {
	Error  ErrorDetail `json:"error"`
	NextAt string      `json:"nextAt"`
}

// DevDripHandler serves POST /api/dev/drip.
type DevDripHandler struct {
	svc    DevDripper
	logger *slog.Logger
}

// NewDevDripHandler creates a DevDripHandler.
func NewDevDripHandler(svc DevDripper, logger *slog.Logger) *DevDripHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &DevDripHandler{svc: svc, logger: logger}
}

// HandleDrip handles POST /api/dev/drip {"wallet": "<base58>"}.
//
//	200 {signature, sol, stonk, explorer, sentSol, sentStonk}
//	400 INVALID_REQUEST | VALIDATION_ERROR
//	429 ALREADY_DRIPPED | IP_LIMITED with nextAt (RFC 3339)
//	502 CHAIN_ERROR      the RPC failed, the transaction failed or was not confirmed in time
//	503 drip_empty       the drip wallet cannot cover the drip (logged for the operator)
func (h *DevDripHandler) HandleDrip(w http.ResponseWriter, r *http.Request) {
	var dto devDripRequestDTO
	if err := json.NewDecoder(io.LimitReader(r.Body, devDripBodyLimit)).Decode(&dto); err != nil {
		SendError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid JSON body")
		return
	}
	dto.Wallet = strings.TrimSpace(dto.Wallet)
	if !isPubkey(dto.Wallet) {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "wallet must be a base58 32-byte public key")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), devDripRequestTimeout)
	defer cancel()
	res, err := h.svc.Drip(ctx, dto.Wallet, ratelimit.IPKey(r))
	if err != nil {
		h.writeError(w, dto.Wallet, err)
		return
	}
	cfg := h.svc.Config()
	SendJSON(w, http.StatusOK, DevDripResponse{
		Signature: res.Signature,
		Sol:       services.SolFloat(res.SolLamports),
		Stonk:     services.TokenFloat(res.StonkRaw, cfg.Decimals),
		Explorer:  res.Explorer,
		SentSol:   res.SentSol,
		SentStonk: res.SentStonk,
	})
}

func (h *DevDripHandler) writeError(w http.ResponseWriter, wallet string, err error) {
	var limited *services.DripLimitedError
	switch {
	case errors.Is(err, services.ErrDripInvalidWallet):
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "wallet must be a base58 32-byte public key")
	case errors.Is(err, services.ErrDripSelf):
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "that is the drip wallet")
	case errors.As(err, &limited):
		code, msg := "ALREADY_DRIPPED", "This wallet was already dripped in the last 24 hours."
		if limited.Scope == "ip" {
			code, msg = "IP_LIMITED", "Too many drips from this address in the last hour."
		}
		SendJSON(w, http.StatusTooManyRequests, devDripLimitedResponse{
			Error:  ErrorDetail{Code: code, Message: msg},
			NextAt: limited.NextAt.UTC().Format(time.RFC3339),
		})
	case errors.Is(err, services.ErrDripEmpty):
		// The service already logged the balances; this line names the request that hit it.
		h.logger.Warn("[DevDripHandler] drip wallet empty, refill it", "wallet", wallet)
		SendError(w, http.StatusServiceUnavailable, DevDripEmptyCode, "The drip wallet is empty. Try again later.")
	case errors.Is(err, services.ErrDripChain), errors.Is(err, services.ErrDripFailed), errors.Is(err, services.ErrDripUnconfirmed):
		h.logger.Error("[DevDripHandler] chain failure", "wallet", wallet, "error", err)
		SendError(w, http.StatusBadGateway, "CHAIN_ERROR", "The devnet RPC did not accept the drip. Try again in a minute.")
	default:
		h.logger.Error("[DevDripHandler] drip failed", "wallet", wallet, "error", err)
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to drip")
	}
}
