package sharing

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAllowedExtension_Allowed(t *testing.T) {
	for _, name := range []string{
		"notes.txt", "README.md", "guide.markdown", "data.json", "rows.jsonl",
		"config.yaml", "config.yml", "table.csv", "table.tsv", "Cargo.toml",
		"feed.xml", "page.html", "page.htm", "daemon.log", "index.rst",
		"settings.ini", "app.cfg", "nginx.conf",
		"main.js", "main.ts", "script.py", "main.go", "lib.rs", "App.java",
		"prog.c", "prog.cpp", "prog.h", "run.sh", "run.ps1", "schema.sql", "style.css",
		"dir/nested/notes.txt",
	} {
		if !AllowedExtension(name) {
			t.Errorf("AllowedExtension(%q) = false, want true", name)
		}
	}
}

func TestAllowedExtension_UppercaseExtensions(t *testing.T) {
	for _, name := range []string{"NOTES.TXT", "Readme.MD", "Data.JSON", "Script.PY", "Config.YAML"} {
		if !AllowedExtension(name) {
			t.Errorf("AllowedExtension(%q) = false, want true (case-insensitive)", name)
		}
	}
}

func TestAllowedExtension_Refused(t *testing.T) {
	for _, name := range []string{
		"model.safetensors", "weights.bin", "emb.npy", "photo.png", "photo.jpg", "clip.mp4",
		"archive.zip", "archive.tar.gz", "paper.pdf", "deck.pptx", "sheet.xlsx", "doc.docx",
		"setup.exe", "lib.dll", "app.msi", "image.svg",
		".env", ".env.local", "prod.env", "secrets.env",
		"noextension", "README", "passwd", "", ".", "..",
	} {
		if AllowedExtension(name) {
			t.Errorf("AllowedExtension(%q) = true, want false", name)
		}
	}
}

func TestAllowedExtensions_SortedAndComplete(t *testing.T) {
	exts := AllowedExtensions()
	if len(exts) != len(allowedExtensions) {
		t.Fatalf("AllowedExtensions() len = %d, want %d", len(exts), len(allowedExtensions))
	}
	for i := 1; i < len(exts); i++ {
		if exts[i-1] >= exts[i] {
			t.Fatalf("AllowedExtensions() not sorted at %d: %q >= %q", i, exts[i-1], exts[i])
		}
	}
	for _, ext := range exts {
		if !strings.HasPrefix(ext, ".") {
			t.Errorf("extension %q missing leading dot", ext)
		}
	}
}

func TestCheckContent_Text(t *testing.T) {
	for name, data := range map[string][]byte{
		"ascii":      []byte("hello world\n"),
		"utf8":       []byte("café 世界 \U0001F600\n"),
		"utf8-bom":   append([]byte{0xEF, 0xBB, 0xBF}, []byte("bom text")...),
		"json":       []byte(`{"a": 1}`),
		"crlf":       []byte("line1\r\nline2\r\n"),
		"tab-and-ff": []byte("a\tb\fc"),
	} {
		if err := CheckContent(data); err != nil {
			t.Errorf("CheckContent(%s) = %v, want nil", name, err)
		}
	}
}

func TestCheckContent_RefusedByContent(t *testing.T) {
	for name, data := range map[string][]byte{
		"nul-byte":       []byte("text\x00more"),
		"png-header":     {0x89, 'P', 'N', 'G', '\r', '\n', 0x1A, '\n', 0, 0, 0, 13},
		"invalid-utf8":   {'a', 0xFF, 0xFE, 'b'},
		"utf16le-text":   {'h', 0, 'i', 0},
		"lone-cont-byte": {0x80, 'x'},
	} {
		err := CheckContent(data)
		if !errors.Is(err, ErrContent) {
			t.Errorf("CheckContent(%s) = %v, want ErrContent", name, err)
		}
	}
}

func TestCheckContent_EmptyRefused(t *testing.T) {
	if err := CheckContent(nil); !errors.Is(err, ErrEmpty) {
		t.Errorf("CheckContent(empty) = %v, want ErrEmpty", err)
	}
}

