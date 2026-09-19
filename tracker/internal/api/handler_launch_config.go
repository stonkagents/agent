// Package: tracker/internal/api
// Feature: StonkAgents Launchpad (Raydium LaunchLab)
// Purpose: Public GET /api/launch/config — launch parameters, live fee, quotes and sized raise

package api

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/services"
)

// LaunchFeeDTO is the live launch fee.
type LaunchFeeDTO struct {
	USD      float64 `json:"usd"`
	Lamports int64   `json:"lamports"`
	SolUSD   float64 `json:"solUsd"`
	PricedAt string  `json:"pricedAt"`
	Stale    bool    `json:"stale"`
}

// LaunchQuoteDTO is one quote token a launch can raise in.
type LaunchQuoteDTO struct {
	Cluster           string `json:"cluster"`
	QuoteMint         string `json:"quoteMint"`
	Symbol            string `json:"symbol"`
	Name              string `json:"name"`
	Decimals          int    `json:"decimals"`
	TokenProgram      string `json:"tokenProgram"`
	Category          string `json:"category"`
	LaunchLabConfigID string `json:"launchlabConfigId"`
	MinFundRaisingRaw string `json:"minFundRaisingRaw"`
	Enabled           bool   `json:"enabled"`
	SortOrder         int    `json:"sortOrder"`
}

// LaunchRaiseDTO is the sized raise for the selected quote. Raw amounts are strings (may exceed 2^53).
type LaunchRaiseDTO struct {
	Raw        string  `json:"raw"`
	Units      float64 `json:"units"`
	MinimumRaw string  `json:"minimumRaw"`
	Basis      string  `json:"basis"`
}

// LaunchCurveDTO is the fixed LaunchLab curve the browser passes to the Raydium SDK.
type LaunchCurveDTO struct {
	ConfigID          string `json:"configId"`
	CurveType         string `json:"curveType"`
	MigrateType       string `json:"migrateType"`
	BaseDecimals      int    `json:"baseDecimals"`
	Supply            string `json:"supply"`
	TotalSellA        string `json:"totalSellA"`
	TotalLockedAmount string `json:"totalLockedAmount"`
	CliffPeriod       string `json:"cliffPeriod"`
	UnlockPeriod      string `json:"unlockPeriod"`
	CpmmCreatorFeeOn  int    `json:"cpmmCreatorFeeOn"`
}

// LaunchConfigDTO is the response data for GET /api/launch/config.
type LaunchConfigDTO struct {
	// Cluster is the Solana cluster the program and quotes live on ("mainnet" | "devnet").
	Cluster        string       `json:"cluster"`
	ProgramID      string       `json:"programId"`
	PlatformID     string       `json:"platformId"`
	Treasury       string       `json:"treasury"`
	TransferFeeBps int          `json:"transferFeeBps"`
	Fee            LaunchFeeDTO `json:"fee"`
	// DefaultQuoteMint is the $STONK quote mint of the cluster — the only quoteMint
	// POST /api/launch/record accepts.
	DefaultQuoteMint string `json:"defaultQuoteMint"`
	// Quotes is the launchable catalog: exactly one entry, the $STONK quote.
	Quotes []LaunchQuoteDTO `json:"quotes"`
	Quote  LaunchQuoteDTO   `json:"quote"`
	Raise  LaunchRaiseDTO   `json:"raise"`
	Curve  LaunchCurveDTO   `json:"curve"`
	// DevDripEnabled is true only when POST /api/dev/drip is registered (DEV_DRIP_SECRET_KEY
	// set on a devnet launchpad); the portal hides the drip when false instead of hitting a 404.
	DevDripEnabled bool `json:"devDripEnabled"`
}

// LaunchConfigHandler serves launchpad configuration.
type LaunchConfigHandler struct {
	svc            *services.LaunchConfigService
	devDripEnabled bool
}

// NewLaunchConfigHandler creates a new LaunchConfigHandler.
func NewLaunchConfigHandler(svc *services.LaunchConfigService) *LaunchConfigHandler {
	return &LaunchConfigHandler{svc: svc}
}

// SetDevDripEnabled records whether the devnet drip route is registered, advertised as
// `devDripEnabled` on GET /api/launch/config. Called from the bootstrap once the drip
// handler is known (nil = disabled).
func (h *LaunchConfigHandler) SetDevDripEnabled(enabled bool) { h.devDripEnabled = enabled }

