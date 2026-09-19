// Package: tracker/internal/repository
// Feature: F-032 (Peers & Reputation)
// Story: US-032-02 (Activity, Badges & Credits)
// Purpose: TDD tests for in-memory PeerEventRepository

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
)

func TestMemoryPeerEvent_InsertAndList(t *testing.T) {
	repo := NewMemoryPeerEventRepository()
	ctx := context.Background()

	now := time.Date(2026, 2, 15, 12, 0, 0, 0, time.UTC)
	events := []*models.PeerEvent{
		{ID: "e1", PeerID: "peer-a", Action: "share", Details: "shared file.vec", CreatedAt: now.Add(-2 * time.Hour)},
		{ID: "e2", PeerID: "peer-a", Action: "install", Details: "installed model.bin", CreatedAt: now.Add(-1 * time.Hour)},
		{ID: "e3", PeerID: "peer-a", Action: "trust", Details: "trusted peer-b", CreatedAt: now},
	}
	for _, e := range events {
		if err := repo.Insert(ctx, e); err != nil {
			t.Fatalf("Insert(%s): %v", e.ID, err)
		}
	}

	// List all events for peer-a (should be in DESC order)
	got, total, err := repo.ListByPeerID(ctx, "peer-a", 10, 0)
	if err != nil {
		t.Fatalf("ListByPeerID: %v", err)
	}
	if total != 3 {
		t.Errorf("total = %d, want 3", total)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	// Most recent first
	if got[0].ID != "e3" {
		t.Errorf("got[0].ID = %q, want e3 (most recent)", got[0].ID)
	}
	if got[2].ID != "e1" {
		t.Errorf("got[2].ID = %q, want e1 (oldest)", got[2].ID)
	}
}

func TestMemoryPeerEvent_Empty(t *testing.T) {
	repo := NewMemoryPeerEventRepository()
	ctx := context.Background()

	got, total, err := repo.ListByPeerID(ctx, "nonexistent", 10, 0)
	if err != nil {
		t.Fatalf("ListByPeerID: %v", err)
	}
	if total != 0 {
		t.Errorf("total = %d, want 0", total)
	}
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}

func TestMemoryPeerEvent_Pagination(t *testing.T) {
	repo := NewMemoryPeerEventRepository()
	ctx := context.Background()

	now := time.Date(2026, 2, 15, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		_ = repo.Insert(ctx, &models.PeerEvent{
			ID:        "e" + string(rune('0'+i)),
			PeerID:    "peer-x",
			Action:    "share",
			Details:   "event",
			CreatedAt: now.Add(time.Duration(i) * time.Hour),
		})
	}

	// limit=2, offset=1 → skip the most recent, get next 2
	got, total, err := repo.ListByPeerID(ctx, "peer-x", 2, 1)
	if err != nil {
		t.Fatalf("ListByPeerID: %v", err)
	}
	if total != 5 {
		t.Errorf("total = %d, want 5", total)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	// After DESC sort + offset=1: should be 3rd and 4th most recent
	if got[0].CreatedAt.After(got[1].CreatedAt) == false {
		t.Errorf("expected DESC order: got[0]=%v, got[1]=%v", got[0].CreatedAt, got[1].CreatedAt)
	}
}
