// Package: tracker/internal/services
// Feature: StonkAgents launchpad (workstream D — server-side token metadata)
// Purpose: Tests for LaunchMetadataService — validation matrix, magic-byte
//          sniffing, JSON shape snapshot vs the portal's metadata.ts, fake uploader
//          happy path, and upload failure wrapping.

package services

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// Minimal magic-byte prefixes recognised by http.DetectContentType.
var (
	pngBytes  = append([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, make([]byte, 16)...)
	jpegBytes = append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, make([]byte, 16)...)
	gifBytes  = append([]byte("GIF89a"), make([]byte, 16)...)
	webpBytes = append([]byte("RIFF\x24\x00\x00\x00WEBPVP8 "), make([]byte, 16)...)
)

const testWallet = "7cVfgArCheMR6Cs4t6vz5rfnqd56vZq4ndaBrY5xkxXy" // valid 32-byte base58 pubkey

func validInput() LaunchMetadataInput {
	return LaunchMetadataInput{
		Name:          "Stonk Agent",
		Symbol:        "STONK",
		Description:   "An agent that stonks.",
		Website:       "https://stonkagents.fun",
		Twitter:       "https://x.com/stonkagents",
		Telegram:      "https://t.me/stonkagents",
		CreatorWallet: testWallet,
		Image:         pngBytes,
	}
}

func newTestService(t *testing.T) (*LaunchMetadataService, *FakeMetadataUploader) {
	t.Helper()
	fake := NewFakeMetadataUploader("https://gateway.pinata.cloud/ipfs/")
	return NewLaunchMetadataService(fake, LaunchMetadataOptions{MaxImageBytes: 1024, GatewayURL: "https://gateway.pinata.cloud/ipfs/"}), fake
}

