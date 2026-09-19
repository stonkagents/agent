// Package: tracker/internal/services
// Feature: StonkAgents launchpad (workstream D — server-side token metadata)
// Purpose: Pinata adapter for MetadataUploader using the v1 pinning API
//          (POST /pinning/pinFileToIPFS, POST /pinning/pinJSONToIPFS) — verified
//          current (not deprecated) at docs.pinata.cloud on 2026-09-12. Same
//          contract the portal used client-side, so the existing JWT keeps working.
//          The JWT is only ever placed in the Authorization header; it is never
//          logged or included in error strings.

package services

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"time"
)

const (
	// PinataAPIBaseURL is the production Pinata REST base.
	PinataAPIBaseURL = "https://api.pinata.cloud"
	// DefaultPinataGatewayURL is the public Pinata gateway used to build HTTPS URIs.
	DefaultPinataGatewayURL = "https://gateway.pinata.cloud/ipfs/"

	pinataFilePath    = "/pinning/pinFileToIPFS"
	pinataJSONPath    = "/pinning/pinJSONToIPFS"
	pinataTimeout     = 30 * time.Second
	pinataErrBodyPeek = 256
)

// PinataUploader implements MetadataUploader against Pinata's v1 pinning API.
type PinataUploader struct {
	jwt     string
	baseURL string
	gateway string
	client  *http.Client
}

// PinataOptions configures a PinataUploader. Zero values fall back to production defaults.
type PinataOptions struct {
	// BaseURL overrides the API base (tests point it at httptest.Server).
	BaseURL string
	// GatewayURL is the public gateway prefix; DefaultPinataGatewayURL when empty.
	GatewayURL string
	// HTTPClient overrides the transport; a 30s-timeout client when nil.
	HTTPClient *http.Client
}

// NewPinataUploader builds a PinataUploader. jwt must be non-empty (enforced by env loading).
func NewPinataUploader(jwt string, opts PinataOptions) *PinataUploader {
	base := strings.TrimRight(opts.BaseURL, "/")
	if base == "" {
		base = PinataAPIBaseURL
	}
	gw := NormalizeGatewayURL(opts.GatewayURL)
	if gw == "" {
		gw = DefaultPinataGatewayURL
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: pinataTimeout}
	}
	return &PinataUploader{jwt: jwt, baseURL: base, gateway: gw, client: client}
}

// pinataPinResponse is the v1 pin response (IpfsHash is the CID).
type pinataPinResponse struct {
	IpfsHash    string `json:"IpfsHash"`
	PinSize     int64  `json:"PinSize"`
	Timestamp   string `json:"Timestamp"`
	IsDuplicate bool   `json:"isDuplicate"`
}

// UploadImage pins the image via multipart pinFileToIPFS (field "file" + pinataMetadata.name).
func (p *PinataUploader) UploadImage(ctx context.Context, filename, contentType string, data []byte) (string, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)

	hdr := make(textproto.MIMEHeader)
	hdr.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="%s"`, sanitizeFilename(filename)))
	hdr.Set("Content-Type", contentType)
	part, err := mw.CreatePart(hdr)
	if err != nil {
		return "", fmt.Errorf("%w: build multipart: %v", ErrUploadFailed, err)
	}
	if _, err := part.Write(data); err != nil {
		return "", fmt.Errorf("%w: build multipart: %v", ErrUploadFailed, err)
	}
	meta, _ := json.Marshal(map[string]any{"name": filename})
	if err := mw.WriteField("pinataMetadata", string(meta)); err != nil {
		return "", fmt.Errorf("%w: build multipart: %v", ErrUploadFailed, err)
	}
	if err := mw.Close(); err != nil {
		return "", fmt.Errorf("%w: build multipart: %v", ErrUploadFailed, err)
	}

	cid, err := p.post(ctx, pinataFilePath, mw.FormDataContentType(), &body)
	if err != nil {
		return "", err
	}
	return p.gateway + cid, nil
}

// UploadJSON pins doc via pinJSONToIPFS ({pinataContent, pinataMetadata:{name}}).
func (p *PinataUploader) UploadJSON(ctx context.Context, name string, doc any) (string, error) {
	payload, err := json.Marshal(map[string]any{
		"pinataContent":  doc,
		"pinataMetadata": map[string]any{"name": name},
		"pinataOptions":  map[string]any{"cidVersion": 0},
	})
	if err != nil {
		return "", fmt.Errorf("%w: marshal metadata: %v", ErrUploadFailed, err)
	}
	cid, err := p.post(ctx, pinataJSONPath, "application/json", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	return p.gateway + cid, nil
}

// post sends an authenticated request and returns the IpfsHash. Error strings
// carry status + a short body excerpt but never the request (and so never the JWT).
func (p *PinataUploader) post(ctx context.Context, path, contentType string, body io.Reader) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+path, body)
	if err != nil {
		return "", fmt.Errorf("%w: build request: %v", ErrUploadFailed, err)
	}
	req.Header.Set("Authorization", "Bearer "+p.jwt)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: pinata %s: transport error", ErrUploadFailed, path)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		peek, _ := io.ReadAll(io.LimitReader(resp.Body, pinataErrBodyPeek))
		return "", fmt.Errorf("%w: pinata %s returned %d: %s", ErrUploadFailed, path, resp.StatusCode, strings.TrimSpace(string(peek)))
	}

	var out pinataPinResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&out); err != nil {
		return "", fmt.Errorf("%w: pinata %s: malformed response", ErrUploadFailed, path)
	}
	if out.IpfsHash == "" {
		return "", fmt.Errorf("%w: pinata %s: empty IpfsHash", ErrUploadFailed, path)
	}
	return out.IpfsHash, nil
}

// sanitizeFilename strips characters that would break the Content-Disposition header.
func sanitizeFilename(name string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '"' || r == '\\' || r == '\r' || r == '\n' || r < 0x20 || r == 0x7f:
			return '_'
		}
		return r
	}, name)
}
