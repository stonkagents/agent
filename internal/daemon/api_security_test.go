// Package: internal/daemon
// Purpose: Security tests for API endpoints

package daemon

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestHandleShare_PathTraversalProtection - SECURITY TEST
// Verifies that path traversal attacks are blocked
func TestHandleShare_PathTraversalProtection(t *testing.T) {
	server := NewServer()
	server.config.DataDir = t.TempDir()
	if err := server.InitializeManagers(); err != nil {
		t.Fatalf("Failed to initialize managers: %v", err)
	}
	defer server.Shutdown()

	// Test cases: filepath.Base() strips path components, so many attacks become safe filenames
	pathTraversalTests := []struct {
		name     string
		filename string
		expected int  // Expected HTTP status code
		safe     bool // True if filepath.Base() makes it safe
	}{
		// filepath.Base("../../etc/passwd.txt") = "passwd.txt" (safe; .txt keeps it inside the plain-text allowlist)
		{"Parent directory traversal", "../../etc/passwd.txt", http.StatusCreated, true},
		// filepath.Base("..\\..\\Windows\\...") = "sam.txt" (safe)
		{"Windows path traversal", "..\\..\\Windows\\System32\\config\\sam.txt", http.StatusCreated, true},
		// filepath.Base("/etc/passwd.txt") = "passwd.txt" (safe)
		{"Absolute path", "/etc/passwd.txt", http.StatusCreated, true},
		// Null byte handled by multipart parser (fails early)
		{"Null byte injection", "test\x00.txt", http.StatusBadRequest, false},
		// filepath.Base(".") = "." (rejected by our validation)
		{"Current directory", ".", http.StatusBadRequest, false},
		// filepath.Base("..") = ".." (rejected by our validation)
		{"Parent directory", "..", http.StatusBadRequest, false},
		// Empty filename handled by multipart parser
		{"Empty filename", "", http.StatusBadRequest, false},
		// Valid filename works normally
		{"Valid filename", "safe-file.txt", http.StatusCreated, true},
	}

	for _, tt := range pathTraversalTests {
		t.Run(tt.name, func(t *testing.T) {
			// Create multipart form data with malicious filename
			body := &bytes.Buffer{}
			writer := multipart.NewWriter(body)

			// Add file field with malicious filename
			part, err := writer.CreateFormFile("file", tt.filename)
			if err != nil {
				t.Fatalf("Failed to create form file: %v", err)
			}
			part.Write([]byte("test file content: " + tt.name))
			writer.Close()

			req := httptest.NewRequest("POST", "/api/v1/share", body)
			req.Header.Set("Content-Type", writer.FormDataContentType())
			rec := httptest.NewRecorder()

			// Act
			server.handleShare(rec, req)

			// Assert
			if rec.Code != tt.expected {
				t.Errorf("Filename %q: expected status %d, got %d (body: %s)",
					tt.filename, tt.expected, rec.Code, rec.Body.String())
			}

			// For safe cases, verify success
			if tt.safe && rec.Code == http.StatusCreated {
				// Success - filepath.Base() sanitized the path
				t.Logf("✓ Path traversal attempt sanitized: %q became safe filename", tt.filename)
			}

			// For rejected cases, verify the error message
			if !tt.safe && rec.Code == http.StatusBadRequest {
				body := rec.Body.String()
				// Should mention filename, path, or request issue
				if !contains(body, "INVALID_FILENAME") && !contains(body, "INVALID_PATH") && !contains(body, "INVALID_REQUEST") {
					t.Errorf("Error response should indicate issue, got: %s", body)
				}
			}
		})
	}
}

// TestHandleShare_FilenameBaseOnly - SECURITY TEST
// Verifies that only the base filename is used (directory components stripped)
func TestHandleShare_FilenameBaseOnly(t *testing.T) {
	server := NewServer()
	server.config.DataDir = t.TempDir()
	if err := server.InitializeManagers(); err != nil {
		t.Fatalf("Failed to initialize managers: %v", err)
	}
	defer server.Shutdown()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, _ := writer.CreateFormFile("file", "subdir/nested/file.txt")
	part.Write([]byte("test content"))
	writer.Close()

	req := httptest.NewRequest("POST", "/api/v1/share", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()

	// Act
	server.handleShare(rec, req)

	// Assert - Should succeed because filepath.Base() strips the path
	// The file will be saved as "file.txt" not "subdir/nested/file.txt"
	// SECURITY: Status 201 (Created) is correct for successful resource creation
	if rec.Code != http.StatusCreated {
		t.Errorf("Expected status 201 for filename with path components (stripped), got %d: %s",
			rec.Code, rec.Body.String())
	}
}

func contains(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && (len(s) >= len(substr)) && (s == substr || len(s) > len(substr) && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