func TestCheckContent_SniffWindowCutsRuneAndIgnoresLaterBytes(t *testing.T) {
	// A 4-byte rune straddling the 8 KB boundary must not be counted as invalid.
	head := bytes.Repeat([]byte("a"), SniffBytes-2)
	head = append(head, []byte("\U0001F600")...) // 4 bytes: 2 inside the window, 2 outside
	if err := CheckContent(head); err != nil {
		t.Errorf("CheckContent(rune straddling window) = %v, want nil", err)
	}
	// Bytes past the window are not inspected (the rule is the first 8 KB).
	tail := append(bytes.Repeat([]byte("a"), SniffBytes), 0, 0, 0)
	if err := CheckContent(tail); err != nil {
		t.Errorf("CheckContent(NUL after window) = %v, want nil", err)
	}
	// Invalid bytes inside the window are still refused even when the window is full.
	bad := bytes.Repeat([]byte("a"), SniffBytes)
	bad[100] = 0xFF
	if err := CheckContent(bad); !errors.Is(err, ErrContent) {
		t.Errorf("CheckContent(invalid inside full window) = %v, want ErrContent", err)
	}
}

func TestCheckReader_ExtensionBeforeContent(t *testing.T) {
	// Disallowed extension with text content: refused by extension.
	err := CheckReader("notes.pdf", strings.NewReader("this is text"))
	if !errors.Is(err, ErrExtension) {
		t.Errorf("CheckReader(.pdf, text) = %v, want ErrExtension", err)
	}
	// No extension with text content: refused.
	err = CheckReader("README", strings.NewReader("this is text"))
	if !errors.Is(err, ErrExtension) {
		t.Errorf("CheckReader(no ext, text) = %v, want ErrExtension", err)
	}
	// Allowed extension with binary content: refused by content.
	err = CheckReader("fake.txt", bytes.NewReader([]byte{0, 1, 2, 3}))
	if !errors.Is(err, ErrContent) {
		t.Errorf("CheckReader(.txt, binary) = %v, want ErrContent", err)
	}
	// Allowed extension with text content: allowed.
	if err := CheckReader("ok.md", strings.NewReader("# title\n")); err != nil {
		t.Errorf("CheckReader(.md, text) = %v, want nil", err)
	}
}

func TestCheckFile_ReadsDisk(t *testing.T) {
	dir := t.TempDir()
	textPath := filepath.Join(dir, "a.txt")
	binPath := filepath.Join(dir, "b.txt")
	if err := os.WriteFile(textPath, []byte("plain text"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binPath, []byte{0x7F, 'E', 'L', 'F', 0, 0}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := CheckFile("a.txt", textPath); err != nil {
		t.Errorf("CheckFile(text) = %v, want nil", err)
	}
	if err := CheckFile("b.txt", binPath); !errors.Is(err, ErrContent) {
		t.Errorf("CheckFile(binary named .txt) = %v, want ErrContent", err)
	}
	// Display name decides the extension rule, not the on-disk path.
	if err := CheckFile("a.bin", textPath); !errors.Is(err, ErrExtension) {
		t.Errorf("CheckFile(display .bin) = %v, want ErrExtension", err)
	}
	if err := CheckFile("missing.txt", filepath.Join(dir, "missing.txt")); err == nil || IsRefusal(err) {
		t.Errorf("CheckFile(missing) = %v, want an I/O error that is not a refusal", err)
	}
}

func TestAllowedMIME(t *testing.T) {
	for _, mt := range []string{
		"", "text/plain", "text/plain; charset=utf-8", "TEXT/HTML; charset=UTF-8", "text/csv", "text/markdown",
		"application/json", "application/x-ndjson", "application/xml", "application/yaml", "application/javascript",
	} {
		if !AllowedMIME(mt) {
			t.Errorf("AllowedMIME(%q) = false, want true", mt)
		}
	}
	for _, mt := range []string{
		"application/octet-stream", "image/png", "image/svg+xml", "video/mp4", "audio/mpeg",
		"application/pdf", "application/zip", "application/x-msdownload", "font/woff2",
	} {
		if AllowedMIME(mt) {
			t.Errorf("AllowedMIME(%q) = true, want false", mt)
		}
	}
}

func TestIsRefusal(t *testing.T) {
	for _, err := range []error{ErrExtension, ErrContent, ErrEmpty} {
		if !IsRefusal(err) {
			t.Errorf("IsRefusal(%v) = false", err)
		}
	}
	if IsRefusal(errors.New("disk on fire")) {
		t.Error("IsRefusal(other) = true, want false")
	}
}
