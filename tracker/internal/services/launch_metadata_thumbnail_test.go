// Package: tracker/internal/services
// Feature: StonkAgents launchpad — launch thumbnails
// Purpose: Tests for the optional thumbnail on POST /api/launch/metadata: size and
//          dimension validation, WebP header parsing, the third pin, and the
//          properties.files entry the metadata document gains.

package services

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"testing"
)

// pngOf encodes a solid w×h PNG.
func pngOf(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = 0x80
	}
	img.Set(0, 0, color.NRGBA{R: 0xff, A: 0xff})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// webpLossless builds a RIFF/WEBP container whose first chunk is a VP8L header for w×h.
func webpLossless(w, h int) []byte {
	bits := uint32(w-1) | uint32(h-1)<<14
	payload := make([]byte, 5, 32)
	payload[0] = 0x2f
	binary.LittleEndian.PutUint32(payload[1:5], bits)
	payload = append(payload, make([]byte, 16)...)
	return riffWebP("VP8L", payload)
}

// webpExtended builds a VP8X container with a w×h canvas.
func webpExtended(w, h int) []byte {
	payload := make([]byte, 10, 32)
	payload[0] = 0x10 // alpha flag; no animation
	cw, ch := uint32(w-1), uint32(h-1)
	payload[4], payload[5], payload[6] = byte(cw), byte(cw>>8), byte(cw>>16)
	payload[7], payload[8], payload[9] = byte(ch), byte(ch>>8), byte(ch>>16)
	payload = append(payload, make([]byte, 16)...)
	return riffWebP("VP8X", payload)
}

// webpLossy builds a VP8 key-frame header for w×h.
func webpLossy(w, h int) []byte {
	payload := make([]byte, 10, 32)
	payload[3], payload[4], payload[5] = 0x9d, 0x01, 0x2a
	binary.LittleEndian.PutUint16(payload[6:8], uint16(w))
	binary.LittleEndian.PutUint16(payload[8:10], uint16(h))
	payload = append(payload, make([]byte, 16)...)
	return riffWebP("VP8 ", payload)
}

func riffWebP(chunk string, payload []byte) []byte {
	var buf bytes.Buffer
	buf.WriteString("RIFF")
	_ = binary.Write(&buf, binary.LittleEndian, uint32(4+8+len(payload)))
	buf.WriteString("WEBP")
	buf.WriteString(chunk)
	_ = binary.Write(&buf, binary.LittleEndian, uint32(len(payload)))
	buf.Write(payload)
	return buf.Bytes()
}

func TestImageDimensions(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		mime string
		w, h int
	}{
		{"png 128", pngOf(t, 128, 128), "image/png", 128, 128},
		{"png 300x20", pngOf(t, 300, 20), "image/png", 300, 20},
		{"webp lossless", webpLossless(128, 96), "image/webp", 128, 96},
		{"webp extended", webpExtended(256, 256), "image/webp", 256, 256},
		{"webp lossy", webpLossy(64, 128), "image/webp", 64, 128},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mime, _, ok := SniffImageType(tc.data)
			if !ok || mime != tc.mime {
				t.Fatalf("sniff = %q %v, want %q", mime, ok, tc.mime)
			}
			w, h, err := ImageDimensions(tc.data, mime)
			if err != nil || w != tc.w || h != tc.h {
				t.Fatalf("ImageDimensions = %d×%d, %v; want %d×%d", w, h, err, tc.w, tc.h)
			}
		})
	}
	if _, _, err := ImageDimensions([]byte("RIFF\x10\x00\x00\x00WEBPVP8Q\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00"), "image/webp"); err == nil {
		t.Error("unknown webp chunk must not decode")
	}
	if _, _, err := ImageDimensions(pngBytes, "image/png"); err == nil {
		t.Error("a bare PNG signature is not a decodable header")
	}
}

