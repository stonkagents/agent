// Package: internal/daemon
// Feature: F-027 (Gallery Page Completion)
// Story: US-027-04 (TransferStats Refactor)
// Purpose: Verify TransferStats tracks separate upload/download speeds

package daemon

import (
	"testing"
)

func TestTransferStats_SeparateSpeeds(t *testing.T) {
	ts := NewTransferStats()

	// Record 1000 bytes upload
	ts.RecordUpload(1000)
	// Record 500 bytes download
	ts.RecordDownload(500)

	upBytes, downBytes, upSpeed, downSpeed := ts.GetStats()

	if upBytes != 1000 {
		t.Errorf("uploadBytes = %d, want 1000", upBytes)
	}
	if downBytes != 500 {
		t.Errorf("downloadBytes = %d, want 500", downBytes)
	}
	// Speeds should be > 0 and independent
	if upSpeed <= 0 {
		t.Errorf("uploadSpeed should be > 0, got %d", upSpeed)
	}
	if downSpeed <= 0 {
		t.Errorf("downloadSpeed should be > 0, got %d", downSpeed)
	}
	// Upload speed should be >= download speed (1000 vs 500 bytes in same window)
	if upSpeed < downSpeed {
		t.Errorf("upload speed (%d) should be >= download speed (%d)", upSpeed, downSpeed)
	}
}

func TestTransferStats_UploadOnly_NoDownloadSpeed(t *testing.T) {
	ts := NewTransferStats()

	ts.RecordUpload(2000)

	_, _, upSpeed, downSpeed := ts.GetStats()

	if upSpeed <= 0 {
		t.Errorf("uploadSpeed should be > 0, got %d", upSpeed)
	}
	if downSpeed != 0 {
		t.Errorf("downloadSpeed should be 0 when no downloads recorded, got %d", downSpeed)
	}
}

func TestTransferStats_DownloadOnly_NoUploadSpeed(t *testing.T) {
	ts := NewTransferStats()

	ts.RecordDownload(3000)

	_, _, upSpeed, downSpeed := ts.GetStats()

	if upSpeed != 0 {
		t.Errorf("uploadSpeed should be 0 when no uploads recorded, got %d", upSpeed)
	}
	if downSpeed <= 0 {
		t.Errorf("downloadSpeed should be > 0, got %d", downSpeed)
	}
}
