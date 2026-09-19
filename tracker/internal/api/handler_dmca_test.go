// Package: tracker/internal/api
// Feature: F-007 (Centralized Tracker)
// Story: US-007-06 (DMCA Takedown Endpoint)
// Purpose: Tests for DMCA HTTP handler

package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func announceAsset(srv *Server, cid, peerID string) {
	registerPeer(srv, peerID)
	body, _ := json.Marshal(AnnounceAssetDTO{
		CID: cid, Filename: "test-" + cid + ".txt",
		PeerID: peerID, ManifestType: "raw",
	})
	req := httptest.NewRequest("POST", "/api/v1/tracker/announce", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
}

func TestHandleDMCA_Success(t *testing.T) {
	srv, _ := newTestServer(t)
	announceAsset(srv, "cid-1", "peer-1")

	body, _ := json.Marshal(DMCANoticeDTO{
		CID:           "cid-1",
		ReporterEmail: "legal@example.com",
		ComplaintText: "Copyright infringement",
	})
	req := httptest.NewRequest("POST", "/api/v1/tracker/dmca", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("HandleDMCA() status = %d, want %d, body: %s", w.Code, http.StatusCreated, w.Body.String())
	}

	var resp DMCAResponseDTO
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Status != "pending" {
		t.Errorf("HandleDMCA() status = %q, want %q", resp.Status, "pending")
	}
}

func TestHandleDMCA_MissingFields(t *testing.T) {
	srv, _ := newTestServer(t)

	body, _ := json.Marshal(DMCANoticeDTO{CID: "cid-1"}) // missing email
	req := httptest.NewRequest("POST", "/api/v1/tracker/dmca", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("HandleDMCA() status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestHandleDMCA_AssetNotFound(t *testing.T) {
	srv, _ := newTestServer(t)

	body, _ := json.Marshal(DMCANoticeDTO{
		CID:           "nonexistent",
		ReporterEmail: "legal@example.com",
		ComplaintText: "test",
	})
	req := httptest.NewRequest("POST", "/api/v1/tracker/dmca", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("HandleDMCA() status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestHandleGetAsset_Quarantined_Returns451(t *testing.T) {
	srv, _ := newTestServer(t)
	announceAsset(srv, "cid-quarantine", "peer-1")

	// File DMCA notice (quarantines the asset)
	body, _ := json.Marshal(DMCANoticeDTO{
		CID:           "cid-quarantine",
		ReporterEmail: "legal@example.com",
		ComplaintText: "Copyright infringement",
	})
	req := httptest.NewRequest("POST", "/api/v1/tracker/dmca", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("DMCA filing status = %d, want %d", w.Code, http.StatusCreated)
	}

	// GET /api/v1/tracker/assets/cid-quarantine should return 451
	req = httptest.NewRequest("GET", "/api/v1/tracker/assets/cid-quarantine", nil)
	w = httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusUnavailableForLegalReasons {
		t.Errorf("GetAsset(quarantined) status = %d, want %d", w.Code, http.StatusUnavailableForLegalReasons)
	}

	var errResp ErrorEnvelope
	_ = json.Unmarshal(w.Body.Bytes(), &errResp)
	if errResp.Error.Code != "QUARANTINED" {
		t.Errorf("GetAsset(quarantined) error code = %q, want %q", errResp.Error.Code, "QUARANTINED")
	}
}

func TestHandleGetAsset_NotQuarantined_Returns200(t *testing.T) {
	srv, _ := newTestServer(t)
	announceAsset(srv, "cid-normal", "peer-1")

	// GET /api/v1/tracker/assets/cid-normal should return 200
	req := httptest.NewRequest("GET", "/api/v1/tracker/assets/cid-normal", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("GetAsset(normal) status = %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
	}
}

func TestHandleGetAsset_NotFound(t *testing.T) {
	srv, _ := newTestServer(t)

	req := httptest.NewRequest("GET", "/api/v1/tracker/assets/nonexistent", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("GetAsset(notfound) status = %d, want %d", w.Code, http.StatusNotFound)
	}
}
