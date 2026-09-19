// Package: tracker/internal/services
// Feature: StonkAgents launchpad (workstream D — server-side token metadata)
// Purpose: LaunchMetadataService — validates launch input, pins the image, builds
//          the metadata JSON in the exact shape the portal used to produce
//          client-side (frontend-apps/.../token-launch/metadata.ts), pins it,
//          and returns the gateway URIs for the on-chain create instruction.

package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// LaunchMetadataInput is the raw (untrusted) request payload.
type LaunchMetadataInput struct {
	Name          string
	Symbol        string
	Description   string
	Website       string
	Twitter       string
	Telegram      string
	CreatorWallet string
	// Image is the decoded image bytes; type is sniffed from magic bytes.
	Image []byte
	// Thumbnail is an optional small (≤ 256 px, ≤ 256 KiB) copy of Image, pinned alongside it.
	Thumbnail []byte
}

// MetadataFile is one entry of the Metaplex `properties.files` array.
type MetadataFile struct {
	URI     string `json:"uri"`
	Type    string `json:"type"`
	Purpose string `json:"purpose,omitempty"` // "thumbnail" marks the small copy; the master has none
}

// MetadataProperties is the Metaplex `properties` block; only `files` is written.
type MetadataProperties struct {
	Files []MetadataFile `json:"files"`
}

// MetadataFilePurposeThumbnail tags the thumbnail entry in properties.files.
const MetadataFilePurposeThumbnail = "thumbnail"

// TokenMetadataDoc mirrors the JSON previously built in metadata.ts:
// { name, symbol, description, image, showName: true, twitter?, telegram?, website? }.
// Field order is preserved for byte-stable output. creatorWallet is an extra
// optional hint recorded server-side; consumers that don't know it ignore it.
type TokenMetadataDoc struct {
	Name          string `json:"name"`
	Symbol        string `json:"symbol"`
	Description   string `json:"description"`
	Image         string `json:"image"`
	ShowName      bool   `json:"showName"`
	Twitter       string `json:"twitter,omitempty"`
	Telegram      string `json:"telegram,omitempty"`
	Website       string `json:"website,omitempty"`
	CreatorWallet string `json:"creatorWallet,omitempty"`
	// Properties is only set when a thumbnail was pinned: `image` stays the master URI
	// and the files array lists master + thumbnail, so readers without thumbnails see no change.
	Properties *MetadataProperties `json:"properties,omitempty"`
}

// LaunchMetadataResult is what the API returns inside the {data} envelope.
type LaunchMetadataResult struct {
	ImageURI      string `json:"imageUri"`
	MetadataURI   string `json:"metadataUri"`
	ImageCID      string `json:"imageCid,omitempty"`
	MetadataCID   string `json:"metadataCid,omitempty"`
	ImageBytes    int    `json:"bytes"`
	MetadataBytes int    `json:"metadataBytes"`
	ContentType   string `json:"contentType"`
	// Thumbnail fields are empty when the request carried no thumbnail.
	ThumbnailURI   string `json:"thumbnailUri,omitempty"`
	ThumbnailCID   string `json:"thumbnailCid,omitempty"`
	ThumbnailBytes int    `json:"thumbnailBytes,omitempty"`
}

// LaunchMetadataOptions configures the service.
type LaunchMetadataOptions struct {
	// MaxImageBytes caps the decoded image size (METADATA_MAX_IMAGE_BYTES).
	MaxImageBytes int64
	// GatewayURL is used to derive CIDs from returned URIs (prefix strip).
	GatewayURL string
}

// DefaultMaxImageBytes is 2 MiB.
const DefaultMaxImageBytes int64 = 2 * 1024 * 1024

// LaunchMetadataService orchestrates validation + the two-step IPFS upload.
type LaunchMetadataService struct {
	uploader      MetadataUploader
	maxImageBytes int64
	gateway       string
}

