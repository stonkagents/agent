// Package: internal/daemon
// Feature: F-010 (Go Core Daemon)
// Story: US-010-06 (Chunked Download Manager)
// Purpose: Wire P2P transfer dependencies into download manager
// Audit: Phase 1 Item 2 — connects existing orchestration code to P2P stack

package daemon

import (
	"context"
	"fmt"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/stonkagents/agent/internal/daemon/download"
	"github.com/stonkagents/agent/internal/daemon/storage"
	"github.com/stonkagents/agent/internal/daemon/tracker"
	"github.com/stonkagents/agent/internal/daemon/upload"
	crypto "github.com/stonkagents/agent/pkg/cryptography"
	"github.com/stonkagents/agent/pkg/protocol"
)

// chunkStoreAdapter bridges storage.SQLiteChunkStore to download.ChunkStoreInterface
// The download manager uses a simplified interface; this adapter translates calls
type chunkStoreAdapter struct {
	store *storage.SQLiteChunkStore
}

func (a *chunkStoreAdapter) StoreChunk(cid string, chunkIndex int, data []byte) error {
	// SQLiteChunkStore.StoreChunk requires a chunkCID; generate one from data
	chunkCID, err := protocol.CalculateChunkCID(data)
	if err != nil {
		return fmt.Errorf("failed to calculate chunk CID: %w", err)
	}

	// Audit B2: Defense-in-depth — verify calculated CID matches data integrity
	valid, err := crypto.VerifyCID(chunkCID, data)
	if err != nil {
		return fmt.Errorf("CID verification error on store: %w", err)
	}
	if !valid {
		return fmt.Errorf("CID integrity check failed on store: calculated CID does not match data")
	}

	return a.store.StoreChunk(cid, chunkIndex, chunkCID, data)
}

func (a *chunkStoreAdapter) GetChunk(cid string, chunkIndex int) ([]byte, error) {
	data, err := a.store.GetChunk(cid, chunkIndex)
	if err != nil {
		return nil, err
	}

	// Audit B2: Defense-in-depth — verify retrieved data integrity via CID
	chunkCID, err := protocol.CalculateChunkCID(data)
	if err != nil {
		return nil, fmt.Errorf("CID calculation error on retrieve: %w", err)
	}
	valid, err := crypto.VerifyCID(chunkCID, data)
	if err != nil {
		return nil, fmt.Errorf("CID verification error on retrieve: %w", err)
	}
	if !valid {
		return nil, fmt.Errorf("CID integrity check failed on retrieve: stored data may be corrupted")
	}

	return data, nil
}

func (a *chunkStoreAdapter) HasChunk(cid string, chunkIndex int) (bool, error) {
	return a.store.HasChunk(cid, chunkIndex), nil
}

// chunkProviderAdapter bridges storage.SQLiteChunkStore to protocol.ChunkProvider
// Required for RegisterBlockExchange to serve chunks to remote peers. uploads
// (optional) enforces the upload cap: every chunk handed to a peer first waits
// for its bytes under the throttle, so POST /api/v1/setup/bandwidth is applied
// on the actual send path and not just recorded.
type chunkProviderAdapter struct {
	store   *storage.SQLiteChunkStore
	uploads *upload.Manager
}

func (a *chunkProviderAdapter) GetChunk(ctx context.Context, fileCID string, chunkIndex int) (*protocol.Chunk, error) {
	data, err := a.store.GetChunk(fileCID, chunkIndex)
	if err != nil {
		return nil, err
	}
	if a.uploads != nil {
		// ctx is the block-exchange stream context (StreamTimeout); a chunk
		// that cannot be paced out in time is refused rather than sent uncapped.
		if err := a.uploads.WaitUpload(ctx, int64(len(data))); err != nil {
			return nil, fmt.Errorf("upload cap: %w", err)
		}
	}

	chunkCID, err := protocol.CalculateChunkCID(data)
	if err != nil {
		return nil, fmt.Errorf("failed to calculate chunk CID: %w", err)
	}

	return &protocol.Chunk{
		FileCID:    fileCID,
		ChunkIndex: chunkIndex,
		ChunkCID:   chunkCID,
		Data:       data,
		Size:       int64(len(data)),
	}, nil
}

func (a *chunkProviderAdapter) HasChunk(_ context.Context, fileCID string, chunkIndex int) (bool, error) {
	return a.store.HasChunk(fileCID, chunkIndex), nil
}

// trackerClientAdapter bridges tracker.Client to download.TrackerClientInterface
// Converts tracker.TrackerPeerInfo to download.PeerInfo
type trackerClientAdapter struct {
	client *tracker.Client
}

