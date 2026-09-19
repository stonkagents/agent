// Package: tracker/internal/services
// Feature: F-007 (Centralized Tracker)
// Story: US-007-06 (DMCA Takedown Endpoint)
// Purpose: Tests for DMCA takedown service

package services

import (
	"context"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/presence"
	"github.com/stonkagents/agent/tracker/internal/repository"

	"github.com/stonkagents/agent/tracker/internal/clock"
)

func newDMCATestDeps() (*repository.MemoryAssetRepository, *repository.MemoryDMCARepository, *presence.MemoryPresenceStore) {
	clk := clock.NewMockClock(time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC))
	return repository.NewMemoryAssetRepository(), repository.NewMemoryDMCARepository(), presence.NewMemoryPresenceStore(clk)
}

func seedAsset(ctx context.Context, assetRepo *repository.MemoryAssetRepository, cid string) {
	_ = assetRepo.Create(ctx, &models.Asset{
		CID:          cid,
		Filename:     "test-" + cid + ".bin",
		PeerID:       "peer-1",
		ManifestType: "raw",
		AnnouncedAt:  time.Now(),
	})
}

func TestDMCAService_FileNotice_Success(t *testing.T) {
	assetRepo, dmcaRepo, _ := newDMCATestDeps()
	svc := NewDMCAService(assetRepo, dmcaRepo)
	ctx := context.Background()

	seedAsset(ctx, assetRepo, "cid-1")

	notice, err := svc.FileNotice(ctx, FileNoticeRequest{
		CID:           "cid-1",
		ReporterEmail: "legal@example.com",
		ComplaintText: "Copyright infringement on my work",
	})
	if err != nil {
		t.Fatalf("FileNotice() unexpected error: %v", err)
	}
	if notice.CID != "cid-1" {
		t.Errorf("FileNotice() got CID = %q, want %q", notice.CID, "cid-1")
	}
	if notice.Status != models.DMCAStatusPending {
		t.Errorf("FileNotice() got Status = %q, want %q", notice.Status, models.DMCAStatusPending)
	}

	// Verify asset is quarantined
	asset, _ := assetRepo.FindByCID(ctx, "cid-1")
	if !asset.Quarantined {
		t.Error("FileNotice() asset not quarantined after DMCA notice")
	}
}

func TestDMCAService_FileNotice_MissingCID(t *testing.T) {
	assetRepo, dmcaRepo, _ := newDMCATestDeps()
	svc := NewDMCAService(assetRepo, dmcaRepo)
	ctx := context.Background()

	_, err := svc.FileNotice(ctx, FileNoticeRequest{
		ReporterEmail: "legal@example.com",
		ComplaintText: "infringement",
	})
	if err != models.ErrInvalidInput {
		t.Errorf("FileNotice() got error = %v, want ErrInvalidInput", err)
	}
}

func TestDMCAService_FileNotice_MissingEmail(t *testing.T) {
	assetRepo, dmcaRepo, _ := newDMCATestDeps()
	svc := NewDMCAService(assetRepo, dmcaRepo)
	ctx := context.Background()

	_, err := svc.FileNotice(ctx, FileNoticeRequest{
		CID:           "cid-1",
		ComplaintText: "infringement",
	})
	if err != models.ErrInvalidInput {
		t.Errorf("FileNotice() got error = %v, want ErrInvalidInput", err)
	}
}

func TestDMCAService_FileNotice_AssetNotFound(t *testing.T) {
	assetRepo, dmcaRepo, _ := newDMCATestDeps()
	svc := NewDMCAService(assetRepo, dmcaRepo)
	ctx := context.Background()

	_, err := svc.FileNotice(ctx, FileNoticeRequest{
		CID:           "nonexistent",
		ReporterEmail: "legal@example.com",
		ComplaintText: "infringement",
	})
	if err != models.ErrNotFound {
		t.Errorf("FileNotice() got error = %v, want ErrNotFound", err)
	}
}

