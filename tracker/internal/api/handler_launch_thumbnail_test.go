// Package: tracker/internal/api
// Feature: StonkAgents launchpad — launch thumbnails
// Purpose: Handler tests for the optional thumbnailDataUrl on POST /api/launch/metadata
//          (JSON and multipart, error codes, response shape) and imageThumbUrl on
//          POST /api/launch/record and every launch read.

package api

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stonkagents/agent/tracker/internal/services"
)

func testPNGSized(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewNRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// testWebP is a 128×128 lossless WebP header (enough for sniffing and dimensions).
func testWebP(w, h int) []byte {
	payload := make([]byte, 5, 24)
	payload[0] = 0x2f
	binary.LittleEndian.PutUint32(payload[1:5], uint32(w-1)|uint32(h-1)<<14)
	payload = append(payload, make([]byte, 16)...)
	var buf bytes.Buffer
	buf.WriteString("RIFF")
	_ = binary.Write(&buf, binary.LittleEndian, uint32(4+8+len(payload)))
	buf.WriteString("WEBPVP8L")
	_ = binary.Write(&buf, binary.LittleEndian, uint32(len(payload)))
	buf.Write(payload)
	return buf.Bytes()
}

func dataURL(mime string, b []byte) string {
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(b)
}

func postJSON(t *testing.T, h *LaunchMetadataHandler, payload map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, LaunchMetadataPath, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleUpload(w, req)
	return w
}

func TestLaunchMetadata_JSON_Thumbnail_HappyPath(t *testing.T) {
	h, fake := newLaunchMetadataHandler(t, 1024)
	thumb := testWebP(128, 128)
	w := postJSON(t, h, map[string]any{
		"name": "Stonk Agent", "symbol": "STONK", "description": "stonks",
		"imageDataUrl": dataURL("image/png", testPNG), "thumbnailDataUrl": dataURL("image/webp", thumb),
	})
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
	if !strings.HasPrefix(d.ThumbnailURI, testGateway) || d.ThumbnailURI == d.ImageURI || d.ThumbnailCID == "" || d.ThumbnailBytes != len(thumb) {
		t.Errorf("thumbnail result = %+v", d)
	}
	if d.ImageURI == "" || d.MetadataURI == "" || d.ImageBytes != len(testPNG) {
		t.Errorf("master result = %+v", d)
	}
	imgs := fake.Images()
	if len(imgs) != 2 || imgs[1].Name != "token-thumb.webp" {
		t.Fatalf("image uploads = %+v", imgs)
	}
	if !bytes.Contains(fake.Docs()[0].Bytes, []byte(`"purpose":"thumbnail"`)) {
		t.Errorf("metadata doc lacks the thumbnail file entry: %s", fake.Docs()[0].Bytes)
	}
}

func TestLaunchMetadata_JSON_Thumbnail_Errors(t *testing.T) {
	h, _ := newLaunchMetadataHandler(t, 1024)
	base := func() map[string]any {
		return map[string]any{"name": "a", "symbol": "A", "imageDataUrl": dataURL("image/png", testPNG)}
	}
	oversize := append(append([]byte{}, testPNGSized(t, 8, 8)...), make([]byte, int(services.MetadataThumbMaxBytes))...)
	cases := []struct {
		name  string
		thumb any
		code  string
	}{
		{"not a data url", "https://x/y.webp", "invalid_thumbnail"},
		{"non-image data url", "data:text/plain;base64,aGk=", "invalid_thumbnail"},
		{"bad base64", "data:image/webp;base64,!!!", "invalid_thumbnail"},
		{"gif", dataURL("image/gif", append([]byte("GIF89a"), make([]byte, 16)...)), "invalid_thumbnail"},
		{"too many pixels", dataURL("image/png", testPNGSized(t, 300, 300)), "invalid_thumbnail"},
		{"too many bytes", dataURL("image/png", oversize), "thumbnail_too_large"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := base()
			p["thumbnailDataUrl"] = tc.thumb
			w := postJSON(t, h, p)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (%s)", w.Code, w.Body.String())
			}
			if got := decodeErr(t, w); got != tc.code {
				t.Fatalf("code = %q, want %q", got, tc.code)
			}
		})
	}

	// Absent or blank thumbnail: the request is exactly what it was before thumbnails.
	for _, blank := range []any{nil, "", "   "} {
		p := base()
		if blank != nil {
			p["thumbnailDataUrl"] = blank
		}
		w := postJSON(t, h, p)
		if w.Code != http.StatusOK {
			t.Fatalf("blank thumbnail %q: status = %d (%s)", blank, w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "thumbnail") {
			t.Errorf("blank thumbnail %q: response carries thumbnail keys: %s", blank, w.Body.String())
		}
	}
}

