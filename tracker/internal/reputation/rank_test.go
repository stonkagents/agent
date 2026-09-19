// Package: tracker/internal/reputation
// Feature: F-032 (Peers & Reputation)
// Story: US-032-01 (Peer Data Foundation)
// Purpose: TDD tests for rank derivation with bootstrap rule

package reputation

import "testing"

func TestDeriveRank(t *testing.T) {
	tests := []struct {
		name          string
		composite     float64
		totalUpload   int64
		totalDownload int64
		sharedFiles   int
		want          string
	}{
		{name: "OG at 0.85", composite: 0.85, totalUpload: 1000, totalDownload: 500, sharedFiles: 5, want: "og"},
		{name: "OG at boundary 0.80", composite: 0.80, totalUpload: 1000, totalDownload: 500, sharedFiles: 5, want: "og"},
		{name: "Gold at 0.65", composite: 0.65, totalUpload: 500, totalDownload: 300, sharedFiles: 3, want: "gold"},
		{name: "Gold at boundary 0.60", composite: 0.60, totalUpload: 500, totalDownload: 300, sharedFiles: 3, want: "gold"},
		{name: "Silver at 0.45", composite: 0.45, totalUpload: 100, totalDownload: 100, sharedFiles: 1, want: "silver"},
		{name: "Silver at boundary 0.40", composite: 0.40, totalUpload: 100, totalDownload: 100, sharedFiles: 1, want: "silver"},
		{name: "Bronze at 0.25", composite: 0.25, totalUpload: 50, totalDownload: 50, sharedFiles: 1, want: "bronze"},
		{name: "Bronze at boundary 0.20", composite: 0.20, totalUpload: 50, totalDownload: 50, sharedFiles: 1, want: "bronze"},
		{name: "New at 0.10", composite: 0.10, totalUpload: 10, totalDownload: 10, sharedFiles: 1, want: "new"},
		{name: "New at zero", composite: 0.0, totalUpload: 0, totalDownload: 0, sharedFiles: 0, want: "new"},
		// Bootstrap rule: zero activity = always New regardless of default composite
		{name: "bootstrap: zero activity with 0.45 composite -> new", composite: 0.45, totalUpload: 0, totalDownload: 0, sharedFiles: 0, want: "new"},
		{name: "bootstrap: zero activity with 0.80 composite -> new", composite: 0.80, totalUpload: 0, totalDownload: 0, sharedFiles: 0, want: "new"},
		// Partial activity counts as real activity
		{name: "has upload only -> respects composite", composite: 0.45, totalUpload: 100, totalDownload: 0, sharedFiles: 0, want: "silver"},
		{name: "has download only -> respects composite", composite: 0.45, totalUpload: 0, totalDownload: 100, sharedFiles: 0, want: "silver"},
		{name: "has shared files only -> respects composite", composite: 0.45, totalUpload: 0, totalDownload: 0, sharedFiles: 1, want: "silver"},
		// Edge: just below boundary
		{name: "just below OG 0.799", composite: 0.799, totalUpload: 1000, totalDownload: 500, sharedFiles: 5, want: "gold"},
		{name: "just below Gold 0.599", composite: 0.599, totalUpload: 500, totalDownload: 300, sharedFiles: 3, want: "silver"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DeriveRank(tt.composite, tt.totalUpload, tt.totalDownload, tt.sharedFiles)
			if got != tt.want {
				t.Errorf("DeriveRank(%v, %v, %v, %v) = %q, want %q",
					tt.composite, tt.totalUpload, tt.totalDownload, tt.sharedFiles, got, tt.want)
			}
		})
	}
}
