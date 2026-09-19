// Package: internal/daemon/download
// Feature: F-027 (Gallery Page Completion)
// Story: US-027-02 (DownloadStatus JSON Tags)
// Purpose: Verify DownloadStatus serializes to snake_case JSON matching frontend DaemonDownload type

package download

import (
	"encoding/json"
	"testing"
	"time"
)

func TestDownloadStatus_JSONTags_SnakeCase(t *testing.T) {
	now := time.Now()
	errMsg := "timeout"
	ds := DownloadStatus{
		CID:             "bafytest123",
		Filename:        "test.vec",
		State:           StateActive,
		TotalSize:       1024,
		TotalChunks:     4,
		CompletedChunks: 2,
		DownloadedBytes: 512,
		SpeedBPS:        256,
		Progress:        0.5,
		ETASEC:          10,
		CompletedAt:     &now,
		ErrorMessage:    &errMsg,
	}

	data, err := json.Marshal(ds)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	// Verify snake_case keys exist
	expected := []string{
		"cid", "filename", "state", "total_size", "total_chunks",
		"completed_chunks", "downloaded_bytes", "speed_bps", "progress",
		"eta_seconds", "completed_at", "error_message",
	}
	for _, key := range expected {
		if _, ok := m[key]; !ok {
			t.Errorf("missing expected JSON key %q", key)
		}
	}

	// Verify NO PascalCase keys leaked
	banned := []string{
		"CID", "Filename", "State", "TotalSize", "TotalChunks",
		"CompletedChunks", "DownloadedBytes", "SpeedBPS", "Progress",
		"ETASEC", "CompletedAt", "ErrorMessage",
	}
	for _, key := range banned {
		if _, ok := m[key]; ok {
			t.Errorf("PascalCase key %q should not appear in JSON", key)
		}
	}
}

func TestDownloadStatus_NullableFields_NilIsNull(t *testing.T) {
	ds := DownloadStatus{
		CID:          "bafynull",
		State:        StateQueued,
		CompletedAt:  nil,
		ErrorMessage: nil,
	}

	data, err := json.Marshal(ds)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if m["completed_at"] != nil {
		t.Errorf("completed_at should be null when nil, got %v", m["completed_at"])
	}
	if m["error_message"] != nil {
		t.Errorf("error_message should be null when nil, got %v", m["error_message"])
	}
}

func TestDownloadStatus_NullableFields_SetValues(t *testing.T) {
	now := time.Date(2026, 2, 15, 12, 0, 0, 0, time.UTC)
	msg := "connection reset"
	ds := DownloadStatus{
		CID:          "bafyset",
		State:        StateFailed,
		CompletedAt:  &now,
		ErrorMessage: &msg,
	}

	data, err := json.Marshal(ds)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if m["completed_at"] == nil {
		t.Error("completed_at should NOT be null when set")
	}
	if m["error_message"] == nil {
		t.Error("error_message should NOT be null when set")
	}
	if m["error_message"] != "connection reset" {
		t.Errorf("error_message = %v, want %q", m["error_message"], "connection reset")
	}
}
