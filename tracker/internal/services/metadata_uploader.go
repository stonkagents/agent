// Package: tracker/internal/services
// Feature: StonkAgents launchpad (workstream D — server-side token metadata)
// Purpose: MetadataUploader port + deterministic fake for dev/tests. The Pinata
//          adapter lives in pinata_uploader.go. Keeping the IPFS secret server-side
//          replaces the browser upload that leaked NEXT_PUBLIC_PINATA_JWT.

package services

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/mr-tron/base58"
)

// MetadataUploader pins token launch artefacts (image + metadata JSON) to IPFS
// and returns a public HTTPS gateway URI for each.
type MetadataUploader interface {
	// UploadImage pins raw image bytes and returns the gateway URI.
	UploadImage(ctx context.Context, filename, contentType string, data []byte) (uri string, err error)
	// UploadJSON pins doc (JSON-serialisable) under a human-readable pin name and returns the gateway URI.
	UploadJSON(ctx context.Context, name string, doc any) (uri string, err error)
}

// ErrUploadFailed wraps any transport/provider failure so callers can map it to 502 upload_failed.
var ErrUploadFailed = errors.New("metadata upload failed")

// NormalizeGatewayURL guarantees a single trailing slash so "gateway + cid" concatenation is stable.
func NormalizeGatewayURL(gateway string) string {
	gateway = strings.TrimSpace(gateway)
	if gateway == "" {
		return ""
	}
	return strings.TrimRight(gateway, "/") + "/"
}

// FakeMetadataUploader is the development/test uploader. It never touches the
// network and returns deterministic URIs derived from the content hash, so the
// same input always yields the same "CID" (handy for snapshot tests).
type FakeMetadataUploader struct {
	gateway string
	// FailWith, when non-nil, is returned from both Upload methods (simulates provider outage).
	FailWith error

	mu     sync.Mutex
	images []FakeUploadRecord
	docs   []FakeUploadRecord
}

// FakeUploadRecord captures one fake upload for assertions.
type FakeUploadRecord struct {
	Name        string
	ContentType string
	Bytes       []byte
	URI         string
}

// NewFakeMetadataUploader returns a fake uploader that prefixes URIs with gateway.
func NewFakeMetadataUploader(gateway string) *FakeMetadataUploader {
	return &FakeMetadataUploader{gateway: NormalizeGatewayURL(gateway)}
}

// fakeCID builds a CIDv0-looking identifier ("Qm" + base58 of sha256) that is
// deterministic per payload. It is NOT a real IPFS CID — it only needs to be
// stable and recognisably fake.
func fakeCID(data []byte) string {
	sum := sha256.Sum256(data)
	return "Qmfake" + base58.Encode(sum[:20])
}

// UploadImage implements MetadataUploader.
func (f *FakeMetadataUploader) UploadImage(_ context.Context, filename, contentType string, data []byte) (string, error) {
	if f.FailWith != nil {
		return "", fmt.Errorf("%w: %v", ErrUploadFailed, f.FailWith)
	}
	uri := f.gateway + fakeCID(data)
	f.mu.Lock()
	f.images = append(f.images, FakeUploadRecord{Name: filename, ContentType: contentType, Bytes: data, URI: uri})
	f.mu.Unlock()
	return uri, nil
}

// UploadJSON implements MetadataUploader.
func (f *FakeMetadataUploader) UploadJSON(_ context.Context, name string, doc any) (string, error) {
	if f.FailWith != nil {
		return "", fmt.Errorf("%w: %v", ErrUploadFailed, f.FailWith)
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("%w: marshal metadata: %v", ErrUploadFailed, err)
	}
	uri := f.gateway + fakeCID(raw)
	f.mu.Lock()
	f.docs = append(f.docs, FakeUploadRecord{Name: name, ContentType: "application/json", Bytes: raw, URI: uri})
	f.mu.Unlock()
	return uri, nil
}

// Images returns a copy of recorded image uploads.
func (f *FakeMetadataUploader) Images() []FakeUploadRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]FakeUploadRecord(nil), f.images...)
}

// Docs returns a copy of recorded JSON uploads.
func (f *FakeMetadataUploader) Docs() []FakeUploadRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]FakeUploadRecord(nil), f.docs...)
}
