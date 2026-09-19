// Package: tracker/internal/services
// Feature: StonkAgents launchpad (workstream D — server-side token metadata)
// Purpose: Input validation for token launch metadata — text fields, link
//          allowlists, creator wallet, and magic-byte image sniffing. Stable
//          error codes are surfaced to the API layer as 400s.

package services

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	_ "image/jpeg" // thumbnail dimension check
	_ "image/png"  // thumbnail dimension check
	"net"
	"net/http"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/mr-tron/base58"
)

// Stable validation error codes (returned verbatim in API error envelopes).
const (
	MetadataErrInvalidImage       = "invalid_image"
	MetadataErrImageTooLarge      = "image_too_large"
	MetadataErrInvalidName        = "invalid_name"
	MetadataErrInvalidSymbol      = "invalid_symbol"
	MetadataErrInvalidDescription = "invalid_description"
	MetadataErrInvalidURL         = "invalid_url"
	MetadataErrInvalidWallet      = "invalid_wallet"
	MetadataErrInvalidThumbnail   = "invalid_thumbnail"
	MetadataErrThumbnailTooLarge  = "thumbnail_too_large"
)

// Thumbnail limits: the portal renders a 128 px WebP; anything up to 256 px / 256 KiB is accepted.
const (
	MetadataThumbMaxBytes int64 = 256 * 1024
	MetadataThumbMaxSide        = 256
)

// Field limits (bytes, matching on-chain Metaplex/LaunchLab constraints).
const (
	MetadataNameMaxBytes        = 32
	MetadataSymbolMaxBytes      = 10
	MetadataDescriptionMaxBytes = 1000
	metadataURLMaxBytes         = 256
	solanaPubkeyLen             = 32
)

// MetadataValidationError is a client error with a stable machine code.
type MetadataValidationError struct {
	Code    string
	Message string
}

func (e *MetadataValidationError) Error() string { return e.Code + ": " + e.Message }

func validationErr(code, format string, args ...any) error {
	return &MetadataValidationError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// allowedImageTypes maps sniffed MIME → file extension used for the pin name.
var allowedImageTypes = map[string]string{
	"image/png":  "png",
	"image/jpeg": "jpg",
	"image/webp": "webp",
	"image/gif":  "gif",
}

// SniffImageType detects the image type from magic bytes (never from the
// client-supplied filename or Content-Type). Returns MIME + extension.
func SniffImageType(data []byte) (mime, ext string, ok bool) {
	if len(data) == 0 {
		return "", "", false
	}
	detected := http.DetectContentType(data)
	ext, ok = allowedImageTypes[detected]
	if !ok {
		return "", "", false
	}
	return detected, ext, true
}

// hasControlRunes reports whether s contains any Unicode control character.
// allowWhitespace permits \n, \r and \t (used for multi-line descriptions).
func hasControlRunes(s string, allowWhitespace bool) bool {
	for _, r := range s {
		if !unicode.IsControl(r) {
			continue
		}
		if allowWhitespace && (r == '\n' || r == '\r' || r == '\t') {
			continue
		}
		return true
	}
	return false
}

// cleanText trims surrounding whitespace and enforces UTF-8 + control-char rules.
func cleanText(raw, code string, minBytes, maxBytes int, allowWhitespace bool) (string, error) {
	s := strings.TrimSpace(raw)
	if !utf8.ValidString(s) {
		return "", validationErr(code, "must be valid UTF-8")
	}
	if hasControlRunes(s, allowWhitespace) {
		return "", validationErr(code, "must not contain control characters")
	}
	if len(s) < minBytes {
		return "", validationErr(code, "must be at least %d byte(s)", minBytes)
	}
	if len(s) > maxBytes {
		return "", validationErr(code, "must be at most %d bytes", maxBytes)
	}
	return s, nil
}

var (
	twitterHosts  = map[string]bool{"twitter.com": true, "www.twitter.com": true, "mobile.twitter.com": true, "x.com": true, "www.x.com": true}
	telegramHosts = map[string]bool{"t.me": true, "www.t.me": true, "telegram.me": true, "telegram.dog": true}
)

// cleanURL validates an optional https link. hostAllow==nil accepts any public host.
func cleanURL(raw, field string, hostAllow map[string]bool) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", nil
	}
	if len(s) > metadataURLMaxBytes || !utf8.ValidString(s) || hasControlRunes(s, false) {
		return "", validationErr(MetadataErrInvalidURL, "%s: malformed URL", field)
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Host == "" || u.Opaque != "" {
		return "", validationErr(MetadataErrInvalidURL, "%s: must be an https URL", field)
	}
	host := strings.ToLower(u.Hostname())
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".local") || net.ParseIP(host) != nil {
		return "", validationErr(MetadataErrInvalidURL, "%s: host not allowed", field)
	}
	if hostAllow != nil {
		if !hostAllow[host] {
			return "", validationErr(MetadataErrInvalidURL, "%s: host not allowed", field)
		}
	} else if !strings.Contains(host, ".") {
		return "", validationErr(MetadataErrInvalidURL, "%s: host must be a public domain", field)
	}
	return s, nil
}

