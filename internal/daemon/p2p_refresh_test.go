// Package: internal/daemon
// Purpose: Unit tests for VPN/network-switch address refresh (listenPortFromAddrs, addrsFromOS, AnnounceAddrsRefreshed)

package daemon

import (
	"strings"
	"testing"

	"github.com/multiformats/go-multiaddr"
)

func TestListenPortFromAddrs(t *testing.T) {
	t.Run("returns_port_when_tcp_present", func(t *testing.T) {
		addr, err := multiaddr.NewMultiaddr("/ip4/127.0.0.1/tcp/4001")
		if err != nil {
			t.Fatalf("NewMultiaddr: %v", err)
		}
		got := listenPortFromAddrs([]multiaddr.Multiaddr{addr})
		if got != "4001" {
			t.Errorf("listenPortFromAddrs() = %q, want %q", got, "4001")
		}
	})

	t.Run("returns_port_when_multiple_addrs", func(t *testing.T) {
		a1, _ := multiaddr.NewMultiaddr("/ip4/192.168.1.1/tcp/9999")
		a2, _ := multiaddr.NewMultiaddr("/ip6/::1/udp/1234")
		got := listenPortFromAddrs([]multiaddr.Multiaddr{a1, a2})
		if got != "9999" {
			t.Errorf("listenPortFromAddrs() = %q, want %q (first tcp)", got, "9999")
		}
	})

	t.Run("returns_empty_when_no_tcp", func(t *testing.T) {
		addr, _ := multiaddr.NewMultiaddr("/ip4/127.0.0.1/udp/4001")
		got := listenPortFromAddrs([]multiaddr.Multiaddr{addr})
		if got != "" {
			t.Errorf("listenPortFromAddrs() = %q, want empty", got)
		}
	})

	t.Run("returns_empty_when_empty_slice", func(t *testing.T) {
		got := listenPortFromAddrs(nil)
		if got != "" {
			t.Errorf("listenPortFromAddrs(nil) = %q, want empty", got)
		}
	})
}

func TestAddrsFromOS(t *testing.T) {
	t.Run("returns_nil_when_empty_port", func(t *testing.T) {
		got := addrsFromOS("", "12D3KooWtest")
		if got != nil {
			t.Errorf("addrsFromOS(empty port) = %v, want nil", got)
		}
	})

	t.Run("returns_nil_when_empty_peerID", func(t *testing.T) {
		got := addrsFromOS("4001", "")
		if got != nil {
			t.Errorf("addrsFromOS(empty peerID) = %v, want nil", got)
		}
	})

	t.Run("returns_addrs_with_correct_format_and_no_loopback_linklocal", func(t *testing.T) {
		peerID := "12D3KooWQ2kuohPuSVMGCuNLYPAmbZCGNGGN1fY6WvdnPcdmqA8p"
		got := addrsFromOS("4001", peerID)
		// On any machine we get at least zero or more addrs; we assert format and filters
		for _, a := range got {
			if !strings.Contains(a, "/tcp/4001/") {
				t.Errorf("addr %q missing /tcp/4001/", a)
			}
			if !strings.HasSuffix(a, "/p2p/"+peerID) {
				t.Errorf("addr %q missing /p2p/<peerID> suffix", a)
			}
			// Must not announce loopback or link-local
			if strings.Contains(a, "/ip4/127.") || strings.Contains(a, "/ip6/::1/") {
				t.Errorf("addr %q should not contain loopback", a)
			}
			if strings.Contains(a, "/ip4/169.254.") || strings.Contains(a, "/ip6/fe80") {
				t.Errorf("addr %q should not contain link-local", a)
			}
		}
	})
}

func TestAnnounceAddrsRefreshed(t *testing.T) {
	config := &Config{
		Host: "127.0.0.1",
		Port: 7841,
	}
	p2pHost, err := NewP2PHost(config, nil)
	if err != nil {
		t.Fatalf("NewP2PHost: %v", err)
	}
	defer p2pHost.Close()

	base := p2pHost.AnnounceAddrs()
	refreshed := p2pHost.AnnounceAddrsRefreshed()

	// Refreshed must contain all base addrs
	baseSet := make(map[string]bool)
	for _, a := range base {
		baseSet[a] = true
	}
	for _, a := range refreshed {
		delete(baseSet, a)
	}
	if len(baseSet) > 0 {
		t.Errorf("AnnounceAddrsRefreshed() missing addrs from AnnounceAddrs(): %v", baseSet)
	}

	// No duplicates
	seen := make(map[string]bool)
	for _, a := range refreshed {
		if seen[a] {
			t.Errorf("AnnounceAddrsRefreshed() duplicate: %q", a)
		}
		seen[a] = true
	}

	// Each addr should have /p2p/ (either direct or relay circuit)
	for _, a := range refreshed {
		if !strings.Contains(a, "/p2p/") {
			t.Errorf("AnnounceAddrsRefreshed() addr missing /p2p/: %q", a)
		}
	}
}
