// Package: tracker/internal/geo
// Feature: F-032 (Peers & Reputation)
// Story: US-032-01 (Peer Data Foundation)
// Purpose: Tests for ipdata.co GeoIP resolver (TD-094)

package geo

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIPDataResolver_PublicIP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("api-key") != "test-key" {
			t.Errorf("expected api-key=test-key, got %q", r.URL.Query().Get("api-key"))
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"country_code": "US",
			"city":         "Mountain View",
			"latitude":     37.38605,
			"longitude":    -122.08385,
		})
	}))
	defer srv.Close()

	resolver := NewIPDataResolver("test-key", WithBaseURL(srv.URL))
	result, err := resolver.Lookup("8.8.8.8")
	if err != nil {
		t.Fatalf("Lookup error: %v", err)
	}
	if result.Country != "US" {
		t.Errorf("Country = %q, want US", result.Country)
	}
	if result.City != "Mountain View" {
		t.Errorf("City = %q, want Mountain View", result.City)
	}
	// Lat/Lng should be rounded to 2 decimal places for privacy
	if math.Abs(result.Lat-37.39) > 0.01 {
		t.Errorf("Lat = %f, want ~37.39 (rounded)", result.Lat)
	}
	if math.Abs(result.Lng-(-122.08)) > 0.01 {
		t.Errorf("Lng = %f, want ~-122.08 (rounded)", result.Lng)
	}
}

func TestIPDataResolver_PrivateIP(t *testing.T) {
	resolver := NewIPDataResolver("test-key")
	result, err := resolver.Lookup("192.168.1.1")
	if err != nil {
		t.Fatalf("Lookup error: %v", err)
	}
	// Private IPs should return empty result without calling the API
	if result.Country != "" || result.City != "" {
		t.Error("Private IP should return empty result")
	}
}

func TestIPDataResolver_EmptyKey(t *testing.T) {
	resolver := NewIPDataResolver("")
	result, err := resolver.Lookup("8.8.8.8")
	if err != nil {
		t.Fatalf("Lookup error: %v", err)
	}
	// No API key = degrade gracefully to empty result
	if result.Country != "" || result.City != "" {
		t.Error("Empty API key should return empty result")
	}
}

func TestIPDataResolver_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"message": "forbidden"}`))
	}))
	defer srv.Close()

	resolver := NewIPDataResolver("bad-key", WithBaseURL(srv.URL))
	result, err := resolver.Lookup("8.8.8.8")
	if err != nil {
		t.Fatalf("Lookup should degrade gracefully, got error: %v", err)
	}
	if result.Country != "" || result.City != "" {
		t.Error("API error should return empty result")
	}
}

func TestIPDataResolver_MalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{not json`))
	}))
	defer srv.Close()

	resolver := NewIPDataResolver("test-key", WithBaseURL(srv.URL))
	result, err := resolver.Lookup("8.8.8.8")
	if err != nil {
		t.Fatalf("Lookup should degrade gracefully, got error: %v", err)
	}
	if result.Country != "" || result.City != "" {
		t.Error("Malformed JSON should return empty result")
	}
}
