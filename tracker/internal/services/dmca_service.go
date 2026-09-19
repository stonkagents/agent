// Package: tracker/internal/services
// Feature: F-007 (Centralized Tracker)
// Story: US-007-06 (DMCA Takedown Endpoint)
// Purpose: Business logic for DMCA takedown notices and quarantine

package services

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/repository"
)

// FileNoticeRequest contains fields for filing a DMCA notice.
type FileNoticeRequest struct {
	CID           string
	ReporterEmail string
	ComplaintText string
}

// DMCAService handles DMCA takedown notices and asset quarantine.
type DMCAService struct {
	assetRepo repository.AssetRepository
	dmcaRepo  repository.DMCARepository
}

// NewDMCAService creates a new DMCAService.
func NewDMCAService(assetRepo repository.AssetRepository, dmcaRepo repository.DMCARepository) *DMCAService {
	return &DMCAService{assetRepo: assetRepo, dmcaRepo: dmcaRepo}
}

// FileNotice creates a DMCA notice and quarantines the asset.
func (s *DMCAService) FileNotice(ctx context.Context, req FileNoticeRequest) (*models.DMCANotice, error) {
	if err := validateFileNotice(req); err != nil {
		return nil, err
	}

	// Verify asset exists
	_, err := s.assetRepo.FindByCID(ctx, req.CID)
	if err != nil {
		return nil, err
	}

	notice := &models.DMCANotice{
		ID:            uuid.New().String(),
		CID:           req.CID,
		ReporterEmail: req.ReporterEmail,
		ComplaintText: req.ComplaintText,
		QuarantinedAt: time.Now(),
		Status:        models.DMCAStatusPending,
	}

	if err := s.dmcaRepo.Create(ctx, notice); err != nil {
		return nil, err
	}

	// Quarantine the asset (idempotent)
	_ = s.assetRepo.SetQuarantined(ctx, req.CID, true)

	return notice, nil
}

// GetNotice returns a DMCA notice by ID.
func (s *DMCAService) GetNotice(ctx context.Context, id string) (*models.DMCANotice, error) {
	return s.dmcaRepo.FindByID(ctx, id)
}

// ListNotices returns all DMCA notices.
func (s *DMCAService) ListNotices(ctx context.Context) ([]*models.DMCANotice, error) {
	return s.dmcaRepo.List(ctx)
}

// UpdateStatus updates a DMCA notice status. If rejected, un-quarantines the asset.
func (s *DMCAService) UpdateStatus(ctx context.Context, id string, status string) error {
	if err := s.dmcaRepo.UpdateStatus(ctx, id, status); err != nil {
		return err
	}

	if status == models.DMCAStatusRejected {
		notice, err := s.dmcaRepo.FindByID(ctx, id)
		if err != nil {
			return err
		}
		return s.assetRepo.SetQuarantined(ctx, notice.CID, false)
	}

	return nil
}

func validateFileNotice(req FileNoticeRequest) error {
	if req.CID == "" {
		return models.ErrInvalidInput
	}
	if req.ReporterEmail == "" {
		return models.ErrInvalidInput
	}
	return nil
}
