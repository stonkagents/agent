package relay

import (
	"testing"
)

func TestNewHost_CreatesRelayWithValidPeerID(t *testing.T) {
	h, err := NewHost("/ip4/127.0.0.1/tcp/0")
	if err != nil {
		t.Fatalf("NewHost() error: %v", err)
	}
	defer h.Close()

	if h.ID().String() == "" {
		t.Error("expected non-empty PeerID")
	}
}

func TestNewHost_ListensOnAddress(t *testing.T) {
	h, err := NewHost("/ip4/127.0.0.1/tcp/0")
	if err != nil {
		t.Fatalf("NewHost() error: %v", err)
	}
	defer h.Close()

	addrs := h.Addrs()
	if len(addrs) == 0 {
		t.Error("expected at least one listen address")
	}
}

func TestHost_Multiaddrs_IncludesPeerID(t *testing.T) {
	h, err := NewHost("/ip4/127.0.0.1/tcp/0")
	if err != nil {
		t.Fatalf("NewHost() error: %v", err)
	}
	defer h.Close()

	maddrs := h.Multiaddrs()
	if len(maddrs) == 0 {
		t.Fatal("expected at least one multiaddr")
	}

	peerID := h.ID().String()
	for _, addr := range maddrs {
		if len(addr) == 0 {
			t.Error("empty multiaddr string")
			continue
		}
		// Each multiaddr should end with /p2p/<peerID>
		suffix := "/p2p/" + peerID
		if addr[len(addr)-len(suffix):] != suffix {
			t.Errorf("multiaddr %q does not end with %q", addr, suffix)
		}
	}
}

func TestHost_AddrInfo_Valid(t *testing.T) {
	h, err := NewHost("/ip4/127.0.0.1/tcp/0")
	if err != nil {
		t.Fatalf("NewHost() error: %v", err)
	}
	defer h.Close()

	info := h.AddrInfo()
	if info.ID != h.ID() {
		t.Errorf("AddrInfo ID mismatch: got %s, want %s", info.ID, h.ID())
	}
	if len(info.Addrs) == 0 {
		t.Error("AddrInfo has no addresses")
	}
}

func TestHost_Close_NoError(t *testing.T) {
	h, err := NewHost("/ip4/127.0.0.1/tcp/0")
	if err != nil {
		t.Fatalf("NewHost() error: %v", err)
	}

	if err := h.Close(); err != nil {
		t.Errorf("Close() error: %v", err)
	}
}
