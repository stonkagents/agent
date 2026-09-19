// Package: tracker/internal/api
// Feature: StonkAgents launchpad (workstream D — server-side token metadata)
// Purpose: Handler tests for POST /api/launch/metadata — multipart and JSON
//          variants, stable error codes, 502 on provider failure, body limits,
//          middleware multipart exemption, and full-router wiring + rate limit.

package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/clock"
	"github.com/stonkagents/agent/tracker/internal/ratelimit"
	"github.com/stonkagents/agent/tracker/internal/services"
)

var (
	testPNG    = append([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, make([]byte, 32)...)
	testWallet = "7cVfgArCheMR6Cs4t6vz5rfnqd56vZq4ndaBrY5xkxXy"
)

const testGateway = "https://gateway.pinata.cloud/ipfs/"

func newLaunchMetadataHandler(t *testing.T, maxImage int64) (*LaunchMetadataHandler, *services.FakeMetadataUploader) {
	t.Helper()
	fake := services.NewFakeMetadataUploader(testGateway)
	svc := services.NewLaunchMetadataService(fake, services.LaunchMetadataOptions{MaxImageBytes: maxImage, GatewayURL: testGateway})
	return NewLaunchMetadataHandler(svc), fake
}

type mpOpts struct {
	fields        map[string]string
	image         []byte
	imageName     string
	noImage       bool
	thumbnail     []byte
	thumbnailName string
}

func buildMultipart(t *testing.T, o mpOpts) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range o.fields {
		_ = mw.WriteField(k, v)
	}
	if !o.noImage {
		name := o.imageName
		if name == "" {
			name = "logo.png"
		}
		part, err := mw.CreateFormFile("image", name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = part.Write(o.image)
	}
	if o.thumbnail != nil {
		part, err := mw.CreateFormFile("thumbnail", o.thumbnailName)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = part.Write(o.thumbnail)
	}
	_ = mw.Close()
	return &buf, mw.FormDataContentType()
}

func defaultFields() map[string]string {
	return map[string]string{
		"name": "Stonk Agent", "symbol": "STONK", "description": "stonks",
		"website": "https://stonkagents.fun", "twitter": "https://x.com/stonk", "telegram": "https://t.me/stonk",
		"creatorWallet": testWallet,
	}
}

func decodeErr(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var env ErrorEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode error envelope: %v (%s)", err, w.Body.String())
	}
	return env.Error.Code
}

func TestLaunchMetadata_Multipart_HappyPath(t *testing.T) {
	h, fake := newLaunchMetadataHandler(t, 1024)
	body, ct := buildMultipart(t, mpOpts{fields: defaultFields(), image: testPNG})
	req := httptest.NewRequest(http.MethodPost, LaunchMetadataPath, body)
	req.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()
	h.HandleUpload(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data services.LaunchMetadataResult `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	d := resp.Data
	if !strings.HasPrefix(d.ImageURI, testGateway) || !strings.HasPrefix(d.MetadataURI, testGateway) {
		t.Errorf("uris = %q %q", d.ImageURI, d.MetadataURI)
	}
	if d.ImageCID == "" || d.MetadataCID == "" || d.ImageBytes != len(testPNG) || d.ContentType != "image/png" {
		t.Errorf("result = %+v", d)
	}
	docs := fake.Docs()
	if len(docs) != 1 {
		t.Fatalf("docs = %d", len(docs))
	}
	var doc map[string]any
	_ = json.Unmarshal(docs[0].Bytes, &doc)
	if doc["name"] != "Stonk Agent" || doc["symbol"] != "STONK" || doc["image"] != d.ImageURI || doc["showName"] != true || doc["creatorWallet"] != testWallet {
		t.Errorf("uploaded doc = %v", doc)
	}
}

func TestLaunchMetadata_JSON_HappyPath(t *testing.T) {
	h, _ := newLaunchMetadataHandler(t, 1024)
	payload := map[string]any{
		"name": "Stonk Agent", "symbol": "STONK", "description": "stonks",
		"imageDataUrl": "data:image/png;base64," + base64.StdEncoding.EncodeToString(testPNG),
	}
	raw, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, LaunchMetadataPath, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleUpload(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"metadataUri":"`+testGateway) {
		t.Errorf("body = %s", w.Body.String())
	}
}