// HandleGetConfig handles GET /api/launch/config?quoteMint=<mint> (public, rate limited by IP).
func (h *LaunchConfigHandler) HandleGetConfig(w http.ResponseWriter, r *http.Request) {
	quoteMint := r.URL.Query().Get("quoteMint")
	if quoteMint != "" && !isBase58Pubkey(quoteMint) {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "quoteMint must be a base58 Solana address")
		return
	}

	result, err := h.svc.GetConfig(r.Context(), quoteMint)
	switch {
	case err == nil:
	case errors.Is(err, services.ErrLaunchQuoteNotFound):
		SendError(w, http.StatusNotFound, "NOT_FOUND", "Unknown or disabled quote mint")
		return
	case errors.Is(err, services.ErrLaunchQuoteNotAllowed):
		SendError(w, http.StatusUnprocessableEntity, LaunchQuoteNotAllowedCode, services.ErrLaunchQuoteNotAllowed.Error())
		return
	case errors.Is(err, services.ErrLaunchFeeUnavailable), errors.Is(err, services.ErrQuotePriceUnavailable):
		slog.Warn("[launch-config] pricing unavailable", "quoteMint", quoteMint, "error", err)
		SendError(w, http.StatusServiceUnavailable, "PRICING_UNAVAILABLE", "Launch pricing is temporarily unavailable")
		return
	default:
		slog.Error("[launch-config] failed to build config", "quoteMint", quoteMint, "error", err)
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to load launch configuration")
		return
	}

	dto := toLaunchConfigDTO(result)
	dto.DevDripEnabled = h.devDripEnabled
	SendData(w, dto)
}

// LaunchQuoteNotAllowedCode is the error code for a quote that is not the $STONK quote
// (422 on GET /api/launch/config?quoteMint= and POST /api/launch/record).
const LaunchQuoteNotAllowedCode = "QUOTE_NOT_ALLOWED"

func toLaunchConfigDTO(res *services.LaunchConfigResult) LaunchConfigDTO {
	quotes := make([]LaunchQuoteDTO, 0, len(res.Quotes))
	for _, q := range res.Quotes {
		quotes = append(quotes, toLaunchQuoteDTO(q))
	}
	return LaunchConfigDTO{
		Cluster:        res.Cluster,
		ProgramID:      res.ProgramID,
		PlatformID:     res.PlatformID,
		Treasury:       res.Treasury,
		TransferFeeBps: res.TransferFeeBps,
		Fee: LaunchFeeDTO{
			USD:      res.Fee.USD,
			Lamports: res.Fee.Lamports,
			SolUSD:   res.Fee.SolUSD,
			PricedAt: res.Fee.PricedAt.UTC().Format(time.RFC3339),
			Stale:    res.Fee.Stale,
		},
		DefaultQuoteMint: res.DefaultQuoteMint,
		Quotes:           quotes,
		Quote:            toLaunchQuoteDTO(res.Quote),
		Raise: LaunchRaiseDTO{
			Raw:        res.Raise.Raw.String(),
			Units:      res.Raise.Units,
			MinimumRaw: res.Raise.MinimumRaw.String(),
			Basis:      res.Raise.Basis,
		},
		Curve: LaunchCurveDTO{
			ConfigID:          res.Curve.ConfigID,
			CurveType:         res.Curve.CurveType,
			MigrateType:       res.Curve.MigrateType,
			BaseDecimals:      res.Curve.BaseDecimals,
			Supply:            res.Curve.Supply,
			TotalSellA:        res.Curve.TotalSellA,
			TotalLockedAmount: res.Curve.TotalLockedAmount,
			CliffPeriod:       res.Curve.CliffPeriod,
			UnlockPeriod:      res.Curve.UnlockPeriod,
			CpmmCreatorFeeOn:  res.Curve.CpmmCreatorFeeOn,
		},
	}
}

func toLaunchQuoteDTO(q *models.LaunchQuote) LaunchQuoteDTO {
	return LaunchQuoteDTO{
		Cluster:           q.Cluster,
		QuoteMint:         q.QuoteMint,
		Symbol:            q.Symbol,
		Name:              q.Name,
		Decimals:          q.Decimals,
		TokenProgram:      q.TokenProgram,
		Category:          q.Category,
		LaunchLabConfigID: q.LaunchLabConfigID,
		MinFundRaisingRaw: q.MinFundRaisingRaw,
		Enabled:           q.Enabled,
		SortOrder:         q.SortOrder,
	}
}

// isBase58Pubkey reports whether s looks like a base58-encoded Solana public key (32-44 chars).
func isBase58Pubkey(s string) bool {
	if len(s) < 32 || len(s) > 44 {
		return false
	}
	for _, c := range s {
		switch {
		case c >= '1' && c <= '9', c >= 'A' && c <= 'H', c >= 'J' && c <= 'N', c >= 'P' && c <= 'Z', c >= 'a' && c <= 'k', c >= 'm' && c <= 'z':
		default:
			return false
		}
	}
	return true
}
