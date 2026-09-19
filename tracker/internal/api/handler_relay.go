// Package: tracker/internal/api
// Feature: F-010 (Go Core Daemon — Cross-Internet Reachability)
// Story: US-010-04 (DHT/GossipSub/mDNS Integration)
// Purpose: HTTP handler for returning tracker relay info to daemons

package api

import (
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/stonkagents/agent/tracker/internal/relay"
)

// RelayInfoDTO is the response for the relay info endpoint.
type RelayInfoDTO struct {
	PeerID     string   `json:"peer_id"`
	Multiaddrs []string `json:"multiaddrs"`
}

// RelayHandler handles relay-related HTTP endpoints.
type RelayHandler struct {
	relayHost        *relay.Host
	publicMultiaddrs []string // Optional override for public-facing multiaddrs (e.g., via ngrok TCP)
}

// NewRelayHandler creates a new RelayHandler.
func NewRelayHandler(relayHost *relay.Host) *RelayHandler {
	return &RelayHandler{relayHost: relayHost}
}

// SetPublicMultiaddrs sets public-facing multiaddrs that override the host's local addresses.
// Use when the relay is behind a tunnel (e.g., ngrok TCP) and daemons need the public address.
func (h *RelayHandler) SetPublicMultiaddrs(addrs []string) {
	h.publicMultiaddrs = addrs
}

// HandleGetRelayInfo handles GET /api/v1/tracker/relay.
// Returns the tracker's libp2p relay PeerID and multiaddrs so daemons
// can configure AutoRelay with this as a static relay.
func (h *RelayHandler) HandleGetRelayInfo(w http.ResponseWriter, r *http.Request) {
	if h.relayHost == nil {
		SendError(w, http.StatusServiceUnavailable, "RELAY_UNAVAILABLE", "Relay service is not running")
		return
	}

	// Include both public (ngrok/tunnel) and local relay addresses.
	// Same-machine daemons connect via local addrs (fast, reliable).
	// Remote daemons connect via public/resolved addrs.
	multiaddrs := h.relayHost.Multiaddrs()
	if len(h.publicMultiaddrs) > 0 {
		multiaddrs = append(h.publicMultiaddrs, multiaddrs...)
	}

	// Also include IP-resolved versions of DNS-based addrs.
	// Clients behind restrictive DNS (e.g., mobile carriers) may fail to resolve
	// tunnel hostnames like *.tcp.ngrok.io. Providing pre-resolved IPs ensures
	// they can still connect.
	multiaddrs = appendResolvedAddrs(multiaddrs)

	SendJSON(w, http.StatusOK, RelayInfoDTO{
		PeerID:     h.relayHost.ID().String(),
		Multiaddrs: multiaddrs,
	})
}

// appendResolvedAddrs returns the original addrs plus IP-resolved versions of any
// DNS-based multiaddrs (e.g., /dns4/host/tcp/port → /ip4/1.2.3.4/tcp/port).
func appendResolvedAddrs(addrs []string) []string {
	result := make([]string, 0, len(addrs)*2)
	result = append(result, addrs...)

	for _, addr := range addrs {
		var ipProto string
		if strings.HasPrefix(addr, "/dns4/") {
			ipProto = "ip4"
		} else if strings.HasPrefix(addr, "/dns6/") {
			ipProto = "ip6"
		} else {
			continue
		}

		parts := strings.Split(addr, "/")
		if len(parts) < 5 || parts[3] != "tcp" {
			continue
		}
		hostname := parts[2]
		port := parts[4]
		suffix := ""
		if len(parts) > 5 {
			suffix = "/" + strings.Join(parts[5:], "/")
		}

		ips, err := net.LookupHost(hostname)
		if err != nil {
			continue
		}
		for _, ip := range ips {
			result = append(result, fmt.Sprintf("/%s/%s/tcp/%s%s", ipProto, ip, port, suffix))
		}
	}

	return result
}
