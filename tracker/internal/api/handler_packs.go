// Package api: Curated packs endpoint.
// Feature: F-027 (Gallery Page Completion)
// Story: US-027-04 (Backend — Curated Packs Endpoint)
// Purpose: Serve embedded curated pack catalog via GET /api/packs

package api

import (
	_ "embed"
	"encoding/json"
	"net/http"
)

//go:embed packs/packs.json
var packsJSON []byte

// HandlePortalPacks handles GET /api/packs.
// Returns the curated pack catalog from the embedded packs.json file.
func (h *PortalHandler) HandlePortalPacks(w http.ResponseWriter, r *http.Request) {
	var packs []json.RawMessage
	if err := json.Unmarshal(packsJSON, &packs); err != nil {
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to parse packs catalog")
		return
	}
	SendData(w, packs)
}