func TestSniffImageType(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		mime string
		ok   bool
	}{
		{"png", pngBytes, "image/png", true},
		{"jpeg", jpegBytes, "image/jpeg", true},
		{"gif", gifBytes, "image/gif", true},
		{"webp", webpBytes, "image/webp", true},
		{"svg (scriptable) rejected", []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`), "", false},
		{"html rejected", []byte("<html><body>hi</body></html>"), "", false},
		{"pdf rejected", []byte("%PDF-1.4 ..."), "", false},
		{"empty", nil, "", false},
		{"png extension lie (text bytes)", []byte("not really a png"), "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mime, _, ok := SniffImageType(tc.data)
			if ok != tc.ok || mime != tc.mime {
				t.Fatalf("SniffImageType() = (%q, %v), want (%q, %v)", mime, ok, tc.mime, tc.ok)
			}
		})
	}
}

func TestLaunchMetadata_ValidationMatrix(t *testing.T) {
	svc, _ := newTestService(t)
	mutate := func(f func(*LaunchMetadataInput)) LaunchMetadataInput {
		in := validInput()
		f(&in)
		return in
	}
	cases := []struct {
		name string
		in   LaunchMetadataInput
		code string
	}{
		{"empty name", mutate(func(i *LaunchMetadataInput) { i.Name = "   " }), MetadataErrInvalidName},
		{"name too long", mutate(func(i *LaunchMetadataInput) { i.Name = strings.Repeat("a", 33) }), MetadataErrInvalidName},
		{"name control char", mutate(func(i *LaunchMetadataInput) { i.Name = "Sto\x00nk" }), MetadataErrInvalidName},
		{"name newline", mutate(func(i *LaunchMetadataInput) { i.Name = "Sto\nnk" }), MetadataErrInvalidName},
		{"name invalid utf8", mutate(func(i *LaunchMetadataInput) { i.Name = "St\xffonk" }), MetadataErrInvalidName},
		{"empty symbol", mutate(func(i *LaunchMetadataInput) { i.Symbol = "" }), MetadataErrInvalidSymbol},
		{"symbol too long", mutate(func(i *LaunchMetadataInput) { i.Symbol = "ABCDEFGHIJK" }), MetadataErrInvalidSymbol},
		{"symbol control char", mutate(func(i *LaunchMetadataInput) { i.Symbol = "ST\x1bONK" }), MetadataErrInvalidSymbol},
		{"symbol multibyte counts bytes", mutate(func(i *LaunchMetadataInput) { i.Symbol = "ééééé" + "é" }), MetadataErrInvalidSymbol},
		{"description too long", mutate(func(i *LaunchMetadataInput) { i.Description = strings.Repeat("x", 1001) }), MetadataErrInvalidDescription},
		{"description control char", mutate(func(i *LaunchMetadataInput) { i.Description = "bad\x07bell" }), MetadataErrInvalidDescription},
		{"website http", mutate(func(i *LaunchMetadataInput) { i.Website = "http://stonkagents.fun" }), MetadataErrInvalidURL},
		{"website javascript", mutate(func(i *LaunchMetadataInput) { i.Website = "javascript:alert(1)" }), MetadataErrInvalidURL},
		{"website userinfo", mutate(func(i *LaunchMetadataInput) { i.Website = "https://user:pw@evil.com" }), MetadataErrInvalidURL},
		{"website localhost", mutate(func(i *LaunchMetadataInput) { i.Website = "https://localhost/x" }), MetadataErrInvalidURL},
		{"website ip", mutate(func(i *LaunchMetadataInput) { i.Website = "https://10.0.0.1/x" }), MetadataErrInvalidURL},
		{"twitter wrong host", mutate(func(i *LaunchMetadataInput) { i.Twitter = "https://twitter.evil.com/x" }), MetadataErrInvalidURL},
		{"twitter lookalike", mutate(func(i *LaunchMetadataInput) { i.Twitter = "https://x.com.evil.com/x" }), MetadataErrInvalidURL},
		{"telegram wrong host", mutate(func(i *LaunchMetadataInput) { i.Telegram = "https://telegram.evil.com/x" }), MetadataErrInvalidURL},
		{"telegram http", mutate(func(i *LaunchMetadataInput) { i.Telegram = "http://t.me/x" }), MetadataErrInvalidURL},
		{"wallet garbage", mutate(func(i *LaunchMetadataInput) { i.CreatorWallet = "not-a-wallet" }), MetadataErrInvalidWallet},
		{"wallet base58 wrong length", mutate(func(i *LaunchMetadataInput) { i.CreatorWallet = "111111111111111111111111111111111" }), MetadataErrInvalidWallet},
		{"missing image", mutate(func(i *LaunchMetadataInput) { i.Image = nil }), MetadataErrInvalidImage},
		{"non-image bytes", mutate(func(i *LaunchMetadataInput) { i.Image = []byte("<svg/>") }), MetadataErrInvalidImage},
		{"image too large", mutate(func(i *LaunchMetadataInput) { i.Image = append(append([]byte{}, pngBytes...), make([]byte, 1024)...) }), MetadataErrImageTooLarge},
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
}

func TestLaunchMetadata_AcceptsAllowedVariants(t *testing.T) {
	svc, _ := newTestService(t)
	variants := []func(*LaunchMetadataInput){
		func(i *LaunchMetadataInput) { i.Twitter = "https://twitter.com/stonk" },
		func(i *LaunchMetadataInput) { i.Twitter = "https://WWW.X.COM/stonk" },
		func(i *LaunchMetadataInput) { i.Telegram = "https://telegram.me/stonk" },
		func(i *LaunchMetadataInput) { i.Website, i.Twitter, i.Telegram, i.CreatorWallet = "", "", "", "" },
		func(i *LaunchMetadataInput) { i.Description = "line one\nline two\ttabbed" },
		func(i *LaunchMetadataInput) { i.Description = "" },
		func(i *LaunchMetadataInput) { i.Name = "  padded  "; i.Symbol = " STK " },
		func(i *LaunchMetadataInput) { i.Image = jpegBytes },
		func(i *LaunchMetadataInput) { i.Image = gifBytes },
		func(i *LaunchMetadataInput) { i.Image = webpBytes },
	}
	for n, f := range variants {
		in := validInput()
		f(&in)
		if _, err := svc.Upload(context.Background(), in); err != nil {
			t.Errorf("variant %d: unexpected error %v", n, err)
		}
	}
}

// TestLaunchMetadata_JSONShapeSnapshot pins the metadata JSON to the shape
// metadata.ts produced client-side, so the portal swap is a drop-in.
func TestLaunchMetadata_JSONShapeSnapshot(t *testing.T) {
	in := validInput()
	got, _ := json.Marshal(BuildMetadataDoc(in, "https://gateway.pinata.cloud/ipfs/QmImage"))
	want := `{"name":"Stonk Agent","symbol":"STONK","description":"An agent that stonks.","image":"https://gateway.pinata.cloud/ipfs/QmImage","showName":true,"twitter":"https://x.com/stonkagents","telegram":"https://t.me/stonkagents","website":"https://stonkagents.fun","creatorWallet":"` + testWallet + `"}`
	if string(got) != want {
		t.Fatalf("metadata JSON mismatch\n got: %s\nwant: %s", got, want)
	}

	// Optional links and wallet are omitted entirely (metadata.ts only sets them when truthy).
	in.Website, in.Twitter, in.Telegram, in.CreatorWallet = "", "", "", ""
	got, _ = json.Marshal(BuildMetadataDoc(in, "https://gateway.pinata.cloud/ipfs/QmImage"))
	want = `{"name":"Stonk Agent","symbol":"STONK","description":"An agent that stonks.","image":"https://gateway.pinata.cloud/ipfs/QmImage","showName":true}`
	if string(got) != want {
		t.Fatalf("minimal metadata JSON mismatch\n got: %s\nwant: %s", got, want)
	}
}

func TestLaunchMetadata_FakeUploaderHappyPath(t *testing.T) {
	svc, fake := newTestService(t)
	in := validInput()
	in.Name = "  Stonk Agent  "

	res, err := svc.Upload(context.Background(), in)
	if err != nil {
		t.Fatalf("Upload() error = %v", err)
	}
	if !strings.HasPrefix(res.ImageURI, "https://gateway.pinata.cloud/ipfs/Qmfake") {
		t.Errorf("ImageURI = %q", res.ImageURI)
	}
	if !strings.HasPrefix(res.MetadataURI, "https://gateway.pinata.cloud/ipfs/Qmfake") {
		t.Errorf("MetadataURI = %q", res.MetadataURI)
	}
	if res.ImageCID == "" || res.MetadataCID == "" || res.ImageCID == res.MetadataCID {
		t.Errorf("CIDs = (%q, %q)", res.ImageCID, res.MetadataCID)
	}
	if res.ImageBytes != len(pngBytes) || res.ContentType != "image/png" || res.MetadataBytes == 0 {
		t.Errorf("sizes/type = %+v", res)
	}

	imgs, docs := fake.Images(), fake.Docs()
	if len(imgs) != 1 || imgs[0].Name != "token-image.png" || imgs[0].ContentType != "image/png" {
		t.Fatalf("image uploads = %+v", imgs)
	}
	if len(docs) != 1 || docs[0].Name != "metadata.json" {
		t.Fatalf("doc uploads = %+v", docs)
	}
	var doc TokenMetadataDoc
	if err := json.Unmarshal(docs[0].Bytes, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Name != "Stonk Agent" || doc.Image != res.ImageURI || !doc.ShowName {
		t.Errorf("uploaded doc = %+v", doc)
	}

	// Determinism: same input → same URIs.
	res2, _ := svc.Upload(context.Background(), in)
	if res2.MetadataURI != res.MetadataURI {
		t.Errorf("fake uploader not deterministic: %q vs %q", res2.MetadataURI, res.MetadataURI)
	}
}

func TestLaunchMetadata_UploadFailureWrapped(t *testing.T) {
	svc, fake := newTestService(t)
	fake.FailWith = errors.New("pinata down")
	_, err := svc.Upload(context.Background(), validInput())
	if !errors.Is(err, ErrUploadFailed) {
		t.Fatalf("err = %v, want ErrUploadFailed", err)
	}
	var verr *MetadataValidationError
	if errors.As(err, &verr) {
		t.Fatal("upload failure must not be a validation error")
	}
}