func TestLaunchMetadata_ErrorCodes(t *testing.T) {
	h, _ := newLaunchMetadataHandler(t, 64)
	with := func(k, v string) map[string]string { f := defaultFields(); f[k] = v; return f }

	cases := []struct {
		name   string
		opts   mpOpts
		status int
		code   string
	}{
		{"invalid_name", mpOpts{fields: with("name", ""), image: testPNG}, 400, "invalid_name"},
		{"invalid_symbol", mpOpts{fields: with("symbol", "TOOLONGSYMBOL"), image: testPNG}, 400, "invalid_symbol"},
		{"invalid_url", mpOpts{fields: with("twitter", "https://evil.com/x"), image: testPNG}, 400, "invalid_url"},
		{"invalid_wallet", mpOpts{fields: with("creatorWallet", "nope"), image: testPNG}, 400, "invalid_wallet"},
		{"invalid_image sniff", mpOpts{fields: defaultFields(), image: []byte("<svg/>"), imageName: "x.png"}, 400, "invalid_image"},
		{"invalid_image missing", mpOpts{fields: defaultFields(), noImage: true}, 400, "invalid_image"},
		{"image_too_large", mpOpts{fields: defaultFields(), image: append(append([]byte{}, testPNG...), make([]byte, 64)...)}, 400, "image_too_large"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, ct := buildMultipart(t, tc.opts)
			req := httptest.NewRequest(http.MethodPost, LaunchMetadataPath, body)
			req.Header.Set("Content-Type", ct)
			w := httptest.NewRecorder()
			h.HandleUpload(w, req)
			if w.Code != tc.status {
				t.Fatalf("status = %d, want %d (%s)", w.Code, tc.status, w.Body.String())
			}
			if got := decodeErr(t, w); got != tc.code {
				t.Fatalf("code = %q, want %q", got, tc.code)
			}
		})
	}
}

func TestLaunchMetadata_JSON_DataURLVariants(t *testing.T) {
	h, _ := newLaunchMetadataHandler(t, 64)
	b64 := base64.StdEncoding.EncodeToString(testPNG)
	cases := []struct {
		name   string
		body   string
		status int
		code   string
	}{
		{"malformed json", `{"name":`, 400, "invalid_request"},
		{"missing data url", `{"name":"a","symbol":"A"}`, 400, "invalid_image"},
		{"not a data url", `{"name":"a","symbol":"A","imageDataUrl":"https://x/y.png"}`, 400, "invalid_image"},
		{"non-image data url", `{"name":"a","symbol":"A","imageDataUrl":"data:text/plain;base64,aGk="}`, 400, "invalid_image"},
		{"bad base64", `{"name":"a","symbol":"A","imageDataUrl":"data:image/png;base64,!!!notb64"}`, 400, "invalid_image"},
		{"mime lies, bytes sniffed", `{"name":"a","symbol":"A","imageDataUrl":"data:image/png;base64,` + base64.StdEncoding.EncodeToString([]byte("<svg xmlns='x'/>")) + `"}`, 400, "invalid_image"},
		{"too large", `{"name":"a","symbol":"A","imageDataUrl":"data:image/png;base64,` + base64.StdEncoding.EncodeToString(append(append([]byte{}, testPNG...), make([]byte, 100)...)) + `"}`, 400, "image_too_large"},
		{"ok jpeg declared as png", `{"name":"a","symbol":"A","imageDataUrl":"data:image/jpeg;base64,` + b64 + `"}`, 200, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, LaunchMetadataPath, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			h.HandleUpload(w, req)
			if w.Code != tc.status {
				t.Fatalf("status = %d, want %d (%s)", w.Code, tc.status, w.Body.String())
			}
			if tc.code != "" && decodeErr(t, w) != tc.code {
				t.Fatalf("code = %q, want %q", decodeErr(t, w), tc.code)
			}
		})
	}
}