func TestDMCAService_FileNotice_AlreadyQuarantined(t *testing.T) {
	assetRepo, dmcaRepo, _ := newDMCATestDeps()
	svc := NewDMCAService(assetRepo, dmcaRepo)
	ctx := context.Background()

	seedAsset(ctx, assetRepo, "cid-1")

	// File first notice
	_, _ = svc.FileNotice(ctx, FileNoticeRequest{
		CID: "cid-1", ReporterEmail: "a@example.com", ComplaintText: "first",
	})

	// File second notice on same asset — should succeed (idempotent quarantine)
	notice, err := svc.FileNotice(ctx, FileNoticeRequest{
		CID: "cid-1", ReporterEmail: "b@example.com", ComplaintText: "second",
	})
	if err != nil {
		t.Fatalf("FileNotice() second call unexpected error: %v", err)
	}
	if notice.Status != models.DMCAStatusPending {
		t.Errorf("FileNotice() got Status = %q, want %q", notice.Status, models.DMCAStatusPending)
	}
}

func TestDMCAService_GetNotice_Success(t *testing.T) {
	assetRepo, dmcaRepo, _ := newDMCATestDeps()
	svc := NewDMCAService(assetRepo, dmcaRepo)
	ctx := context.Background()

	seedAsset(ctx, assetRepo, "cid-1")
	filed, _ := svc.FileNotice(ctx, FileNoticeRequest{
		CID: "cid-1", ReporterEmail: "a@example.com", ComplaintText: "test",
	})

	found, err := svc.GetNotice(ctx, filed.ID)
	if err != nil {
		t.Fatalf("GetNotice() unexpected error: %v", err)
	}
	if found.ID != filed.ID {
		t.Errorf("GetNotice() got ID = %q, want %q", found.ID, filed.ID)
	}
}

func TestDMCAService_ListNotices(t *testing.T) {
	assetRepo, dmcaRepo, _ := newDMCATestDeps()
	svc := NewDMCAService(assetRepo, dmcaRepo)
	ctx := context.Background()

	seedAsset(ctx, assetRepo, "cid-1")
	seedAsset(ctx, assetRepo, "cid-2")

	_, _ = svc.FileNotice(ctx, FileNoticeRequest{CID: "cid-1", ReporterEmail: "a@a.com", ComplaintText: "x"})
	_, _ = svc.FileNotice(ctx, FileNoticeRequest{CID: "cid-2", ReporterEmail: "b@b.com", ComplaintText: "y"})

	notices, err := svc.ListNotices(ctx)
	if err != nil {
		t.Fatalf("ListNotices() unexpected error: %v", err)
	}
	if len(notices) != 2 {
		t.Errorf("ListNotices() got %d notices, want 2", len(notices))
	}
}

func TestDMCAService_UpdateStatus_Confirm(t *testing.T) {
	assetRepo, dmcaRepo, _ := newDMCATestDeps()
	svc := NewDMCAService(assetRepo, dmcaRepo)
	ctx := context.Background()

	seedAsset(ctx, assetRepo, "cid-1")
	filed, _ := svc.FileNotice(ctx, FileNoticeRequest{CID: "cid-1", ReporterEmail: "a@a.com", ComplaintText: "x"})

	err := svc.UpdateStatus(ctx, filed.ID, models.DMCAStatusConfirmed)
	if err != nil {
		t.Fatalf("UpdateStatus() unexpected error: %v", err)
	}

	// Asset should remain quarantined
	asset, _ := assetRepo.FindByCID(ctx, "cid-1")
	if !asset.Quarantined {
		t.Error("UpdateStatus(confirmed) asset should remain quarantined")
	}
}

func TestDMCAService_UpdateStatus_Reject(t *testing.T) {
	assetRepo, dmcaRepo, _ := newDMCATestDeps()
	svc := NewDMCAService(assetRepo, dmcaRepo)
	ctx := context.Background()

	seedAsset(ctx, assetRepo, "cid-1")
	filed, _ := svc.FileNotice(ctx, FileNoticeRequest{CID: "cid-1", ReporterEmail: "a@a.com", ComplaintText: "x"})

	err := svc.UpdateStatus(ctx, filed.ID, models.DMCAStatusRejected)
	if err != nil {
		t.Fatalf("UpdateStatus() unexpected error: %v", err)
	}

	// Asset should be un-quarantined
	asset, _ := assetRepo.FindByCID(ctx, "cid-1")
	if asset.Quarantined {
		t.Error("UpdateStatus(rejected) asset should be un-quarantined")
	}
}
