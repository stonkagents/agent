// Package: tracker/internal/api
// Feature: StonkAgents launchpad (Raydium LaunchLab)
// Purpose: GET /api/revenue — public platform revenue totals and 30-day daily series

package api

import (
	"log/slog"
	"net/http"

	"github.com/stonkagents/agent/tracker/internal/services"
)

// RevenueHandler serves the public revenue summary.
type RevenueHandler struct {
	svc    *services.RevenueService
	logger *slog.Logger
}

// NewRevenueHandler creates a RevenueHandler.
func NewRevenueHandler(svc *services.RevenueService) *RevenueHandler {
	return &RevenueHandler{svc: svc, logger: slog.Default()}
}

// HandleSummary handles GET /api/revenue (public).
func (h *RevenueHandler) HandleSummary(w http.ResponseWriter, r *http.Request) {
	summary, err := h.svc.Summary(r.Context())
	if err != nil {
		h.logger.Error("[RevenueHandler.HandleSummary] failed", "error", err)
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to aggregate revenue")
		return
	}
	SendData(w, summary)
}
