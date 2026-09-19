// Package: tracker/internal/repository
// Feature: F-007 (Centralized Tracker)
// Story: US-007-06 (DMCA Takedown Endpoint)
// Purpose: Tests for in-memory DMCA repository

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
)

func newTestDMCANotice(id, cid string) *models.DMCANotice {
	return &models.DMCANotice{
		ID:            id,
		CID:           cid,
		ReporterEmail: "legal@example.com",
		ComplaintText: "Copyright infringement",
		QuarantinedAt: time.Now(),
		Status:        models.DMCAStatusPending,
	}
}

func TestMemoryDMCARepo_Create_Success(t *testing.T) {
	repo := NewMemoryDMCARepository()
	ctx := context.Background()
	notice := newTestDMCANotice("dmca-1", "cid-1")

	err := repo.Create(ctx, notice)
	if err != nil {
		t.Fatalf("Create() unexpected error: %v", err)
	}

	found, err := repo.FindByID(ctx, "dmca-1")
	if err != nil {
		t.Fatalf("FindByID() unexpected error: %v", err)
	}
	if found.CID != "cid-1" {
		t.Errorf("FindByID() got CID = %q, want %q", found.CID, "cid-1")
	}
	if found.Status != models.DMCAStatusPending {
		t.Errorf("FindByID() got Status = %q, want %q", found.Status, models.DMCAStatusPending)
	}
}

func TestMemoryDMCARepo_FindByCID(t *testing.T) {
	repo := NewMemoryDMCARepository()
	ctx := context.Background()

	_ = repo.Create(ctx, newTestDMCANotice("dmca-1", "cid-1"))
	_ = repo.Create(ctx, newTestDMCANotice("dmca-2", "cid-1"))
	_ = repo.Create(ctx, newTestDMCANotice("dmca-3", "cid-2"))

	notices, err := repo.FindByCID(ctx, "cid-1")
	if err != nil {
		t.Fatalf("FindByCID() unexpected error: %v", err)
	}
	if len(notices) != 2 {
		t.Errorf("FindByCID() got %d notices, want 2", len(notices))
	}
}

func TestMemoryDMCARepo_FindByID_NotFound(t *testing.T) {
	repo := NewMemoryDMCARepository()
	ctx := context.Background()

	_, err := repo.FindByID(ctx, "nonexistent")
	if err != models.ErrNotFound {
		t.Errorf("FindByID() got error = %v, want ErrNotFound", err)
	}
}

func TestMemoryDMCARepo_List_All(t *testing.T) {
	repo := NewMemoryDMCARepository()
	ctx := context.Background()

	_ = repo.Create(ctx, newTestDMCANotice("dmca-1", "cid-1"))
	_ = repo.Create(ctx, newTestDMCANotice("dmca-2", "cid-2"))

	notices, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("List() unexpected error: %v", err)
	}
	if len(notices) != 2 {
		t.Errorf("List() got %d notices, want 2", len(notices))
	}
}

func TestMemoryDMCARepo_UpdateStatus(t *testing.T) {
	repo := NewMemoryDMCARepository()
	ctx := context.Background()

	_ = repo.Create(ctx, newTestDMCANotice("dmca-1", "cid-1"))

	err := repo.UpdateStatus(ctx, "dmca-1", models.DMCAStatusConfirmed)
	if err != nil {
		t.Fatalf("UpdateStatus() unexpected error: %v", err)
	}

	found, _ := repo.FindByID(ctx, "dmca-1")
	if found.Status != models.DMCAStatusConfirmed {
		t.Errorf("UpdateStatus() got Status = %q, want %q", found.Status, models.DMCAStatusConfirmed)
	}
}

func TestMemoryDMCARepo_UpdateStatus_NotFound(t *testing.T) {
	repo := NewMemoryDMCARepository()
	ctx := context.Background()

	err := repo.UpdateStatus(ctx, "nonexistent", models.DMCAStatusConfirmed)
	if err != models.ErrNotFound {
		t.Errorf("UpdateStatus() got error = %v, want ErrNotFound", err)
	}
}