func TestLaunchMetadata_ThumbnailValidation(t *testing.T) {
	svc, _ := newTestService(t)
	with := func(thumb []byte) LaunchMetadataInput {
		in := validInput()
		in.Thumbnail = thumb
		return in
	}
	cases := []struct {
		name string
		in   LaunchMetadataInput
		code string
	}{
		{"gif rejected", with(gifBytes), MetadataErrInvalidThumbnail},
		{"svg rejected", with([]byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`)), MetadataErrInvalidThumbnail},
		{"undecodable png header", with(pngBytes), MetadataErrInvalidThumbnail},
		{"too wide", with(pngOf(t, 257, 10)), MetadataErrInvalidThumbnail},
		{"too tall webp", with(webpLossless(128, 300)), MetadataErrInvalidThumbnail},
		{"too many bytes", with(append(append([]byte{}, pngOf(t, 8, 8)...), make([]byte, int(MetadataThumbMaxBytes))...)), MetadataErrThumbnailTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.Upload(context.Background(), tc.in)
			var verr *MetadataValidationError
			if !errors.As(err, &verr) {
				t.Fatalf("Upload() err = %v, want MetadataValidationError", err)
			}
			if verr.Code != tc.code {
				t.Fatalf("code = %q, want %q (%s)", verr.Code, tc.code, verr.Message)
			}
		})
	}
	for _, ok := range [][]byte{pngOf(t, 128, 128), pngOf(t, 256, 256), webpLossless(128, 128), webpExtended(256, 256), webpLossy(128, 128)} {
		if _, err := svc.Upload(context.Background(), with(ok)); err != nil {
			t.Errorf("valid thumbnail rejected: %v", err)
		}
	}
}

func TestLaunchMetadata_ThumbnailPinnedAndListedInFiles(t *testing.T) {
	svc, fake := newTestService(t)
	in := validInput()
	in.Thumbnail = webpLossless(128, 128)

	res, err := svc.Upload(context.Background(), in)
	if err != nil {
		t.Fatalf("Upload() error = %v", err)
	}
	if res.ThumbnailURI == "" || res.ThumbnailURI == res.ImageURI || res.ThumbnailCID == "" || res.ThumbnailBytes != len(in.Thumbnail) {
		t.Fatalf("thumbnail result = %+v", res)
	}
	imgs := fake.Images()
	if len(imgs) != 2 || imgs[0].Name != "token-image.png" || imgs[1].Name != "token-thumb.webp" || imgs[1].ContentType != "image/webp" {
		t.Fatalf("image uploads = %+v", imgs)
	}

	var doc map[string]any
	if err := json.Unmarshal(fake.Docs()[0].Bytes, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["image"] != res.ImageURI {
		t.Errorf("image must stay the master URI, got %v", doc["image"])
	}
	props, _ := doc["properties"].(map[string]any)
	files, _ := props["files"].([]any)
	if len(files) != 2 {
		t.Fatalf("properties.files = %v", doc["properties"])
	}
	master, _ := files[0].(map[string]any)
	thumb, _ := files[1].(map[string]any)
	if master["uri"] != res.ImageURI || master["type"] != "image/png" || master["purpose"] != nil {
		t.Errorf("master entry = %v", master)
	}
	if thumb["uri"] != res.ThumbnailURI || thumb["type"] != "image/webp" || thumb["purpose"] != "thumbnail" {
		t.Errorf("thumbnail entry = %v", thumb)
	}

	// Without a thumbnail the document is byte-for-byte what it was before thumbnails existed.
	fake2 := NewFakeMetadataUploader("https://gateway.pinata.cloud/ipfs/")
	svc2 := NewLaunchMetadataService(fake2, LaunchMetadataOptions{MaxImageBytes: 1024})
	res2, err := svc2.Upload(context.Background(), validInput())
	if err != nil {
		t.Fatal(err)
	}
	if res2.ThumbnailURI != "" || bytes.Contains(fake2.Docs()[0].Bytes, []byte("properties")) {
		t.Errorf("no-thumbnail upload leaked thumbnail fields: %+v / %s", res2, fake2.Docs()[0].Bytes)
	}
	if bytes.Contains([]byte(mustJSON(t, res2)), []byte("thumbnail")) {
		t.Errorf("result must omit thumbnail keys when absent: %s", mustJSON(t, res2))
	}
}

func TestLaunchMetadata_ThumbnailUploadFailureWrapped(t *testing.T) {
	svc, fake := newTestService(t)
	in := validInput()
	in.Thumbnail = pngOf(t, 32, 32)
	res, err := svc.Upload(context.Background(), in)
	if err != nil || res.ThumbnailURI == "" {
		t.Fatalf("baseline upload: %v %+v", err, res)
	}
	fake.FailWith = errors.New("pinata down")
	_, err = svc.Upload(context.Background(), in)
	if !errors.Is(err, ErrUploadFailed) {
		t.Fatalf("err = %v, want ErrUploadFailed", err)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
