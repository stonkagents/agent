// Package: internal/daemon/download
// Feature: F-029 (P2P Download Parallelization)
// Story: US-029-P3 (Connection Pre-Establishment)
// Purpose: Pre-warm peer connections in parallel before starting the worker pool (TD-059)

package download

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

// warmupConnections pre-establishes connections to all registered peers
// concurrently, with a hard deadline. Connections that don't finish in time
// are abandoned — the worker pool's ensurePeerConnection will retry lazily.
func warmupConnections(ctx context.Context, coord *Coordinator, connector PeerConnector, deadline time.Duration) {
	peers := coord.GetAllPeers()
	if len(peers) == 0 {
		return
	}

	warmCtx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()

	var wg sync.WaitGroup
	for _, p := range peers {
		pid, err := peer.Decode(p.PeerID)
		if err != nil {
			continue
		}
		addrs := p.Addrs

		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case <-warmCtx.Done():
				return
			default:
			}
			if err := connector.ConnectToPeer(pid, addrs); err != nil {
				slog.Debug("[warmup] connection failed", "peer", pid, "error", err)
			}
		}()
	}

	// Wait for all connections or deadline, whichever comes first
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-warmCtx.Done():
	}
}
