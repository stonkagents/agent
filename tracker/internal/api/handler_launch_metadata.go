// Package: tracker/internal/api
// Feature: StonkAgents launchpad (workstream D — server-side token metadata)
// Purpose: POST /api/launch/metadata — public, IP rate-limited endpoint that
//          pins a token image + metadata JSON to IPFS on behalf of the static
//          portal (which can't hold the Pinata secret). Accepts multipart/form-data
//          or application/json with a base64 data URL image.

package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"

	"github.com/stonkagents/agent/tracker/internal/services"
)

// LaunchMetadataPath is the portal-facing route (under the /api subrouter).
const LaunchMetadataPath = "/api/launch/metadata"

// LaunchMetadataMaxBodyBytes caps the whole request: a 2 MiB image base64-encoded
// (~2.8 MiB) plus a 256 KiB thumbnail (~350 KiB) and the JSON fields fits under 4 MiB.
const LaunchMetadataMaxBodyBytes int64 = 4 * 1024 * 1024

// LaunchMetadataHandler serves POST /api/launch/metadata.
type LaunchMetadataHandler struct {
	svc          *services.LaunchMetadataService
	maxBodyBytes int64
}

// NewLaunchMetadataHandler wires the handler to the service.
func NewLaunchMetadataHandler(svc *services.LaunchMetadataService) *LaunchMetadataHandler {
	return &LaunchMetadataHandler{svc: svc, maxBodyBytes: LaunchMetadataMaxBodyBytes}
}

// launchMetadataJSONRequest is the application/json variant.
type launchMetadataJSONRequest struct {
	Name          string `json:"name"`
	Symbol        string `json:"symbol"`
	Description   string `json:"description"`
	Website       string `json:"website"`
	Twitter       string `json:"twitter"`
	Telegram      string `json:"telegram"`
	CreatorWallet string `json:"creatorWallet"`
	ImageDataURL  string `json:"imageDataUrl"`
	// ThumbnailDataURL is optional: a ≤ 256 px copy of the image, pinned as the launch thumbnail.
	ThumbnailDataURL string `json:"thumbnailDataUrl"`
}

// HandleUpload validates the request, pins image + metadata, and returns
// {data: {imageUri, metadataUri, imageCid, metadataCid, bytes, metadataBytes, contentType,
// thumbnailUri?, thumbnailCid?, thumbnailBytes?}}.
func (h *LaunchMetadataHandler) HandleUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, h.maxBodyBytes)

	mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	var (
		in  services.LaunchMetadataInput
		err error
	)
	switch mediaType {
	case "multipart/form-data":
		in, err = h.parseMultipart(r)
	case "application/json":
		in, err = h.parseJSON(r)
	default:
		SendError(w, http.StatusUnsupportedMediaType, "UNSUPPORTED_MEDIA_TYPE", "Content-Type must be multipart/form-data or application/json")
		return
	}
	if err != nil {
		writeLaunchMetadataError(w, err)
		return
	}

	res, err := h.svc.Upload(r.Context(), in)
	if err != nil {
		writeLaunchMetadataError(w, err)
		return
	}
	SendData(w, res)
}

// parseMultipart reads fields + the "image" file part. The image is bounded to
// MaxImageBytes+1 so oversize files surface as image_too_large, not a hard 413.
func (h *LaunchMetadataHandler) parseMultipart(r *http.Request) (services.LaunchMetadataInput, error) {
	var in services.LaunchMetadataInput
	if err := r.ParseMultipartForm(h.maxBodyBytes); err != nil {
		return in, classifyBodyError(err)
	}
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()

	in.Name = r.FormValue("name")
	in.Symbol = r.FormValue("symbol")
	in.Description = r.FormValue("description")
	in.Website = r.FormValue("website")
	in.Twitter = r.FormValue("twitter")
	in.Telegram = r.FormValue("telegram")
	in.CreatorWallet = r.FormValue("creatorWallet")

	file, _, err := r.FormFile("image")
	if err != nil {
		if errors.Is(err, http.ErrMissingFile) {
			return in, &services.MetadataValidationError{Code: services.MetadataErrInvalidImage, Message: "image file part is required"}
		}
		return in, classifyBodyError(err)
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, h.svc.MaxImageBytes()+1))
	if err != nil {
		return in, classifyBodyError(err)
	}
	in.Image = data

	// The thumbnail part is optional; a present-but-oversize one is thumbnail_too_large.
	thumb, _, err := r.FormFile("thumbnail")
	if err == nil {
		defer thumb.Close()
		tdata, rerr := io.ReadAll(io.LimitReader(thumb, services.MetadataThumbMaxBytes+1))
		if rerr != nil {
			return in, classifyBodyError(rerr)
		}
		in.Thumbnail = tdata
	} else if !errors.Is(err, http.ErrMissingFile) {
		return in, classifyBodyError(err)
	}
	return in, nil
}

