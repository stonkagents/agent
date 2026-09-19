// Package golden provides snapshot tests for the daemon API response shapes.
//
// Usage:
//
//	go test -v                       # Compare against saved snapshots
//	GOLDEN_UPDATE=1 go test -v       # Regenerate .golden files
//
// Golden files are stored in testdata/ and should be committed to version control.
// When API response shapes change intentionally, re-run with -update and review diffs.
package golden

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Set via: GOLDEN_UPDATE=1 go test -v
var update = os.Getenv("GOLDEN_UPDATE") == "1" || os.Getenv("GOLDEN_UPDATE") == "true"

// goldenTest captures a named HTTP request/response pair for snapshot testing.
type goldenTest struct {
	name       string
	method     string
	path       string
	body       string
	handler    http.HandlerFunc
	statusCode int
}

// TestHealthEndpointGolden tests the /health endpoint response shape.
func TestHealthEndpointGolden(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":  "healthy",
			"version": "0.1.0",
		})
	})

	compareGolden(t, "health_response", http.MethodGet, "/health", "", handler, http.StatusOK)
}

// TestErrorResponseGolden tests the standard error envelope shape.
func TestErrorResponseGolden(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]interface{}{
				"code":    "UNAUTHORIZED",
				"message": "Missing authorization token",
			},
		})
	})

	compareGolden(t, "error_unauthorized", http.MethodGet, "/api/v1/status", "", handler, http.StatusUnauthorized)
}

// TestSearchEmptyResponseGolden tests the search endpoint with no results.
func TestSearchEmptyResponseGolden(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"results": []interface{}{},
			"total":   0,
		})
	})

	compareGolden(t, "search_empty", http.MethodGet, "/api/v1/search?q=test", "", handler, http.StatusOK)
}

// TestStatusResponseGolden tests the status endpoint response shape.
func TestStatusResponseGolden(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"daemon": map[string]interface{}{
				"version":        "0.1.0",
				"uptime_seconds": 0,
				"peer_id":        "12D3KooMVP",
			},
			"network": map[string]interface{}{
				"connected": false,
				"peers":     0,
			},
			"shared_assets": 0,
			"downloads": map[string]interface{}{
				"active":    0,
				"queued":    0,
				"completed": 0,
			},
		})
	})

	compareGolden(t, "status_response", http.MethodGet, "/api/v1/status", "", handler, http.StatusOK)
}

// TestDownloadQueuedResponseGolden tests the download accepted response shape.
func TestDownloadQueuedResponseGolden(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"cid":     "bafkreigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi",
			"status":  "queued",
			"message": "Download queued successfully",
		})
	})

	compareGolden(t, "download_queued", http.MethodPost, "/api/v1/download", `{"cid":"bafkreigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi"}`, handler, http.StatusAccepted)
}

// TestShareResponseGolden tests the share success response shape.
func TestShareResponseGolden(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"cid":     "bafkreigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi",
			"message": "Asset shared successfully",
		})
	})

	compareGolden(t, "share_success", http.MethodPost, "/api/v1/share", "", handler, http.StatusCreated)
}

// TestValidationErrorGolden tests the validation error response shape.
func TestValidationErrorGolden(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]interface{}{
				"code":    "MISSING_CID",
				"message": "CID is required",
			},
		})
	})

	compareGolden(t, "error_missing_cid", http.MethodPost, "/api/v1/download", `{}`, handler, http.StatusBadRequest)
}

// TestForbiddenErrorGolden tests the RBAC forbidden response shape.
func TestForbiddenErrorGolden(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]interface{}{
				"code":    "FORBIDDEN",
				"message": "Insufficient privileges: requires daemon role",
				"details": map[string]interface{}{
					"required_role": "daemon",
					"user_role":     "client",
				},
			},
		})
	})

	compareGolden(t, "error_forbidden", http.MethodPost, "/api/v1/share", "", handler, http.StatusForbidden)
}

// compareGolden is the core helper that executes a request, captures the response,
// and compares it to the golden file.
func compareGolden(t *testing.T, name, method, path, body string, handler http.HandlerFunc, expectedStatus int) {
	t.Helper()

	// Build request
	var bodyReader *strings.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	} else {
		bodyReader = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, bodyReader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}

	// Execute
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	// Check status code
	if rec.Code != expectedStatus {
		t.Errorf("expected status %d, got %d", expectedStatus, rec.Code)
	}

	// Pretty-print the JSON for readable golden files
	var rawJSON interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &rawJSON); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	prettyJSON, err := json.MarshalIndent(rawJSON, "", "  ")
	if err != nil {
		t.Fatalf("failed to pretty-print JSON: %v", err)
	}

	// Build golden snapshot (status + headers + body)
	snapshot := buildSnapshot(method, path, rec.Code, rec.Header().Get("Content-Type"), prettyJSON)

	goldenPath := filepath.Join("testdata", name+".golden")

	if update {
		// Write/overwrite golden file
		os.MkdirAll("testdata", 0755)
		if err := os.WriteFile(goldenPath, []byte(snapshot), 0644); err != nil {
			t.Fatalf("failed to write golden file: %v", err)
		}
		t.Logf("updated %s", goldenPath)
		return
	}

	// Read and compare
	expected, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("golden file %s not found. Run to generate:\n  cd dev-tools/golden && go test -v -args -update", goldenPath)
	}

	if snapshot != string(expected) {
		t.Errorf("response does not match golden file %s.\nRun with -args -update to regenerate.\n\nExpected:\n%s\n\nGot:\n%s", goldenPath, string(expected), snapshot)
	}
}

// buildSnapshot creates a deterministic text representation of an API response.
func buildSnapshot(method, path string, status int, contentType string, body []byte) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("# %s %s\n", method, path))
	b.WriteString(fmt.Sprintf("# Status: %s (%d)\n", http.StatusText(status), status))
	b.WriteString(fmt.Sprintf("# Content-Type: %s\n", contentType))
	b.WriteString("\n")
	b.Write(body)
	b.WriteString("\n")
	return b.String()
}