// cleanWallet validates an optional base58 Solana pubkey (32 bytes decoded).
func cleanWallet(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", nil
	}
	if len(s) < 32 || len(s) > 44 {
		return "", validationErr(MetadataErrInvalidWallet, "creatorWallet: must be a base58 Solana public key")
	}
	b, err := base58.Decode(s)
	if err != nil || len(b) != solanaPubkeyLen {
		return "", validationErr(MetadataErrInvalidWallet, "creatorWallet: must be a base58 Solana public key")
	}
	return s, nil
}

// validateLaunchMetadataInput normalises and validates every field; returns a cleaned copy.
func validateLaunchMetadataInput(in LaunchMetadataInput, maxImageBytes int64) (LaunchMetadataInput, string, string, error) {
	var out LaunchMetadataInput
	var err error

	if out.Name, err = cleanText(in.Name, MetadataErrInvalidName, 1, MetadataNameMaxBytes, false); err != nil {
		return out, "", "", err
	}
	if out.Symbol, err = cleanText(in.Symbol, MetadataErrInvalidSymbol, 1, MetadataSymbolMaxBytes, false); err != nil {
		return out, "", "", err
	}
	if out.Description, err = cleanText(in.Description, MetadataErrInvalidDescription, 0, MetadataDescriptionMaxBytes, true); err != nil {
		return out, "", "", err
	}
	if out.Website, err = cleanURL(in.Website, "website", nil); err != nil {
		return out, "", "", err
	}
	if out.Twitter, err = cleanURL(in.Twitter, "twitter", twitterHosts); err != nil {
		return out, "", "", err
	}
	if out.Telegram, err = cleanURL(in.Telegram, "telegram", telegramHosts); err != nil {
		return out, "", "", err
	}
	if out.CreatorWallet, err = cleanWallet(in.CreatorWallet); err != nil {
		return out, "", "", err
	}

	if len(in.Image) == 0 {
		return out, "", "", validationErr(MetadataErrInvalidImage, "image is required")
	}
	if int64(len(in.Image)) > maxImageBytes {
		return out, "", "", validationErr(MetadataErrImageTooLarge, "image exceeds %d bytes", maxImageBytes)
	}
	mime, ext, ok := SniffImageType(in.Image)
	if !ok {
		return out, "", "", validationErr(MetadataErrInvalidImage, "image must be PNG, JPEG, WebP or GIF")
	}
	out.Image = in.Image
	if len(in.Thumbnail) > 0 {
		if err := validateThumbnail(in.Thumbnail); err != nil {
			return out, "", "", err
		}
		out.Thumbnail = in.Thumbnail
	}
	return out, mime, ext, nil
}

