// Package: tracker/internal/api
// Feature: F-007 (Centralized Tracker)
// Story: US-007-06 (DMCA Takedown Endpoint)
// Purpose: HTTP handler for DMCA takedown notices

package api

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/services"
)

// DMCANoticeDTO is the request body for filing a DMCA notice.
type DMCANoticeDTO struct {
	CID           string `json:"cid"`
	ReporterEmail string `json:"reporter_email"`
	ComplaintText string `json:"complaint_text"`
}

// DMCAResponseDTO is the response for a DMCA notice.
type DMCAResponseDTO struct {
	ID            string `json:"id"`
	CID           string `json:"cid"`
	ReporterEmail string `json:"reporter_email"`
	Status        string `json:"status"`
	QuarantinedAt string `json:"quarantined_at"`
}

// DMCAHandler handles DMCA-related HTTP endpoints.
type DMCAHandler struct {
	service *services.DMCAService
}

// NewDMCAHandler creates a new DMCAHandler.
func NewDMCAHandler(svc *services.DMCAService) *DMCAHandler {
	return &DMCAHandler{service: svc}
}

// HandleFileNotice handles POST /api/v1/tracker/dmca.
func (h *DMCAHandler) HandleFileNotice(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		SendError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Only POST allowed")
		return
	}

	// Parse request body with size limit (10MB max)
	limitedBody := io.LimitReader(r.Body, 10<<20)
	var dto DMCANoticeDTO
	if err := json.NewDecoder(limitedBody).Decode(&dto); err != nil {
		SendError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid JSON body")
		return
	}

	notice, err := h.service.FileNotice(r.Context(), services.FileNoticeRequest{
		CID:           dto.CID,
		ReporterEmail: dto.ReporterEmail,
		ComplaintText: dto.ComplaintText,
	})
	if err == models.ErrInvalidInput {
		SendError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Missing required fields: cid, reporter_email")
		return
	}
	if err == models.ErrNotFound {
		SendError(w, http.StatusNotFound, "NOT_FOUND", "Asset not found")
		return
	}
	if err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to file DMCA notice")
		return
	}

	SendJSON(w, http.StatusCreated, DMCAResponseDTO{
		ID:            notice.ID,
		CID:           notice.CID,
		ReporterEmail: notice.ReporterEmail,
		Status:        notice.Status,
		QuarantinedAt: notice.QuarantinedAt.Format("2006-01-02T15:04:05Z"),
	})
}