func TestLaunchMetadata_UploadFailure502(t *testing.T) {
	h, fake := newLaunchMetadataHandler(t, 1024)
	fake.FailWith = errors.New("pinata 500")
	body, ct := buildMultipart(t, mpOpts{fields: defaultFields(), image: testPNG})
	req := httptest.NewRequest(http.MethodPost, LaunchMetadataPath, body)
	req.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()
	h.HandleUpload(w, req)
	if w.Code != http.StatusBadGateway || decodeErr(t, w) != "upload_failed" {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
}

func TestLaunchMetadata_UnsupportedMediaTypeAndBodyLimit(t *testing.T) {
	h, _ := newLaunchMetadataHandler(t, 1024)
	req := httptest.NewRequest(http.MethodPost, LaunchMetadataPath, strings.NewReader("x=y"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.HandleUpload(w, req)
	if w.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d", w.Code)
	}

	h.maxBodyBytes = 256
	body, ct := buildMultipart(t, mpOpts{fields: defaultFields(), image: make([]byte, 512)})
	req = httptest.NewRequest(http.MethodPost, LaunchMetadataPath, body)
	req.Header.Set("Content-Type", ct)
	w = httptest.NewRecorder()
	h.HandleUpload(w, req)
	if w.Code != http.StatusRequestEntityTooLarge || decodeErr(t, w) != "payload_too_large" {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
}

// TestContentTypeMiddleware_MultipartOnlyForLaunchMetadata: the global JSON-only
// guard must let multipart through for this route and nothing else.
func TestContentTypeMiddleware_MultipartOnlyForLaunchMetadata(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	mw := ContentTypeMiddleware(next)

	req := httptest.NewRequest(http.MethodPost, LaunchMetadataPath, nil)
	req.Header.Set("Content-Type", "multipart/form-data; boundary=abc")
	w := httptest.NewRecorder()
	mw.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("launch metadata multipart: status = %d, want 200", w.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/tracker/register", nil)
	req.Header.Set("Content-Type", "multipart/form-data; boundary=abc")
	w = httptest.NewRecorder()
	mw.ServeHTTP(w, req)
	if w.Code != http.StatusUnsupportedMediaType {
		t.Errorf("other route multipart: status = %d, want 415", w.Code)
	}
}

// TestLaunchMetadata_RouterWiring drives the real router: middleware chain,
// route registration, {data} envelope and the 10/min/IP limiter.
func TestLaunchMetadata_RouterWiring(t *testing.T) {
	h, _ := newLaunchMetadataHandler(t, 1024)
	clk := clock.NewMockClock(time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC))
	srv := NewServer(ServerDeps{
		PeerHandler:           newTestServer_peerHandler(t),
		LaunchMetadataHandler: h,
		Limiter:               ratelimit.NewMemoryLimiter(clk),
	})
	for i := 0; i < 11; i++ {
		body, ct := buildMultipart(t, mpOpts{fields: defaultFields(), image: testPNG})
		req := httptest.NewRequest(http.MethodPost, LaunchMetadataPath, body)
		req.Header.Set("Content-Type", ct)
		req.RemoteAddr = "203.0.113.9:4444"
		w := httptest.NewRecorder()
		srv.Router().ServeHTTP(w, req)
		want := http.StatusOK
		if i == 10 {
			want = http.StatusTooManyRequests
		}
		if w.Code != want {
			t.Fatalf("request %d: status = %d, want %d (%s)", i+1, w.Code, want, w.Body.String())
		}
	}

	// Without the handler the route must not exist.
	bare := NewServer(ServerDeps{PeerHandler: newTestServer_peerHandler(t)})
	req := httptest.NewRequest(http.MethodPost, LaunchMetadataPath, strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	bare.Router().ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("unwired: status = %d, want 404", w.Code)
	}
}
