// Package sharing holds the product rule for what may travel over StonkAgents P2P sharing.
// Agents share knowledge, so only plain-text files are allowed. The rule is enforced by
// extension (allowlist) and by content (UTF-8, no NUL bytes in the first SniffBytes).
// Both the daemon (POST /api/v1/share) and the tracker (POST /api/v1/tracker/announce)
// use this package so an old daemon cannot bypass the rule.
package sharing

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

// SniffBytes is how much of a file is inspected for the content check.
const SniffBytes = 8 * 1024

// ErrorCode is the API error code returned when a file is refused.
const ErrorCode = "UNSUPPORTED_FILE_TYPE"

// RuleMessage is the user-facing sentence stating the rule. Keep in sync with the portal copy.
const RuleMessage = "Only plain-text files can be shared: .txt, .md, .json, .csv, .yaml, code files and similar."

// Refusal reasons.
var (
	ErrExtension = errors.New("file extension is not a plain-text format")
	ErrContent   = errors.New("file content is not plain text")
	ErrEmpty     = errors.New("file is empty")
)

// allowedExtensions is the allowlist (lowercase, with the leading dot).
// .env and .env-style files are deliberately absent: they carry secrets.
var allowedExtensions = map[string]bool{
	// Plain text and data
	".txt": true, ".md": true, ".markdown": true,
	".json": true, ".jsonl": true,
	".yaml": true, ".yml": true,
	".csv": true, ".tsv": true,
	".toml": true, ".xml": true,
	".html": true, ".htm": true,
	".log": true, ".rst": true,
	".ini": true, ".cfg": true, ".conf": true,
	// Source code
	".js": true, ".jsx": true, ".mjs": true, ".cjs": true,
	".ts": true, ".tsx": true,
	".py": true, ".go": true, ".rs": true, ".java": true,
	".c": true, ".cc": true, ".cpp": true, ".h": true, ".hpp": true,
	".sh": true, ".ps1": true, ".sql": true, ".css": true, ".scss": true,
	".rb": true, ".php": true, ".kt": true, ".swift": true, ".lua": true,
}

// allowedMIMETypes lists the application/* types a text file may be announced as (text/* is always fine).
// The tracker uses it for announcements that carry a mime_type.
var allowedMIMETypes = map[string]bool{
	"application/json":          true,
	"application/ld+json":       true,
	"application/x-ndjson":      true,
	"application/jsonl":         true,
	"application/xml":           true,
	"application/xhtml+xml":     true,
	"application/yaml":          true,
	"application/x-yaml":        true,
	"application/toml":          true,
	"application/javascript":    true,
	"application/x-javascript":  true,
	"application/ecmascript":    true,
	"application/typescript":    true,
	"application/x-typescript":  true,
	"application/sql":           true,
	"application/x-sh":          true,
	"application/x-shellscript": true,
	"application/x-python":      true,
	"application/x-powershell":  true,
}

// AllowedExtensions returns the allowlist sorted, with leading dots (for docs and UI).
func AllowedExtensions() []string {
	out := make([]string, 0, len(allowedExtensions))
	for ext := range allowedExtensions {
		out = append(out, ext)
	}
	sort.Strings(out)
	return out
}

// AllowedExtension reports whether the filename's extension is on the allowlist.
// The comparison is case-insensitive; a file without an extension is refused.
func AllowedExtension(filename string) bool {
	ext := strings.ToLower(filepath.Ext(filepath.Base(filename)))
	if ext == "" {
		return false
	}
	return allowedExtensions[ext]
}

// AllowedMIME reports whether a mime type is compatible with a plain-text file.
// An empty mime type is accepted (unknown); text/* and a small set of application/* text types are accepted.
func AllowedMIME(mimeType string) bool {
	mt := strings.TrimSpace(strings.ToLower(mimeType))
	if mt == "" {
		return true
	}
	if i := strings.IndexByte(mt, ';'); i >= 0 {
		mt = strings.TrimSpace(mt[:i])
	}
	if strings.HasPrefix(mt, "text/") {
		return true
	}
	return allowedMIMETypes[mt]
}

// CheckContent inspects the first SniffBytes of a file (head) and refuses binary content.
// head is at most SniffBytes long; when it is exactly SniffBytes an incomplete trailing
// UTF-8 sequence is tolerated because the cut may have landed inside a rune.
func CheckContent(head []byte) error {
	if len(head) == 0 {
		return ErrEmpty
	}
	if len(head) > SniffBytes {
		head = head[:SniffBytes]
	}
	for _, b := range head {
		if b == 0 {
			return ErrContent
		}
	}
	if len(head) == SniffBytes {
		// The cut may land inside a multi-byte rune: drop up to 3 trailing bytes until valid.
		// Content that is invalid anywhere earlier stays invalid.
		for i := 0; i < utf8.UTFMax-1 && !utf8.Valid(head); i++ {
			head = head[:len(head)-1]
		}
	}
	if !utf8.Valid(head) {
		return ErrContent
	}
	return nil
}

// CheckReader applies the extension rule to filename and the content rule to the first SniffBytes of r.
func CheckReader(filename string, r io.Reader) error {
	if !AllowedExtension(filename) {
		return ErrExtension
	}
	head := make([]byte, SniffBytes)
	n, err := io.ReadFull(r, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return fmt.Errorf("read file head: %w", err)
	}
	return CheckContent(head[:n])
}

// CheckFile applies both rules to a file on disk. displayName is the name the file will be shared under
// (its extension is checked); path is where the bytes are read from.
func CheckFile(displayName, path string) error {
	if !AllowedExtension(displayName) {
		return ErrExtension
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open file: %w", err)
	}
	defer f.Close()
	return CheckReader(displayName, f)
}

// IsRefusal reports whether err is one of the rule refusals (as opposed to an I/O failure).
func IsRefusal(err error) bool {
	return errors.Is(err, ErrExtension) || errors.Is(err, ErrContent) || errors.Is(err, ErrEmpty)
}
