package services

import (
	"context"
	"testing"

	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/repository"
)

func TestLaunchRecordSourceResolver_UsesRecordWithHint(t *testing.T) {
	repo := repository.NewMemoryLaunchRepository()
	if err := repo.Create(context.Background(), &models.TokenLaunch{
		Mint: "MintAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", PoolID: "PoolBBBB", QuoteMint: "QuoteCCCC",
		CreatorWallet: "CreatorDDDD", LaunchSignature: "SigEEEE",
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	r := NewLaunchRecordSourceResolver(repo, nil)

	res := r.Resolve(context.Background(), &models.PeerToken{TokenContractAddress: "MintAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"})
	if res.Source != models.TokenSourceLaunchLab {
		t.Fatalf("source = %q, want launchlab", res.Source)
	}
	if res.Hint.PoolID != "PoolBBBB" || res.Hint.QuoteMint != "QuoteCCCC" {
		t.Fatalf("hint = %+v, want pool/quote from record", res.Hint)
	}
}

func TestLaunchRecordSourceResolver_FallsBackForUnknownMint(t *testing.T) {
	r := NewLaunchRecordSourceResolver(repository.NewMemoryLaunchRepository(), nil)

	if got := r.Resolve(context.Background(), &models.PeerToken{TokenContractAddress: "abc123pump"}); got.Source != models.TokenSourcePumpFun {
		t.Fatalf("pump suffix should fall back to pump.fun, got %q", got.Source)
	}
	if got := r.Resolve(context.Background(), &models.PeerToken{TokenContractAddress: "SomeOtherMint"}); got.Source != models.TokenSourceLaunchLab || got.Hint.PoolID != "" {
		t.Fatalf("unknown mint should fall back to default launchlab with no hint, got %+v", got)
	}
	if got := r.Resolve(context.Background(), nil); got.Source != models.TokenSourceLaunchLab {
		t.Fatalf("nil token should resolve to default, got %+v", got)
	}
}
