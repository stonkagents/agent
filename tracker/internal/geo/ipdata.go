// Package: tracker/internal/geo
// Feature: F-032 (Peers & Reputation)
// Story: US-032-01 (Peer Data Foundation)
// Purpose: ipdata.co GeoIP resolver implementation (TD-094)

package geo

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"strings"
	"time"
)

const (
	defaultIPDataBaseURL = "https://api.ipdata.co"
	ipdataTimeout        = 3 * time.Second
	ipdataMaxBody        = 4096 // 4KB response body limit
)

// IPDataResolver implements Resolver using ipdata.co API.
type IPDataResolver struct {
	apiKey  string
	baseURL string
	client  *http.Client
}

// IPDataOption configures the IPDataResolver.
type IPDataOption func(*IPDataResolver)

// WithBaseURL overrides the base URL (for testing).
func WithBaseURL(url string) IPDataOption {
	return func(r *IPDataResolver) {
		r.baseURL = url
	}
}

// NewIPDataResolver creates a new ipdata.co resolver.
// If apiKey is empty, the resolver degrades gracefully (returns empty results).
func NewIPDataResolver(apiKey string, opts ...IPDataOption) *IPDataResolver {
	r := &IPDataResolver{
		apiKey:  apiKey,
		baseURL: defaultIPDataBaseURL,
		client: &http.Client{
			Timeout: ipdataTimeout,
		},
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// ipdataResponse is the subset of ipdata.co response we use.
type ipdataResponse struct {
	CountryCode string  `json:"country_code"`
	City        string  `json:"city"`
	Latitude    float64 `json:"latitude"`
	Longitude   float64 `json:"longitude"`
}

// Lookup performs a GeoIP lookup via ipdata.co.
// Degrades gracefully: returns empty LookupResult on any error (API, network, parse).
// Private/localhost IPs are short-circuited without an API call.
func (r *IPDataResolver) Lookup(ip string) (*LookupResult, error) {
	// No API key = degrade gracefully
	if r.apiKey == "" {
		return &LookupResult{}, nil
	}

	// Skip private/localhost IPs
	if isPrivate(ip) {
		return &LookupResult{}, nil
	}

	url := fmt.Sprintf("%s/%s?api-key=%s&fields=country_code,city,latitude,longitude", r.baseURL, ip, r.apiKey)

	ctx, cancel := context.WithTimeout(context.Background(), ipdataTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		slog.Warn("[GeoIP] request creation failed", "ip", ip, "error", err)
		return &LookupResult{}, nil
	}

	resp, err := r.client.Do(req)
	if err != nil {
		slog.Warn("[GeoIP] request failed", "ip", ip, "error", err)
		return &LookupResult{}, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		slog.Warn("[GeoIP] non-200 response", "ip", ip, "status", resp.StatusCode)
		return &LookupResult{}, nil
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, ipdataMaxBody))
	if err != nil {
		slog.Warn("[GeoIP] body read failed", "ip", ip, "error", err)
		return &LookupResult{}, nil
	}

	var data ipdataResponse
	if err := json.Unmarshal(body, &data); err != nil {
		slog.Warn("[GeoIP] JSON parse failed", "ip", ip, "error", err)
		return &LookupResult{}, nil
	}

	return &LookupResult{
		Country: data.CountryCode,
		City:    data.City,
		Lat:     roundTo2(data.Latitude),
		Lng:     roundTo2(data.Longitude),
	}, nil
}

// roundTo2 rounds a float to 2 decimal places (~1.1km precision for privacy).
func roundTo2(v float64) float64 {
	return math.Round(v*100) / 100
}

// isPrivate returns true for private, localhost, and link-local IPs.
func isPrivate(ip string) bool {
	ip = strings.TrimSpace(ip)
	if ip == "" || ip == "localhost" {
		return true
	}
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return true // unparseable = treat as private
	}
	return parsed.IsLoopback() || parsed.IsPrivate() || parsed.IsLinkLocalUnicast() || parsed.IsLinkLocalMulticast()
}
