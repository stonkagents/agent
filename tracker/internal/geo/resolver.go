// Package: tracker/internal/geo
// Feature: F-032 (Peers & Reputation)
// Story: US-032-01 (Peer Data Foundation)
// Purpose: GeoIP Resolver interface with stub for graceful degradation

package geo

// LookupResult holds the result of a GeoIP lookup.
type LookupResult struct {
	Country string  // ISO 3166-1 alpha-2 (e.g. "US", "DE")
	City    string  // City name
	Lat     float64 // Latitude (rounded to 2 decimals for privacy)
	Lng     float64 // Longitude (rounded to 2 decimals for privacy)
}

// Resolver performs GeoIP lookups. Implementations may use MaxMind, ip-api, or return empty (stub).
type Resolver interface {
	Lookup(ip string) (*LookupResult, error)
}

// StubResolver always returns an empty LookupResult. Used when no GeoIP database is configured.
type StubResolver struct{}

// Lookup returns an empty result (graceful degradation when GeoIP is unavailable).
func (s *StubResolver) Lookup(ip string) (*LookupResult, error) {
	return &LookupResult{}, nil
}

// MaskPeerID masks a peer ID for public display.
// Format: first 6 chars + "..." + last 6 chars
// For short IDs (< 13 chars), returns full ID
func MaskPeerID(peerID string) string {
	if len(peerID) <= 12 {
		return peerID
	}
	if len(peerID) <= 13 {
		return peerID[:6] + "..." + peerID[7:]
	}
	return peerID[:6] + "..." + peerID[len(peerID)-6:]
}
