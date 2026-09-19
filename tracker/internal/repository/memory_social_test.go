// Package: tracker/internal/repository
// Feature: F-013 (Credits & Identity)
// Story: US-013-05 (Social Connections)
// Purpose: Tests for in-memory SocialRepository

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
)

func newTestSocial(accountID, platform string) *models.SocialConnection {
	return &models.SocialConnection{
		ID:             "sc-" + accountID + "-" + platform,
		AccountID:      accountID,
		Platform:       platform,
		PlatformUserID: "ext-user-123",
		VerifiedAt:     time.Now(),
		BonusGranted:   50,
	}
}

func TestMemorySocialRepo_Insert_Success(t *testing.T) {
	repo := NewMemorySocialRepository()
	ctx := context.Background()

	conn := newTestSocial("acc-1", models.PlatformGitHub)
	err := repo.Insert(ctx, conn)
	if err != nil {
		t.Fatalf("Insert() unexpected error: %v", err)
	}

	found, err := repo.GetByAccountAndPlatform(ctx, "acc-1", models.PlatformGitHub)
	if err != nil {
		t.Fatalf("GetByAccountAndPlatform() unexpected error: %v", err)
	}
	if found.PlatformUserID != "ext-user-123" {
		t.Errorf("PlatformUserID = %q, want %q", found.PlatformUserID, "ext-user-123")
	}
}

func TestMemorySocialRepo_Insert_DuplicatePlatform(t *testing.T) {
	repo := NewMemorySocialRepository()
	ctx := context.Background()

	_ = repo.Insert(ctx, newTestSocial("acc-1", models.PlatformGitHub))
	err := repo.Insert(ctx, newTestSocial("acc-1", models.PlatformGitHub))

	if err != models.ErrAlreadyExists {
		t.Errorf("Insert() duplicate got error = %v, want ErrAlreadyExists", err)
	}
}

func TestMemorySocialRepo_GetByAccountID(t *testing.T) {
	repo := NewMemorySocialRepository()
	ctx := context.Background()

	_ = repo.Insert(ctx, newTestSocial("acc-1", models.PlatformGitHub))
	_ = repo.Insert(ctx, newTestSocial("acc-1", models.PlatformTwitter))

	conns, err := repo.GetByAccountID(ctx, "acc-1")
	if err != nil {
		t.Fatalf("GetByAccountID() unexpected error: %v", err)
	}
	if len(conns) != 2 {
		t.Errorf("GetByAccountID() got %d connections, want 2", len(conns))
	}
}

func TestMemorySocialRepo_GetByAccountAndPlatform_NotFound(t *testing.T) {
	repo := NewMemorySocialRepository()
	ctx := context.Background()

	_, err := repo.GetByAccountAndPlatform(ctx, "acc-1", models.PlatformGitHub)
	if err != models.ErrNotFound {
		t.Errorf("GetByAccountAndPlatform() got error = %v, want ErrNotFound", err)
	}
}

func TestMemorySocialRepo_Delete(t *testing.T) {
	repo := NewMemorySocialRepository()
	ctx := context.Background()

	_ = repo.Insert(ctx, newTestSocial("acc-1", models.PlatformGitHub))

	err := repo.Delete(ctx, "acc-1", models.PlatformGitHub)
	if err != nil {
		t.Fatalf("Delete() unexpected error: %v", err)
	}

	_, err = repo.GetByAccountAndPlatform(ctx, "acc-1", models.PlatformGitHub)
	if err != models.ErrNotFound {
		t.Errorf("GetByAccountAndPlatform() after delete got error = %v, want ErrNotFound", err)
	}
}
