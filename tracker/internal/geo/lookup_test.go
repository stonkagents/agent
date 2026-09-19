// Package: tracker/internal/geo
// Purpose: Tests for MaskPeerID (GeoIP Lookup deprecated in favor of Resolver)

package geo

import "testing"

func TestMaskPeerID_ShortID(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"", ""},
		{"abc", "abc"},
		{"12D3KooW", "12D3KooW"},
		{"12D3KooWAbc", "12D3KooWAbc"},
		{"12D3KooWAbcd", "12D3KooWAbcd"},
	}

	for _, tt := range tests {
		got := MaskPeerID(tt.input)
		if got != tt.expected {
			t.Errorf("MaskPeerID(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestMaskPeerID_LongID(t *testing.T) {
	input := "12D3KooWEyJ5Y8Z9Q3x7Qabcdefghijklmnopqrstuvwxyz"
	// Implementation: first 6 + "..." + last 6 chars
	expected := "12D3Ko...uvwxyz"
	got := MaskPeerID(input)
	if got != expected {
		t.Errorf("MaskPeerID(%q) = %q, want %q", input, got, expected)
	}
}

func TestMaskPeerID_Exact13Chars(t *testing.T) {
	input := "12D3KooWAbcde"
	got := MaskPeerID(input)
	// Should be first 6 + "..." + last 6 = 15 chars total
	if len(got) != 15 {
		t.Errorf("MaskPeerID(%q) length = %d, want 15", input, len(got))
	}
	if !(len(got) >= len(input)) {
		t.Errorf("MaskPeerID(%q) = %q, should be at least as long as input", input, got)
	}
}