// parseJSON reads the JSON variant and decodes imageDataUrl (data:image/*;base64,...).
func (h *LaunchMetadataHandler) parseJSON(r *http.Request) (services.LaunchMetadataInput, error) {
	var in services.LaunchMetadataInput
	var req launchMetadataJSONRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return in, classifyBodyError(err)
	}
	in = services.LaunchMetadataInput{
		Name: req.Name, Symbol: req.Symbol, Description: req.Description,
		Website: req.Website, Twitter: req.Twitter, Telegram: req.Telegram,
		CreatorWallet: req.CreatorWallet,
	}
	if strings.TrimSpace(req.ImageDataURL) == "" {
		return in, &services.MetadataValidationError{Code: services.MetadataErrInvalidImage, Message: "imageDataUrl is required"}
	}
	data, err := decodeImageDataURL(req.ImageDataURL, h.svc.MaxImageBytes())
	if err != nil {
		return in, err
	}
	in.Image = data
	if strings.TrimSpace(req.ThumbnailDataURL) != "" {
		thumb, err := decodeDataURL(req.ThumbnailDataURL, "thumbnailDataUrl", services.MetadataThumbMaxBytes,
			services.MetadataErrInvalidThumbnail, services.MetadataErrThumbnailTooLarge)
		if err != nil {
			return in, err
		}
		in.Thumbnail = thumb
	}
	return in, nil
}

// decodeImageDataURL parses "data:image/<sub>;base64,<payload>". The declared
// MIME type is ignored for trust purposes — the service sniffs magic bytes.
func decodeImageDataURL(s string, maxBytes int64) ([]byte, error) {
	return decodeDataURL(s, "imageDataUrl", maxBytes, services.MetadataErrInvalidImage, services.MetadataErrImageTooLarge)
}

// decodeDataURL is decodeImageDataURL with the field name and error codes parameterised,
// so the thumbnail reports thumbnail_* codes.
func decodeDataURL(s, field string, maxBytes int64, invalidCode, tooLargeCode string) ([]byte, error) {
	invalid := &services.MetadataValidationError{Code: invalidCode, Message: field + " must be a base64 data:image/* URL"}
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(strings.ToLower(s), "data:") {
		return nil, invalid
	}
	comma := strings.IndexByte(s, ',')
	if comma < 0 {
		return nil, invalid
	}
	header := strings.ToLower(s[len("data:"):comma])
	if !strings.HasPrefix(header, "image/") || !strings.HasSuffix(header, ";base64") {
		return nil, invalid
	}
	payload := s[comma+1:]
	// Base64 expands 4:3, so anything beyond this can't decode under the cap.
	if int64(len(payload)) > (maxBytes+3)/3*4+4 {
		return nil, &services.MetadataValidationError{Code: tooLargeCode, Message: field + " exceeds size limit"}
	}
	dec := base64.NewDecoder(base64.StdEncoding, strings.NewReader(payload))
	data, err := io.ReadAll(io.LimitReader(dec, maxBytes+1))
	if err != nil {
		return nil, invalid
	}
	return data, nil
}

// classifyBodyError maps transport/parse failures to a client error.
func classifyBodyError(err error) error {
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) || strings.Contains(err.Error(), "request body too large") {
		return &payloadTooLargeError{}
	}
	return &services.MetadataValidationError{Code: "invalid_request", Message: "malformed request body"}
}

// payloadTooLargeError → 413 (whole request exceeded LaunchMetadataMaxBodyBytes).
type payloadTooLargeError struct{}

func (*payloadTooLargeError) Error() string { return "payload too large" }

// writeLaunchMetadataError maps service/parse errors to stable API responses.
func writeLaunchMetadataError(w http.ResponseWriter, err error) {
	var verr *services.MetadataValidationError
	var tooLarge *payloadTooLargeError
	switch {
	case errors.As(err, &verr):
		SendError(w, http.StatusBadRequest, verr.Code, verr.Message)
	case errors.As(err, &tooLarge):
		SendError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "request body exceeds limit")
	case errors.Is(err, services.ErrUploadFailed):
		slog.Error("[launch-metadata] upload failed", "error", err.Error())
		SendError(w, http.StatusBadGateway, "upload_failed", "metadata upload provider failed")
	default:
		slog.Error("[launch-metadata] unexpected error", "error", err.Error())
		SendError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "unexpected error")
	}
}