func (a *trackerClientAdapter) SearchByCID(cid string) ([]download.PeerInfo, error) {
	trackerPeers, err := a.client.SearchByCID(cid)
	if err != nil {
		return nil, err
	}

	peers := make([]download.PeerInfo, len(trackerPeers))
	for i, tp := range trackerPeers {
		peers[i] = download.PeerInfo{
			PeerID: tp.PeerID,
			Chunks: tp.Chunks,
			Addrs:  tp.Multiaddrs,
		}
	}
	return peers, nil
}

func (a *trackerClientAdapter) UpdateAvailability(cid string, chunks []int) error {
	return a.client.UpdateAvailability(cid, chunks)
}

// dhtContentDiscoveryAdapter bridges P2PHost.FindProvidersForContent to download.DHTContentDiscoveryInterface.
// Converts peer.AddrInfo to download.PeerInfo with optimistic full chunk list (DHT does not provide chunk availability).
type dhtContentDiscoveryAdapter struct {
	p2pHost *P2PHost
}

func (a *dhtContentDiscoveryAdapter) FindProvidersForContent(ctx context.Context, cid string, totalChunks int, limit int) ([]download.PeerInfo, error) {
	infos, err := a.p2pHost.FindProvidersForContent(ctx, cid, limit)
	if err != nil || len(infos) == 0 {
		return nil, err
	}
	optimisticChunks := make([]int, totalChunks)
	for i := 0; i < totalChunks; i++ {
		optimisticChunks[i] = i
	}
	peers := make([]download.PeerInfo, 0, len(infos))
	for _, info := range infos {
		addrs := make([]string, 0, len(info.Addrs))
		for _, ma := range info.Addrs {
			addrs = append(addrs, ma.String()+"/p2p/"+info.ID.String())
		}
		peers = append(peers, download.PeerInfo{
			PeerID: info.ID.String(),
			Chunks: optimisticChunks,
			Addrs:  addrs,
		})
	}
	return peers, nil
}

// WireP2PDownloads connects the P2P stack to the download manager
// Must be called after both InitializeManagers() and StartP2P() succeed
// Creates adapters to bridge interface mismatches between components
func (s *Server) WireP2PDownloads() error {
	if s.p2pHost == nil {
		return fmt.Errorf("P2P host not initialized; call StartP2P() first")
	}
	if s.downloadManager == nil {
		return fmt.Errorf("download manager not initialized; call InitializeManagers() first")
	}
	if s.chunkStore == nil {
		return fmt.Errorf("chunk store not initialized")
	}

	// 1. Create P2PClient from libp2p host
	p2pClient := NewP2PClient(s.p2pHost.Host(), s.logger)

	// 2. Create Coordinator (totalChunks=0 as placeholder; actual value set per-download)
	coordinator := download.NewCoordinator(0)

	// 3. Register block exchange protocol handler for serving chunks to peers
	chunkProvider := &chunkProviderAdapter{store: s.chunkStore, uploads: s.uploadManager}
	if err := s.p2pHost.RegisterBlockExchange(chunkProvider); err != nil {
		return fmt.Errorf("failed to register block exchange: %w", err)
	}
	// Wire upload stats recording so served chunks count toward leaderboard/reputation
	if s.transferStats != nil {
		if svc := s.p2pHost.BlockExchangeService(); svc != nil {
			type uploadRecorderSetter interface {
				SetUploadRecorder(protocol.UploadRecorder)
			}
			if setter, ok := svc.(uploadRecorderSetter); ok {
				setter.SetUploadRecorder(s.transferStats)
			}
		}
	}

	// 3b. Register agent-chat stream handler for direct token chat (owner's daemon responds with local LLM)
	s.p2pHost.RegisterAgentChatHandler(s.HandleAgentChatStream)

	// 4. Create adapters for interface bridging
	chunkStoreAdapter := &chunkStoreAdapter{store: s.chunkStore}
	trackerAdapter := &trackerClientAdapter{client: s.trackerClient}

	// 5. DHT content discovery adapter (BitTorrent-style: FindProviders for CID)
	dhtDiscovery := &dhtContentDiscoveryAdapter{p2pHost: s.p2pHost}

	// 6. Inject P2P dependencies into download manager
	s.downloadManager.SetP2PDependencies(&download.P2PDependencies{
		ChunkStore:          chunkStoreAdapter,
		TrackerClient:       trackerAdapter,
		P2PClient:           p2pClient,
		Coordinator:         coordinator,
		PeerConnector:       s.p2pHost,
		DHTContentDiscovery: dhtDiscovery,
	})

	// 7. Start background download worker
	s.downloadManager.StartWorker()

	if s.logger != nil {
		s.logger.Info("Server", "WireP2PDownloads", "P2P download pipeline wired", map[string]interface{}{
			"peer_id": s.p2pHost.ID().String(),
		})
	}

	return nil
}

// Host returns the underlying libp2p host (used by P2PClient creation)
func (p *P2PHost) Host() host.Host {
	return p.host
}
