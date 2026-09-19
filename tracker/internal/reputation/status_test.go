// Package: tracker/internal/reputation
// Feature: F-032 (Peers & Reputation)
// Story: US-032-01 (Peer Data Foundation)
// Purpose: TDD tests for peer status derivation from timestamps

package reputation

import (
	"testing"
	"time"
)

func TestDeriveStatus(t *testing.T) {
	now := time.Date(2026, 2, 15, 12, 0, 0, 0, time.UTC)
	within := now.Add(-3 * time.Minute)   // 3 min ago — within 5-min window
	outside := now.Add(-10 * time.Minute) // 10 min ago — outside window
	stale := now.Add(-6 * time.Minute)    // 6 min ago — outside heartbeat TTL

	tests := []struct {
		name           string
		lastSeen       time.Time
		lastUploadAt   *time.Time
		lastDownloadAt *time.Time
		want           string
	}{
		{
			name:     "offline when lastSeen beyond TTL",
			lastSeen: stale,
			want:     "offline",
		},
		{
			name:         "seeding when upload within window",
			lastSeen:     now,
			lastUploadAt: &within,
			want:         "seeding",
		},
		{
			name:           "leeching when download within window but no recent upload",
			lastSeen:       now,
			lastUploadAt:   &outside,
			lastDownloadAt: &within,
			want:           "leeching",
		},
		{
			name:     "online when no recent transfer activity",
			lastSeen: now,
			want:     "online",
		},
		{
			name:           "seeding wins when both upload and download within window",
			lastSeen:       now,
			lastUploadAt:   &within,
			lastDownloadAt: &within,
			want:           "seeding",
		},
		{
			name:           "online when nil upload and nil download",
			lastSeen:       now,
			lastUploadAt:   nil,
			lastDownloadAt: nil,
			want:           "online",
		},
		{
			name:           "leeching when nil upload but download within window",
			lastSeen:       now,
			lastUploadAt:   nil,
			lastDownloadAt: &within,
			want:           "leeching",
		},
		{
			name:         "online when upload outside window and no download",
			lastSeen:     now,
			lastUploadAt: &outside,
			want:         "online",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DeriveStatus(tt.lastSeen, tt.lastUploadAt, tt.lastDownloadAt, now)
			if got != tt.want {
				t.Errorf("DeriveStatus() = %q, want %q", got, tt.want)
			}
		})
	}
}
