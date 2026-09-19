// Package: internal/daemon
// Purpose: POST /api/v1/share and POST /api/v1/downloads/{cid}/seed enforce the plain-text sharing rule
// (see internal/sharing) and answer 415 UNSUPPORTED_FILE_TYPE otherwise.

package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stonkagents/agent/internal/daemon/storage"
)

func decodeUnsupportedFileType(t *testing.T, rec *httptest.ResponseRecorder) ErrorResponse {
	t.Helper()
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415, body: %s", rec.Code, rec.Body.String())
	}
	var resp ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if resp.Error.Code != "UNSUPPORTED_FILE_TYPE" {
		t.Errorf("code = %q, want UNSUPPORTED_FILE_TYPE", resp.Error.Code)
	}
	if !strings.Contains(resp.Error.Message, "Only plain-text files can be shared") {
		t.Errorf("message = %q, want the plain-text rule", resp.Error.Message)
	}
	return resp
}

func TestHandleShare_RefusesByExtension(t *testing.T) {
	server := newShareTestServer(t)
	for _, name := range []string{"model.safetensors", "photo.png", "archive.zip", "paper.pdf", "README", ".env", "setup.exe"} {
		t.Run(name, func(t *testing.T) {
			req, rec := createShareRequest(t, name, []byte("perfectly fine text"), false)
			server.handleShare(rec, req)
			decodeUnsupportedFileType(t, rec)
		})
	}
	// Nothing was stored
	files, err := server.chunkStore.ListFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Errorf("stored %d files, want 0", len(files))
	}
}

func TestHandleShare_RefusesByContent(t *testing.T) {
	server := newShareTestServer(t)
	cases := map[string][]byte{
		"nul.txt":     []byte("text\x00binary"),
		"png.md":      {0x89, 'P', 'N', 'G', '\r', '\n', 0x1A, '\n'},
		"bad-utf8.js": {'v', 'a', 'r', ' ', 0xFF, 0xFE},
		"empty.json":  {},
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			req, rec := createShareRequest(t, name, data, false)
			server.handleShare(rec, req)
			decodeUnsupportedFileType(t, rec)
		})
	}
	files, err := server.chunkStore.ListFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Errorf("stored %d files, want 0", len(files))
	}
	// The temp copy is cleaned up after a refusal
	if ents, _ := os.ReadDir(filepath.Join(server.config.DataDir, "tmp")); len(ents) != 0 {
		t.Errorf("tmp dir has %d leftover files, want 0", len(ents))
	}
}

func TestHandleShare_AcceptsPlainTextAndUppercaseExtension(t *testing.T) {
	server := newShareTestServer(t)
	for _, name := range []string{"notes.txt", "NOTES.MD", "data.JSON", "script.py", "table.csv"} {
		req, rec := createShareRequest(t, name, []byte("# "+name+"\nhello 世界\n"), false)
		server.handleShare(rec, req)
		if rec.Code != http.StatusCreated {
			t.Errorf("%s: status = %d, want 201, body: %s", name, rec.Code, rec.Body.String())
		}
	}
}

func TestHandleShare_UnsupportedFileTypeDetails(t *testing.T) {
	server := newShareTestServer(t)
	req, rec := createShareRequest(t, "weights.bin", []byte("x"), false)
	server.handleShare(rec, req)
	resp := decodeUnsupportedFileType(t, rec)
	details, ok := resp.Error.Details.(map[string]interface{})
	if !ok {
		t.Fatalf("details = %T, want object", resp.Error.Details)
	}
	exts, ok := details["allowed_extensions"].([]interface{})
	if !ok || len(exts) == 0 {
		t.Fatalf("allowed_extensions missing from details: %v", details)
	}
	if details["reason"] == "" {
		t.Error("reason missing from details")
	}
}

func TestHandleSeedDownloadByPath_RefusesNonPlainText(t *testing.T) {
	dir := t.TempDir()
	store, err := storage.NewSQLiteChunkStore(filepath.Join(dir, "chunks.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server := &Server{
		config:     &Config{DataDir: dir},
		chunkStore: store,
	}
	cid := "bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi"
	assetDir := filepath.Join(dir, "downloads", "assets", cid)
	if err := os.MkdirAll(assetDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(assetDir, "model.safetensors"), []byte{0, 1, 2, 3}, 0o644); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/downloads/"+cid+"/seed", nil)
	req.SetPathValue("cid", cid)
	rec := httptest.NewRecorder()
	server.handleSeedDownloadByPath(rec, req)
	decodeUnsupportedFileType(t, rec)

	if existing, err := store.GetFile(cid); err == nil && existing != nil {
		t.Error("refused file must not enter the library")
	}
}
