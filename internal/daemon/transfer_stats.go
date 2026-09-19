// Package: internal/daemon
// Feature: F-027 (Gallery Page Completion)
// Story: US-027-04 (TransferStats Refactor)
// Purpose: Transfer statistics tracking with separate upload/download speed windows

package daemon

import (
	"sync"
	"time"
)

// TransferStats tracks cumulative upload and download bytes with separate speed windows.
type TransferStats struct {
	mu               sync.RWMutex
	totalUpload      int64
	totalDownload    int64
	lastUploadTime   time.Time
	lastDownloadTime time.Time
	uploadWindow     []speedSample
	downloadWindow   []speedSample
}

type speedSample struct {
	bytes     int64
	timestamp time.Time
}

// NewTransferStats creates a new TransferStats tracker.
func NewTransferStats() *TransferStats {
	return &TransferStats{
		uploadWindow:   make([]speedSample, 0),
		downloadWindow: make([]speedSample, 0),
	}
}

// RecordUpload records uploaded bytes.
func (ts *TransferStats) RecordUpload(bytes int64) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.totalUpload += bytes
	ts.lastUploadTime = time.Now()
	ts.uploadWindow = addAndPrune(ts.uploadWindow, bytes, time.Now())
}

// RecordDownload records downloaded bytes.
func (ts *TransferStats) RecordDownload(bytes int64) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.totalDownload += bytes
	ts.lastDownloadTime = time.Now()
	ts.downloadWindow = addAndPrune(ts.downloadWindow, bytes, time.Now())
}

// GetStats returns cumulative bytes and separate upload/download speeds (bytes per second).
func (ts *TransferStats) GetStats() (uploadBytes, downloadBytes, uploadSpeedBPS, downloadSpeedBPS int64) {
	ts.mu.RLock()
	defer ts.mu.RUnlock()

	uploadBytes = ts.totalUpload
	downloadBytes = ts.totalDownload
	uploadSpeedBPS = calcWindowSpeed(ts.uploadWindow)
	downloadSpeedBPS = calcWindowSpeed(ts.downloadWindow)

	return uploadBytes, downloadBytes, uploadSpeedBPS, downloadSpeedBPS
}

// calcWindowSpeed calculates average bytes per second over the last 60 seconds.
func calcWindowSpeed(window []speedSample) int64 {
	now := time.Now()
	windowStart := now.Add(-60 * time.Second)
	totalBytes := int64(0)
	sampleCount := 0

	for _, sample := range window {
		if sample.timestamp.After(windowStart) {
			totalBytes += sample.bytes
			sampleCount++
		}
	}

	if sampleCount > 0 {
		windowDuration := now.Sub(windowStart).Seconds()
		if windowDuration > 0 {
			return int64(float64(totalBytes) / windowDuration)
		}
	}

	return 0
}

// addAndPrune adds a speed sample and prunes samples older than 60 seconds.
func addAndPrune(window []speedSample, bytes int64, timestamp time.Time) []speedSample {
	window = append(window, speedSample{
		bytes:     bytes,
		timestamp: timestamp,
	})

	// Prune samples older than 60 seconds
	cutoff := timestamp.Add(-60 * time.Second)
	validStart := 0
	for i, sample := range window {
		if sample.timestamp.After(cutoff) {
			validStart = i
			break
		}
	}
	if validStart > 0 {
		window = window[validStart:]
	}

	return window
}
