// Package: tracker/internal/repository
// Feature: StonkAgents launchpad — launch thumbnails
// Purpose: The in-memory launch repository keeps image_thumb_url through create and every read.

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
)

func TestMemoryLaunchRepository_ImageThumbURL_RoundTrip(t *testing.T) {
	repo := NewMemoryLaunchRepository()
	ctx := context.Background()
	const thumb = "https://gateway.example/ipfs/QmThumb"
	launch := &models.TokenLaunch{
		Mint: "MintThumb", CreatorWallet: "WalletA", QuoteMint: models.SOLMint, Name: "Thumb", Symbol: "THMB",
		ImageURL: "https://gateway.example/ipfs/QmImage", ImageThumbURL: thumb,
		LaunchSignature: "SigThumb", FeeLamports: 1, TransferFeeBps: 100, CreatedAt: time.Now().UTC(),
	}
	if err := repo.Create(ctx, launch); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Mutating the caller's struct after Create must not leak into the store.
	launch.ImageThumbURL = "mutated"

	byMint, err := repo.GetByMint(ctx, "MintThumb")
	if err != nil || byMint.ImageThumbURL != thumb {
		t.Fatalf("GetByMint = %+v, %v; want thumb %q", byMint, err, thumb)
	}
	bySig, err := repo.GetBySignature(ctx, "SigThumb")
	if err != nil || bySig.ImageThumbURL != thumb {
		t.Fatalf("GetBySignature = %+v, %v", bySig, err)
	}
	list, total, err := repo.List(ctx, ListLaunchesOptions{CreatorWallet: "WalletA", Limit: 10})
	if err != nil || total != 1 || len(list) != 1 || list[0].ImageThumbURL != thumb {
		t.Fatalf("List = %+v, %d, %v", list, total, err)
	}

	// A launch recorded before thumbnails existed reads back empty (the API renders null).
	old := &models.TokenLaunch{Mint: "MintOld", CreatorWallet: "WalletB", QuoteMint: models.SOLMint, LaunchSignature: "SigOld", CreatedAt: time.Now().UTC()}
	if err := repo.Create(ctx, old); err != nil {
		t.Fatal(err)
	}
	got, _ := repo.GetByMint(ctx, "MintOld")
	if got.ImageThumbURL != "" {
		t.Errorf("legacy launch thumb = %q, want empty", got.ImageThumbURL)
	}
}
