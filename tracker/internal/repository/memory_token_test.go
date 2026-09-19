// Package: tracker/internal/repository
// Feature: F-031 (Token Data Persistence)
// Story: US-031-05 (Token Gallery — Real Token Data)
// Purpose: Tests for MemoryTokenRepository.List (pagination + sort order)

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
)

func TestMemoryTokenList_EmptyRepo(t *testing.T) {
	repo := NewMemoryTokenRepository()
	tokens, total, err := repo.List(context.Background(), 20, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 0 {
		t.Errorf("total = %d, want 0", total)
	}
	if len(tokens) != 0 {
		t.Errorf("len(tokens) = %d, want 0", len(tokens))
	}
}

func TestMemoryTokenList_Pagination(t *testing.T) {
	repo := NewMemoryTokenRepository()
	now := time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)

	// Seed 5 tokens
	for i := 0; i < 5; i++ {
		_ = repo.Create(context.Background(), &models.PeerToken{
			PeerID:               "peer-" + string(rune('A'+i)),
			TokenContractAddress: "contract-" + string(rune('A'+i)),
			TokenTicker:          "T" + string(rune('A'+i)),
			TokenName:            "Token" + string(rune('A'+i)),
			LaunchedAt:           now.Add(time.Duration(i) * time.Hour),
		})
	}

	// Page 1: limit=2, offset=0 → should return 2 tokens, total=5
	tokens, total, err := repo.List(context.Background(), 2, 0)
	if err != nil {
		t.Fatalf("List page1: %v", err)
	}
	if total != 5 {
		t.Errorf("total = %d, want 5", total)
	}
	if len(tokens) != 2 {
		t.Errorf("len(tokens) page1 = %d, want 2", len(tokens))
	}

	// Page 3: limit=2, offset=4 → should return 1 token
	tokens2, total2, err := repo.List(context.Background(), 2, 4)
	if err != nil {
		t.Fatalf("List page3: %v", err)
	}
	if total2 != 5 {
		t.Errorf("total page3 = %d, want 5", total2)
	}
	if len(tokens2) != 1 {
		t.Errorf("len(tokens) page3 = %d, want 1", len(tokens2))
	}

	// Beyond range: offset=10 → empty
	tokens3, total3, err := repo.List(context.Background(), 2, 10)
	if err != nil {
		t.Fatalf("List beyond: %v", err)
	}
	if total3 != 5 {
		t.Errorf("total beyond = %d, want 5", total3)
	}
	if len(tokens3) != 0 {
		t.Errorf("len(tokens) beyond = %d, want 0", len(tokens3))
	}
}

func TestMemoryTokenList_SortByLaunchedAtDesc(t *testing.T) {
	repo := NewMemoryTokenRepository()

	t1 := time.Date(2026, 2, 1, 10, 0, 0, 0, time.UTC) // oldest
	t2 := time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)
	t3 := time.Date(2026, 2, 1, 14, 0, 0, 0, time.UTC) // newest

	// Insert in random order
	_ = repo.Create(context.Background(), &models.PeerToken{
		PeerID: "peer-mid", TokenContractAddress: "c-mid", TokenTicker: "MID", TokenName: "Mid",
		LaunchedAt: t2,
	})
	_ = repo.Create(context.Background(), &models.PeerToken{
		PeerID: "peer-old", TokenContractAddress: "c-old", TokenTicker: "OLD", TokenName: "Old",
		LaunchedAt: t1,
	})
	_ = repo.Create(context.Background(), &models.PeerToken{
		PeerID: "peer-new", TokenContractAddress: "c-new", TokenTicker: "NEW", TokenName: "New",
		LaunchedAt: t3,
	})

	tokens, _, err := repo.List(context.Background(), 10, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(tokens) != 3 {
		t.Fatalf("len(tokens) = %d, want 3", len(tokens))
	}
	// Should be newest first: peer-new, peer-mid, peer-old
	if tokens[0].PeerID != "peer-new" {
		t.Errorf("tokens[0].PeerID = %q, want peer-new", tokens[0].PeerID)
	}
	if tokens[1].PeerID != "peer-mid" {
		t.Errorf("tokens[1].PeerID = %q, want peer-mid", tokens[1].PeerID)
	}
	if tokens[2].PeerID != "peer-old" {
		t.Errorf("tokens[2].PeerID = %q, want peer-old", tokens[2].PeerID)
	}
}
