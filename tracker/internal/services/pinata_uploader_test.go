// Package: tracker/internal/services
// Feature: StonkAgents launchpad (workstream D — server-side token metadata)
// Purpose: Wire-contract tests for PinataUploader against the v1 pinning API
//          (pinFileToIPFS multipart + pinJSONToIPFS JSON), incl. secret hygiene.

package services

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testJWT = "super-secret-jwt-do-not-leak"

func TestPinataUploader_UploadImage_Contract(t *testing.T) {
	var gotAuth, gotName, gotType, gotMeta string
	var gotBytes []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/pinning/pinFileToIPFS" || r.Method != http.MethodPost {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("multipart parse: %v", err)
		}
		f, hdr, err := r.FormFile("file")
		if err != nil {
			t.Fatalf("missing file part: %v", err)
		}
		defer f.Close()
		gotBytes, _ = io.ReadAll(f)
		gotName = hdr.Filename
		gotType = hdr.Header.Get("Content-Type")
		gotMeta = r.FormValue("pinataMetadata")
		_ = json.NewEncoder(w).Encode(map[string]any{"IpfsHash": "QmImageCID", "PinSize": len(gotBytes), "Timestamp": "2026-09-12T00:00:00Z"})
	}))
	defer srv.Close()

	up := NewPinataUploader(testJWT, PinataOptions{BaseURL: srv.URL, GatewayURL: "https://gw.example/ipfs"})
	uri, err := up.UploadImage(context.Background(), "token-image.png", "image/png", pngBytes)
	if err != nil {
		t.Fatalf("UploadImage() error = %v", err)
	}
	if uri != "https://gw.example/ipfs/QmImageCID" {
		t.Errorf("uri = %q", uri)
	}
	if gotAuth != "Bearer "+testJWT {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotName != "token-image.png" || gotType != "image/png" || string(gotBytes) != string(pngBytes) {
		t.Errorf("file part = (%q, %q, %d bytes)", gotName, gotType, len(gotBytes))
	}
	if !strings.Contains(gotMeta, `"name":"token-image.png"`) {
		t.Errorf("pinataMetadata = %q", gotMeta)
	}
}

func TestPinataUploader_UploadJSON_Contract(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/pinning/pinJSONToIPFS" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected %s %s ct=%s", r.Method, r.URL.Path, r.Header.Get("Content-Type"))
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"IpfsHash": "QmMetaCID"})
	}))
	defer srv.Close()

	up := NewPinataUploader(testJWT, PinataOptions{BaseURL: srv.URL})
	doc := TokenMetadataDoc{Name: "Stonk", Symbol: "STK", Image: "https://x/img", ShowName: true}
	uri, err := up.UploadJSON(context.Background(), "metadata.json", doc)
	if err != nil {
		t.Fatalf("UploadJSON() error = %v", err)
	}
	if uri != DefaultPinataGatewayURL+"QmMetaCID" {
		t.Errorf("uri = %q", uri)
	}
	content, _ := body["pinataContent"].(map[string]any)
	if content["name"] != "Stonk" || content["symbol"] != "STK" || content["showName"] != true {
		t.Errorf("pinataContent = %v", content)
	}
	meta, _ := body["pinataMetadata"].(map[string]any)
	if meta["name"] != "metadata.json" {
		t.Errorf("pinataMetadata = %v", meta)
	}
}

func TestPinataUploader_ErrorsNeverLeakJWT(t *testing.T) {
	cases := map[string]http.HandlerFunc{
		"401": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"Invalid credentials"}`))
		},
		"500":        func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusInternalServerError) },
		"garbage":    func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("not json")) },
		"empty hash": func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"IpfsHash":""}`)) },
	}
	for name, h := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(h)
			defer srv.Close()
			up := NewPinataUploader(testJWT, PinataOptions{BaseURL: srv.URL})
			_, err := up.UploadJSON(context.Background(), "metadata.json", map[string]string{"a": "b"})
			if !errors.Is(err, ErrUploadFailed) {
				t.Fatalf("err = %v, want ErrUploadFailed", err)
			}
			if strings.Contains(err.Error(), testJWT) {
				t.Fatalf("error leaks JWT: %v", err)
			}
		})
	}

	// Transport failure (server closed) must also wrap ErrUploadFailed without the JWT.
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	up := NewPinataUploader(testJWT, PinataOptions{BaseURL: url})
	_, err := up.UploadImage(context.Background(), "x.png", "image/png", pngBytes)
	if !errors.Is(err, ErrUploadFailed) || strings.Contains(err.Error(), testJWT) {
		t.Fatalf("transport err = %v", err)
	}
}

func TestSanitizeFilename(t *testing.T) {
	if got := sanitizeFilename("a\"b\\c\r\nd.png"); got != "a_b_c__d.png" {
		t.Errorf("sanitizeFilename() = %q", got)
	}
}
