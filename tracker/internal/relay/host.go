// Package: tracker/internal/relay
// Feature: F-010 (Go Core Daemon — Cross-Internet Reachability)
// Story: US-010-04 (DHT/GossipSub/mDNS Integration)
// Purpose: libp2p relay host for the tracker, enabling NAT traversal via circuit relay v2

package relay

import (
	"context"
	"fmt"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	relayv2 "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"
	"github.com/multiformats/go-multiaddr"
)

const (
	// DefaultListenPort is the default libp2p listen port for the relay
	DefaultListenPort = 9841

	// RelayDuration is the max duration of a relayed connection
	RelayDuration = 5 * time.Minute

	// RelayDataLimit is the max bytes relayed per direction per connection (10MB)
	RelayDataLimit = 10 * 1024 * 1024

	// MaxReservations is the max number of active relay reservations
	MaxReservations = 128

	// MaxCircuits is the max number of open relay circuits
	MaxCircuits = 64
)

// Host wraps a libp2p host configured as a circuit relay v2 server.
type Host struct {
	host   host.Host
	relay  *relayv2.Relay
	ctx    context.Context
	cancel context.CancelFunc
}

// NewHost creates a libp2p host with circuit relay v2 service enabled.
// listenAddr should be a multiaddr string like "/ip4/0.0.0.0/tcp/9841".
func NewHost(listenAddr string) (*Host, error) {
	ctx, cancel := context.WithCancel(context.Background())

	h, err := libp2p.New(
		libp2p.ListenAddrStrings(listenAddr),
		libp2p.ForceReachabilityPublic(), // Skip AutoNAT — this host is publicly reachable
		// EnableRelay() is the default and MUST remain active for the circuit relay
		// transport to work. DisableRelay() would prevent circuit connections even
		// though relayv2.New() registers the HOP protocol handler.
	)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to create relay libp2p host: %w", err)
	}

	// Start circuit relay v2 service with configured limits.
	// IMPORTANT: Must set ReservationTTL explicitly — zero value causes
	// "expiration date in the past" errors because expire = now.Add(0).
	r, err := relayv2.New(h,
		relayv2.WithLimit(&relayv2.RelayLimit{
			Duration: RelayDuration,
			Data:     RelayDataLimit,
		}),
		relayv2.WithResources(relayv2.Resources{
			ReservationTTL:         time.Hour, // How long a relay reservation lasts
			MaxReservations:        MaxReservations,
			MaxCircuits:            MaxCircuits,
			BufferSize:             4096,
			MaxReservationsPerPeer: 4,
			MaxReservationsPerIP:   8,
			MaxReservationsPerASN:  32,
		}),
	)
	if err != nil {
		h.Close()
		cancel()
		return nil, fmt.Errorf("failed to start relay v2 service: %w", err)
	}

	return &Host{
		host:   h,
		relay:  r,
		ctx:    ctx,
		cancel: cancel,
	}, nil
}

// ID returns the relay host's PeerID.
func (rh *Host) ID() peer.ID {
	return rh.host.ID()
}

// Addrs returns the relay host's listen multiaddrs.
func (rh *Host) Addrs() []multiaddr.Multiaddr {
	return rh.host.Addrs()
}

// AddrInfo returns the relay host's AddrInfo for use with AutoRelay static relays.
func (rh *Host) AddrInfo() peer.AddrInfo {
	return peer.AddrInfo{
		ID:    rh.host.ID(),
		Addrs: rh.host.Addrs(),
	}
}

// Multiaddrs returns full multiaddrs with /p2p/<peerID> appended, as strings.
func (rh *Host) Multiaddrs() []string {
	peerID := rh.host.ID().String()
	addrs := rh.host.Addrs()
	result := make([]string, 0, len(addrs))
	for _, addr := range addrs {
		result = append(result, fmt.Sprintf("%s/p2p/%s", addr.String(), peerID))
	}
	return result
}

// Close shuts down the relay host.
func (rh *Host) Close() error {
	rh.cancel()
	if rh.relay != nil {
		rh.relay.Close()
	}
	if rh.host != nil {
		return rh.host.Close()
	}
	return nil
}
