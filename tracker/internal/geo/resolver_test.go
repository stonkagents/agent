// Package: tracker/internal/geo
// Feature: F-032 (Peers & Reputation)
// Story: US-032-01 (Peer Data Foundation)
// Purpose: Tests for GeoIP Resolver interface and stub implementation

package geo

import "testing"

func TestStubResolver_PublicIP(t *testing.T) {
	r := &StubResolver{}
	result, err := r.Lookup("8.8.8.8")
	if err != nil {
		t.Fatalf("StubResolver.Lookup() error: %v", err)
	}
	if result == nil {
		t.Fatal("StubResolver.Lookup() returned nil")
	}
	if result.Country != "" {
		t.Errorf("StubResolver.Lookup() Country = %q, want empty", result.Country)
	}
	if result.City != "" {
		t.Errorf("StubResolver.Lookup() City = %q, want empty", result.City)
	}
	if result.Lat != 0 {
		t.Errorf("StubResolver.Lookup() Lat = %f, want 0", result.Lat)
	}
	if result.Lng != 0 {
		t.Errorf("StubResolver.Lookup() Lng = %f, want 0", result.Lng)
	}
}

func TestStubResolver_PrivateIP(t *testing.T) {
	r := &StubResolver{}
	result, err := r.Lookup("127.0.0.1")
	if err != nil {
		t.Fatalf("StubResolver.Lookup() error: %v", err)
	}
	if result == nil {
		t.Fatal("StubResolver.Lookup() returned nil")
	}
	// Stub returns empty for all IPs, including private
	if result.Country != "" || result.City != "" {
		t.Error("StubResolver should return empty for private IPs")
	}
}

func TestStubResolver_EmptyIP(t *testing.T) {
	r := &StubResolver{}
	result, err := r.Lookup("")
	if err != nil {
		t.Fatalf("StubResolver.Lookup() error: %v", err)
	}
	if result == nil {
		t.Fatal("StubResolver.Lookup() returned nil for empty IP")
	}
}
