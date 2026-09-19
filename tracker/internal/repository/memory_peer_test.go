// Package: tracker/internal/repository
// Feature: F-007 (Centralized Tracker)
// Story: US-007-01 (PostgreSQL Schema and Migrations)
// Purpose: Tests for in-memory peer repository

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
)

func newTestPeer(id string) *models.Peer {
	return &models.Peer{
		PeerID:     id,
		PublicKey:  "pubkey-" + id,
		Multiaddrs: []string{"/ip4/127.0.0.1/tcp/4001"},
		FirstSeen:  time.Now(),
		LastSeen:   time.Now(),
	}
}

func TestMemoryPeerRepo_Create_Success(t *testing.T) {
	repo := NewMemoryPeerRepository()
	ctx := context.Background()
	peer := newTestPeer("peer-1")

	err := repo.Create(ctx, peer)
	if err != nil {
		t.Fatalf("Create() unexpected error: %v", err)
	}

	found, err := repo.FindByID(ctx, "peer-1")
	if err != nil {
		t.Fatalf("FindByID() unexpected error: %v", err)
	}
	if found.PeerID != "peer-1" {
		t.Errorf("FindByID() got PeerID = %q, want %q", found.PeerID, "peer-1")
	}
	if found.PublicKey != "pubkey-peer-1" {
		t.Errorf("FindByID() got PublicKey = %q, want %q", found.PublicKey, "pubkey-peer-1")
	}
}

func TestMemoryPeerRepo_Create_Duplicate(t *testing.T) {
	repo := NewMemoryPeerRepository()
	ctx := context.Background()
	peer := newTestPeer("peer-1")

	_ = repo.Create(ctx, peer)
	err := repo.Create(ctx, peer)

	if err == nil {
		t.Fatal("Create() expected ErrAlreadyExists, got nil")
	}
	if err != models.ErrAlreadyExists {
		t.Errorf("Create() got error = %v, want ErrAlreadyExists", err)
	}
}

func TestMemoryPeerRepo_FindByID_Exists(t *testing.T) {
	repo := NewMemoryPeerRepository()
	ctx := context.Background()
	peer := newTestPeer("peer-1")
	peer.Multiaddrs = []string{"/ip4/10.0.0.1/tcp/4001", "/ip4/10.0.0.2/tcp/4001"}

	_ = repo.Create(ctx, peer)

	found, err := repo.FindByID(ctx, "peer-1")
	if err != nil {
		t.Fatalf("FindByID() unexpected error: %v", err)
	}
	if len(found.Multiaddrs) != 2 {
		t.Errorf("FindByID() got %d multiaddrs, want 2", len(found.Multiaddrs))
	}
}

func TestMemoryPeerRepo_FindByID_NotFound(t *testing.T) {
	repo := NewMemoryPeerRepository()
	ctx := context.Background()

	_, err := repo.FindByID(ctx, "nonexistent")
	if err == nil {
		t.Fatal("FindByID() expected ErrNotFound, got nil")
	}
	if err != models.ErrNotFound {
		t.Errorf("FindByID() got error = %v, want ErrNotFound", err)
	}
}

func TestMemoryPeerRepo_Upsert_UpdatesExisting(t *testing.T) {
	repo := NewMemoryPeerRepository()
	ctx := context.Background()

	peer := newTestPeer("peer-1")
	_ = repo.Create(ctx, peer)

	updated := newTestPeer("peer-1")
	updated.PublicKey = "new-pubkey"
	updated.Multiaddrs = []string{"/ip4/192.168.1.1/tcp/4001"}

	err := repo.Upsert(ctx, updated)
	if err != nil {
		t.Fatalf("Upsert() unexpected error: %v", err)
	}

	found, _ := repo.FindByID(ctx, "peer-1")
	if found.PublicKey != "new-pubkey" {
		t.Errorf("Upsert() PublicKey = %q, want %q", found.PublicKey, "new-pubkey")
	}
	if len(found.Multiaddrs) != 1 || found.Multiaddrs[0] != "/ip4/192.168.1.1/tcp/4001" {
		t.Errorf("Upsert() Multiaddrs not updated correctly")
	}
}

func TestMemoryPeerRepo_List_Empty(t *testing.T) {
	repo := NewMemoryPeerRepository()
	ctx := context.Background()

	peers, err := repo.List(ctx, ListPeersOptions{Limit: 10})
	if err != nil {
		t.Fatalf("List() unexpected error: %v", err)
	}
	if len(peers) != 0 {
		t.Errorf("List() got %d peers, want 0", len(peers))
	}
}

func TestMemoryPeerRepo_List_Multiple(t *testing.T) {
	repo := NewMemoryPeerRepository()
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		_ = repo.Create(ctx, newTestPeer("peer-"+string(rune('a'+i))))
	}

	peers, err := repo.List(ctx, ListPeersOptions{Limit: 10})
	if err != nil {
		t.Fatalf("List() unexpected error: %v", err)
	}
	if len(peers) != 3 {
		t.Errorf("List() got %d peers, want 3", len(peers))
	}
}

func TestMemoryPeerRepo_Delete_Success(t *testing.T) {
	repo := NewMemoryPeerRepository()
	ctx := context.Background()

	_ = repo.Create(ctx, newTestPeer("peer-1"))

	err := repo.Delete(ctx, "peer-1")
	if err != nil {
		t.Fatalf("Delete() unexpected error: %v", err)
	}

	_, err = repo.FindByID(ctx, "peer-1")
	if err != models.ErrNotFound {
		t.Errorf("FindByID() after delete got error = %v, want ErrNotFound", err)
	}
}

func TestMemoryPeerRepo_Delete_NotFound(t *testing.T) {
	repo := NewMemoryPeerRepository()
	ctx := context.Background()

	err := repo.Delete(ctx, "nonexistent")
	if err == nil {
		t.Fatal("Delete() expected ErrNotFound, got nil")
	}
	if err != models.ErrNotFound {
		t.Errorf("Delete() got error = %v, want ErrNotFound", err)
	}
}

func TestMemoryPeerRepo_FindByIDs(t *testing.T) {
	repo := NewMemoryPeerRepository()
	ctx := context.Background()

	_ = repo.Create(ctx, newTestPeer("peer-a"))
	_ = repo.Create(ctx, newTestPeer("peer-b"))
	_ = repo.Create(ctx, newTestPeer("peer-c"))

	found, err := repo.FindByIDs(ctx, []string{"peer-a", "peer-c", "peer-missing"})
	if err != nil {
		t.Fatalf("FindByIDs() unexpected error: %v", err)
	}
	if len(found) != 2 {
		t.Errorf("FindByIDs() got %d peers, want 2", len(found))
	}
}