// thumbnailTypes are the sniffed MIME types a thumbnail may have (no GIF: a thumb is one frame).
var thumbnailTypes = map[string]string{
	"image/png":  "png",
	"image/jpeg": "jpg",
	"image/webp": "webp",
}

// validateThumbnail checks the optional thumbnail: a PNG/JPEG/WebP of at most
// MetadataThumbMaxBytes whose decoded size is at most MetadataThumbMaxSide on each side.
func validateThumbnail(data []byte) error {
	if int64(len(data)) > MetadataThumbMaxBytes {
		return validationErr(MetadataErrThumbnailTooLarge, "thumbnail exceeds %d bytes", MetadataThumbMaxBytes)
	}
	mime, _, ok := SniffImageType(data)
	if !ok || thumbnailTypes[mime] == "" {
		return validationErr(MetadataErrInvalidThumbnail, "thumbnail must be PNG, JPEG or WebP")
	}
	w, h, err := ImageDimensions(data, mime)
	if err != nil {
		return validationErr(MetadataErrInvalidThumbnail, "thumbnail could not be decoded")
	}
	if w <= 0 || h <= 0 || w > MetadataThumbMaxSide || h > MetadataThumbMaxSide {
		return validationErr(MetadataErrInvalidThumbnail, "thumbnail must be at most %d×%d px (got %d×%d)", MetadataThumbMaxSide, MetadataThumbMaxSide, w, h)
	}
	return nil
}

// ThumbnailType returns the sniffed MIME and pin extension of validated thumbnail bytes.
func ThumbnailType(data []byte) (mime, ext string) {
	mime, _, _ = SniffImageType(data)
	return mime, thumbnailTypes[mime]
}

// ImageDimensions reads the pixel size from the header of a PNG, JPEG or WebP without
// decoding pixels. WebP has no stdlib decoder, so its three container variants are parsed here.
func ImageDimensions(data []byte, mime string) (width, height int, err error) {
	if mime != "image/webp" {
		cfg, _, derr := image.DecodeConfig(bytes.NewReader(data))
		if derr != nil {
			return 0, 0, derr
		}
		return cfg.Width, cfg.Height, nil
	}
	return webpDimensions(data)
}

// webpDimensions parses RIFF/WEBP: "VP8X" (extended, 24-bit canvas size minus one),
// "VP8L" (lossless, 14-bit size minus one) or "VP8 " (lossy key frame, 14-bit size).
func webpDimensions(data []byte) (int, int, error) {
	if len(data) < 30 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WEBP" {
		return 0, 0, fmt.Errorf("webp: not a RIFF/WEBP container")
	}
	chunk := string(data[12:16])
	p := data[20:]
	switch chunk {
	case "VP8X":
		if len(p) < 10 {
			return 0, 0, fmt.Errorf("webp: short VP8X header")
		}
		w := int(p[4]) | int(p[5])<<8 | int(p[6])<<16
		h := int(p[7]) | int(p[8])<<8 | int(p[9])<<16
		return w + 1, h + 1, nil
	case "VP8L":
		if len(p) < 5 || p[0] != 0x2f {
			return 0, 0, fmt.Errorf("webp: bad VP8L signature")
		}
		bits := binary.LittleEndian.Uint32(p[1:5])
		return int(bits&0x3fff) + 1, int((bits>>14)&0x3fff) + 1, nil
	case "VP8 ":
		if len(p) < 10 || p[3] != 0x9d || p[4] != 0x01 || p[5] != 0x2a {
			return 0, 0, fmt.Errorf("webp: bad VP8 key frame start code")
		}
		w := int(binary.LittleEndian.Uint16(p[6:8])) & 0x3fff
		h := int(binary.LittleEndian.Uint16(p[8:10])) & 0x3fff
		return w, h, nil
	}
	return 0, 0, fmt.Errorf("webp: unknown first chunk %q", chunk)
}
