// Package: internal/daemon
// Feature: F-025 (Auto-Update System)
// Story: US-025-04 (Daemon Update Relay)
// Purpose: Tests for daemon → controller update notification relay

package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestNotifyControllerUpdate_PostsToController(t *testing.T) {
	var received map[string]string
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/update/notify" {
			t.Errorf("expected /update/notify, got %s", r.URL.Path)
		}
		if r.Header.Get("X-Request-Id") == "" {
			t.Error("expected X-Request-Id header (correlation ID)")
		}
		json.NewDecoder(r.Body).Decode(&received)
		w.WriteHeader(http.StatusOK)
	}))
	defer controller.Close()

	s := &Server{controllerURL: controller.URL}
	s.notifyControllerUpdate("1.2.0", "Security fixes")

	if received["latest_version"] != "1.2.0" {
		t.Errorf("latest_version = %q, want %q", received["latest_version"], "1.2.0")
	}
	if received["release_notes"] != "Security fixes" {
		t.Errorf("release_notes = %q, want %q", received["release_notes"], "Security fixes")
	}
}

func TestNotifyControllerUpdate_SkipsIfEmptyVersion(t *testing.T) {
	var called int32
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&called, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer controller.Close()

	s := &Server{controllerURL: controller.URL}
	s.notifyControllerUpdate("", "notes")

	if atomic.LoadInt32(&called) != 0 {
		t.Error("should not call controller when latest_version is empty")
	}
}

func TestNotifyControllerUpdate_SkipsIfSameVersion(t *testing.T) {
	var callCount int32
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&callCount, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer controller.Close()

	s := &Server{controllerURL: controller.URL}

	// First call — should POST
	s.notifyControllerUpdate("1.2.0", "Notes")
	if atomic.LoadInt32(&callCount) != 1 {
		t.Fatalf("first call: expected 1 POST, got %d", atomic.LoadInt32(&callCount))
	}

	// Second call with same version — should skip (debounce)
	s.notifyControllerUpdate("1.2.0", "Notes")
	if atomic.LoadInt32(&callCount) != 1 {
		t.Errorf("second call: expected 1 POST (debounced), got %d", atomic.LoadInt32(&callCount))
	}

	// Third call with different version — should POST again
	s.notifyControllerUpdate("1.3.0", "New features")
	if atomic.LoadInt32(&callCount) != 2 {
		t.Errorf("third call: expected 2 POSTs, got %d", atomic.LoadInt32(&callCount))
	}
}

func TestNotifyControllerUpdate_RetriesOnFailure(t *testing.T) {
	var callCount int32
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&callCount, 1)
		if n == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer controller.Close()

	s := &Server{controllerURL: controller.URL}
	s.notifyControllerUpdate("1.2.0", "Notes")

	if atomic.LoadInt32(&callCount) != 2 {
		t.Errorf("expected 2 attempts (1 failure + 1 retry), got %d", atomic.LoadInt32(&callCount))
	}
	// Should still record the version on successful retry
	if s.lastNotifiedVersion != "1.2.0" {
		t.Errorf("lastNotifiedVersion = %q, want %q", s.lastNotifiedVersion, "1.2.0")
	}
}

func TestNotifyControllerUpdate_GivesUpAfterTwoFailures(t *testing.T) {
	var callCount int32
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&callCount, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer controller.Close()

	s := &Server{controllerURL: controller.URL}
	s.notifyControllerUpdate("1.2.0", "Notes")

	if atomic.LoadInt32(&callCount) != 2 {
		t.Errorf("expected 2 attempts then give up, got %d", atomic.LoadInt32(&callCount))
	}
	// Should NOT record the version (both attempts failed)
	if s.lastNotifiedVersion != "" {
		t.Errorf("lastNotifiedVersion = %q, want empty (both attempts failed)", s.lastNotifiedVersion)
	}
}