func TestLaunchMetadata_Multipart_ThumbnailPart(t *testing.T) {
	h, fake := newLaunchMetadataHandler(t, 1024)
	body, ct := buildMultipart(t, mpOpts{fields: defaultFields(), image: testPNG, thumbnail: testPNGSized(t, 64, 64), thumbnailName: "thumb.png"})
	req := httptest.NewRequest(http.MethodPost, LaunchMetadataPath, body)
	req.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()
	h.HandleUpload(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"thumbnailUri":"`+testGateway) {
		t.Errorf("body = %s", w.Body.String())
	}
	if imgs := fake.Images(); len(imgs) != 2 || imgs[1].Name != "token-thumb.png" {
		t.Errorf("uploads = %+v", imgs)
	}

	body, ct = buildMultipart(t, mpOpts{fields: defaultFields(), image: testPNG, thumbnail: testPNGSized(t, 257, 1), thumbnailName: "thumb.png"})
	req = httptest.NewRequest(http.MethodPost, LaunchMetadataPath, body)
	req.Header.Set("Content-Type", ct)
	w = httptest.NewRecorder()
	h.HandleUpload(w, req)
	if w.Code != http.StatusBadRequest || decodeErr(t, w) != "invalid_thumbnail" {
		t.Errorf("oversize multipart thumbnail: %d %s", w.Code, w.Body.String())
	}
}

func TestLaunchRecord_ImageThumbUrl_RoundTrip(t *testing.T) {
	env := newLaunchTestEnv(t, false)
	const thumb = "https://cdn.example/a-thumb.webp"

	body := recordBody()
	body["imageThumbUrl"] = thumb
	w := env.do(t, http.MethodPost, "/api/launch/record", body, "")
	if w.Code != http.StatusCreated {
		t.Fatalf("record status = %d body=%s", w.Code, w.Body.String())
	}
	// The record response is the stored row (snake_case, like image_url); reads add the camelCase view.
	if data := decodeData(t, w); data["image_thumb_url"] != thumb {
		t.Errorf("record data = %v", data)
	}

	for _, path := range []string{"/api/launch/" + lMint, "/api/launches?creator=" + lCreator, "/api/launch/pending?wallet=" + lCreator} {
		w = env.do(t, http.MethodGet, path, nil, "")
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"imageThumbUrl":"`+thumb+`"`) {
			t.Errorf("GET %s = %d %s, want imageThumbUrl", path, w.Code, w.Body.String())
		}
	}
}

func TestLaunchRecord_ImageThumbUrl_NullWhenAbsentAndValidated(t *testing.T) {
	env := newLaunchTestEnv(t, false)

	w := env.do(t, http.MethodPost, "/api/launch/record", recordBody(), "")
	if w.Code != http.StatusCreated {
		t.Fatalf("record status = %d body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "thumb") {
		t.Errorf("record without thumbnail must omit image_thumb_url from the stored row: %s", w.Body.String())
	}
	w = env.do(t, http.MethodGet, "/api/launch/"+lMint, nil, "")
	if !strings.Contains(w.Body.String(), `"imageThumbUrl":null`) {
		t.Errorf("GET without thumbnail must return imageThumbUrl null: %s", w.Body.String())
	}

	for _, bad := range []string{"javascript:alert(1)", "https://cdn.example/<x>", "ftp://cdn.example/a.webp", "https://" + strings.Repeat("a", 512)} {
		body := recordBody()
		body["imageThumbUrl"] = bad
		body["mint"] = lOther
		w = env.do(t, http.MethodPost, "/api/launch/record", body, "")
		if w.Code != http.StatusBadRequest || errorCode(t, w) != "VALIDATION_ERROR" {
			t.Errorf("imageThumbUrl %q: status = %d %s, want 400 VALIDATION_ERROR", bad, w.Code, w.Body.String())
		}
	}
}
