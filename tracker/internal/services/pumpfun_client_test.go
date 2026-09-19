// Package: tracker/internal/services
// Feature: F-031 (Token Data Persistence)
// Story: US-031-02 (Backend Token Metrics Aggregation)
// Purpose: Tests for PumpFunClient — imageUrl HTTPS validation and ctx.Err retry guard

package services

import (
	"testing"
)

func TestPumpFun_ImageUrl_NonHttps_Nulled(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool // true = should return non-nil
	}{
		{"https valid", "https://example.com/img.png", true},
		{"http rejected", "http://example.com/img.png", false},
		{"data URI rejected", "data:image/png;base64,abc", false},
		{"javascript rejected", "javascript:alert(1)", false},
		{"blob rejected", "blob:http://example.com/xxx", false},
		{"empty string", "", false},
		{"relative path", "/images/token.png", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := validateImageURL(tt.input)
			if tt.want && result == nil {
				t.Errorf("validateImageURL(%q) = nil, want non-nil", tt.input)
			}
			if !tt.want && result != nil {
				t.Errorf("validateImageURL(%q) = %q, want nil", tt.input, *result)
			}
		})
	}
}