// NewLaunchMetadataService builds the service. uploader must be non-nil.
func NewLaunchMetadataService(uploader MetadataUploader, opts LaunchMetadataOptions) *LaunchMetadataService {
	max := opts.MaxImageBytes
	if max <= 0 {
		max = DefaultMaxImageBytes
	}
	return &LaunchMetadataService{
		uploader:      uploader,
		maxImageBytes: max,
		gateway:       NormalizeGatewayURL(opts.GatewayURL),
	}
}

// MaxImageBytes exposes the configured cap (handlers use it to bound readers).
func (s *LaunchMetadataService) MaxImageBytes() int64 { return s.maxImageBytes }

// BuildMetadataDoc assembles the metadata JSON document from validated input.
// Exported so the JSON-shape snapshot test can pin the contract independently of uploads.
// `files`, when given, becomes properties.files (master first, then the thumbnail).
func BuildMetadataDoc(in LaunchMetadataInput, imageURI string, files ...MetadataFile) TokenMetadataDoc {
	var props *MetadataProperties
	if len(files) > 0 {
		props = &MetadataProperties{Files: files}
	}
	return TokenMetadataDoc{
		Name:          in.Name,
		Symbol:        in.Symbol,
		Description:   in.Description,
		Image:         imageURI,
		ShowName:      true,
		Twitter:       in.Twitter,
		Telegram:      in.Telegram,
		Website:       in.Website,
		CreatorWallet: in.CreatorWallet,
		Properties:    props,
	}
}

// Upload validates the input, pins the image, then pins the metadata JSON.
// Returns *MetadataValidationError for client errors and an ErrUploadFailed-wrapped
// error when the provider fails.
func (s *LaunchMetadataService) Upload(ctx context.Context, raw LaunchMetadataInput) (*LaunchMetadataResult, error) {
	in, mime, ext, err := validateLaunchMetadataInput(raw, s.maxImageBytes)
	if err != nil {
		return nil, err
	}

	imageURI, err := s.uploader.UploadImage(ctx, "token-image."+ext, mime, in.Image)
	if err != nil {
		return nil, wrapUpload("image", err)
	}

	var files []MetadataFile
	var thumbURI, thumbMime string
	if len(in.Thumbnail) > 0 {
		var thumbExt string
		thumbMime, thumbExt = ThumbnailType(in.Thumbnail)
		thumbURI, err = s.uploader.UploadImage(ctx, "token-thumb."+thumbExt, thumbMime, in.Thumbnail)
		if err != nil {
			return nil, wrapUpload("thumbnail", err)
		}
		files = []MetadataFile{
			{URI: imageURI, Type: mime},
			{URI: thumbURI, Type: thumbMime, Purpose: MetadataFilePurposeThumbnail},
		}
	}
	doc := BuildMetadataDoc(in, imageURI, files...)
	metadataURI, err := s.uploader.UploadJSON(ctx, "metadata.json", doc)
	if err != nil {
		return nil, wrapUpload("metadata", err)
	}

	encoded, _ := json.Marshal(doc)
	return &LaunchMetadataResult{
		ImageURI:       imageURI,
		MetadataURI:    metadataURI,
		ImageCID:       s.cidFromURI(imageURI),
		MetadataCID:    s.cidFromURI(metadataURI),
		ImageBytes:     len(in.Image),
		MetadataBytes:  len(encoded),
		ContentType:    mime,
		ThumbnailURI:   thumbURI,
		ThumbnailCID:   s.cidFromURI(thumbURI),
		ThumbnailBytes: len(in.Thumbnail),
	}, nil
}

// cidFromURI strips the gateway prefix; empty when the URI isn't gateway-shaped.
func (s *LaunchMetadataService) cidFromURI(uri string) string {
	if s.gateway == "" || !strings.HasPrefix(uri, s.gateway) {
		return ""
	}
	cid := strings.TrimPrefix(uri, s.gateway)
	if cid == "" || strings.ContainsAny(cid, "/?#") {
		return ""
	}
	return cid
}

func wrapUpload(stage string, err error) error {
	if errors.Is(err, ErrUploadFailed) {
		return fmt.Errorf("%s upload: %w", stage, err)
	}
	return fmt.Errorf("%s upload: %w: %v", stage, ErrUploadFailed, err)
}
