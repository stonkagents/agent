// Package api: Tests for curated packs endpoint (F-027, US-027-04)
// Feature: F-027 (Gallery Page Completion)
// Story: US-027-04 (Backend — Curated Packs Endpoint)
// Purpose: Verify GET /api/packs returns embedded pack catalog

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestPortal_Packs_Returns200AndPacks verifies GET /api/packs returns the embedded pack catalog.
func TestPortal_Packs_Returns200AndPacks(t *testing.T) {
	env := newPortalTestEnv(t)

	req := httptest.NewRequest(http.MethodGet, "/api/packs", nil)
	w := httptest.NewRecorder()
	env.srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/packs status = %d, want 200; body = %s", w.Code, w.Body.String())
	}

	var envelope struct {
		Data []map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	// Must have 4 packs
	if len(envelope.Data) != 4 {
		t.Fatalf("packs count = %d, want 4", len(envelope.Data))
	}

	// Verify pack IDs
	expectedIDs := []string{"starter-pack", "defi-research", "code-review", "security-audit"}
	for i, pack := range envelope.Data {
		id, ok := pack["id"].(string)
		if !ok {
			t.Fatalf("pack[%d] missing 'id' field", i)
		}
		if id != expectedIDs[i] {
			t.Errorf("pack[%d].id = %q, want %q", i, id, expectedIDs[i])
		}
	}
}

// TestPortal_Packs_ItemsHaveRequiredFields verifies each pack item has required fields.
func TestPortal_Packs_ItemsHaveRequiredFields(t *testing.T) {
	env := newPortalTestEnv(t)

	req := httptest.NewRequest(http.MethodGet, "/api/packs", nil)
	w := httptest.NewRecorder()
	env.srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var envelope struct {
		Data []struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			Description string `json:"description"`
			Icon        string `json:"icon"`
			ItemCount   int    `json:"item_count"`
			Items       []struct {
				Filename    string `json:"filename"`
				Type        string `json:"type"`
				Title       string `json:"title"`
				Description string `json:"description"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}

	for _, pack := range envelope.Data {
		if pack.Name == "" {
			t.Errorf("pack %q has empty name", pack.ID)
		}
		if pack.Description == "" {
			t.Errorf("pack %q has empty description", pack.ID)
		}
		if pack.Icon == "" {
			t.Errorf("pack %q has empty icon", pack.ID)
		}
		if pack.ItemCount != len(pack.Items) {
			t.Errorf("pack %q: item_count=%d but len(items)=%d", pack.ID, pack.ItemCount, len(pack.Items))
		}
		for j, item := range pack.Items {
			if item.Filename == "" {
				t.Errorf("pack %q item[%d] has empty filename", pack.ID, j)
			}
			if item.Type == "" {
				t.Errorf("pack %q item[%d] has empty type", pack.ID, j)
			}
			if item.Title == "" {
				t.Errorf("pack %q item[%d] has empty title", pack.ID, j)
			}
		}
	}
}

// TestPortal_Packs_StarterPackHas7Items verifies the starter pack has exactly 7 items.
func TestPortal_Packs_StarterPackHas7Items(t *testing.T) {
	env := newPortalTestEnv(t)

	req := httptest.NewRequest(http.MethodGet, "/api/packs", nil)
	w := httptest.NewRecorder()
	env.srv.Router().ServeHTTP(w, req)

	var envelope struct {
		Data []struct {
			ID    string                   `json:"id"`
			Items []map[string]interface{} `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}

	for _, pack := range envelope.Data {
		if pack.ID == "starter-pack" {
			if len(pack.Items) != 7 {
				t.Errorf("starter-pack items count = %d, want 7", len(pack.Items))
			}
			return
		}
	}
	t.Fatal("starter-pack not found in response")
}
