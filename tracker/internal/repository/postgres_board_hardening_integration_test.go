// Package repository: Postgres integration test for the community hardening pass: the bounty
// lifecycle statements that fail closed (AwardBounty only on an open bounty, ExpireBounty only
// on an open bounty, RevertBountyAward, ListExpiredOpenBounties including expired escrows
// without a refund) and every board read with an id that is not a UUID (never a Postgres
// error, always ErrNotFound or empty). Runs only with TRACKER_TEST_DATABASE_URL set, like the
// phase 1 to 3 tests; rows are scoped to a random prefix and deleted at the end.
package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stonkagents/agent/tracker/internal/models"
)

func TestPostgres_BountyLifecycleFailsClosed(t *testing.T) {
	pool := pgTestPool(t)
	ctx := context.Background()
	run := "ith-" + uuid.New().String()[:8] + "-"
	author, winner := run+"author", run+"winner"
	now := time.Now().UTC().Truncate(time.Microsecond)

	peers := NewPostgresPeerRepository(pool)
	for _, id := range []string{author, winner} {
		if err := peers.Create(ctx, &models.Peer{PeerID: id, PublicKey: "pk-" + id, Multiaddrs: []string{}, FirstSeen: now.Add(-48 * time.Hour), LastSeen: now}); err != nil {
			t.Fatalf("create peer %s: %v", id, err)
		}
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(ctx, `DELETE FROM peers WHERE peer_id LIKE $1`, run+"%"); err != nil {
			t.Logf("cleanup: %v", err)
		}
	})
	forum := NewPostgresForumRepository(pool)
	newBounty := func(title string, status string, expiresAt time.Time, escrow string) *models.ForumPost {
		amt := 100
		p := &models.ForumPost{AuthorPeerID: author, Title: run + title, Description: "b", Category: "bounty", Tags: []string{},
			CreatedAt: now, UpdatedAt: now, BountyAmount: &amt, BountyStatus: status, BountyExpiresAt: &expiresAt}
		if escrow != "" {
			id := run + escrow
			p.BountyEscrowRequestID = &id
		}
		if err := forum.CreatePost(ctx, p); err != nil {
			t.Fatalf("create post %s: %v", title, err)
		}
		return p
	}
	past, future := now.Add(-time.Hour), now.Add(24*time.Hour)

	// AwardBounty: once, and only while open.
	open := newBounty("open", "open", future, "e1")
	if err := forum.AwardBounty(ctx, open.ID, winner); err != nil {
		t.Fatalf("first award: %v", err)
	}
	if err := forum.AwardBounty(ctx, open.ID, winner); !errors.Is(err, models.ErrInvalidInput) {
		t.Errorf("second award = %v, want ErrInvalidInput", err)
	}
	if err := forum.ExpireBounty(ctx, open.ID); !errors.Is(err, models.ErrInvalidInput) {
		t.Errorf("expire of a completed bounty = %v, want ErrInvalidInput", err)
	}
	if got, _ := forum.GetPostByID(ctx, open.ID); got.BountyStatus != "completed" || got.BountyClaimedBy == nil || *got.BountyClaimedBy != winner {
		t.Errorf("after award: %+v", got)
	}
	// RevertBountyAward reopens it; a second award then works again.
	if err := forum.RevertBountyAward(ctx, open.ID); err != nil {
		t.Fatalf("revert: %v", err)
	}
	if got, _ := forum.GetPostByID(ctx, open.ID); got.BountyStatus != "open" || got.BountyClaimedBy != nil || got.BountyCompletedAt != nil {
		t.Errorf("after revert: status=%q claimed_by=%v completed_at=%v", got.BountyStatus, got.BountyClaimedBy, got.BountyCompletedAt)
	}
	if err := forum.AwardBounty(ctx, open.ID, winner); err != nil {
		t.Errorf("award after revert: %v", err)
	}

	// ExpireBounty: open to expired once; an expired bounty cannot be awarded.
	due := newBounty("due", "open", past, "e2")
	if err := forum.ExpireBounty(ctx, due.ID); err != nil {
		t.Fatalf("expire: %v", err)
	}
	if err := forum.ExpireBounty(ctx, due.ID); !errors.Is(err, models.ErrInvalidInput) {
		t.Errorf("second expire = %v, want ErrInvalidInput", err)
	}
	if err := forum.AwardBounty(ctx, due.ID, winner); !errors.Is(err, models.ErrInvalidInput) {
		t.Errorf("award of an expired bounty = %v, want ErrInvalidInput", err)
	}

	// ListExpiredOpenBounties: open past the deadline, plus expired escrows without a refund;
	// never a refunded one, a legacy one without an escrow id, or an open one still in time.
	unrefunded := newBounty("unrefunded", "expired", past, "e3")
	refunded := newBounty("refunded", "expired", past, "e4")
	if err := forum.SetBountyRefundedAt(ctx, refunded.ID, now); err != nil {
		t.Fatal(err)
	}
	legacy := newBounty("legacy", "expired", past, "")
	stillOpen := newBounty("still-open", "open", future, "e5")
	pastOpen := newBounty("past-open", "open", past, "e6")
	list, err := forum.ListExpiredOpenBounties(ctx, now)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	got := map[string]bool{}
	for _, p := range list {
		got[p.ID] = true
	}
	for _, want := range []*models.ForumPost{unrefunded, pastOpen} {
		if !got[want.ID] {
			t.Errorf("list must include %s", want.Title)
		}
	}
	for _, not := range []*models.ForumPost{refunded, legacy, stillOpen, due, open} {
		if not.ID == due.ID {
			// due is expired with an escrow id and no refund yet: it belongs in the list.
			continue
		}
		if got[not.ID] {
			t.Errorf("list must not include %s", not.Title)
		}
	}
	if !got[due.ID] {
		t.Errorf("list must include the expired unrefunded %s", due.Title)
	}
}

// TestPostgres_NonUUIDIdsReadAsNotFound: every board statement keyed by a post or reply id
// answers ErrNotFound (or nothing) for an id that is not a UUID rather than a Postgres error
// (22P02), so a malformed path never turns into a 500.
func TestPostgres_NonUUIDIdsReadAsNotFound(t *testing.T) {
	pool := pgTestPool(t)
	ctx := context.Background()
	forum := NewPostgresForumRepository(pool)
	const bad = "not-a-uuid"
	if _, err := forum.GetPostByID(ctx, bad); !errors.Is(err, models.ErrNotFound) {
		t.Errorf("GetPostByID = %v, want ErrNotFound", err)
	}
	if _, err := forum.GetReplyByID(ctx, bad); !errors.Is(err, models.ErrNotFound) {
		t.Errorf("GetReplyByID = %v, want ErrNotFound", err)
	}
	if _, n, err := forum.ListRepliesFiltered(ctx, bad, ReplyQuery{Limit: 10}); err != nil || n != 0 {
		t.Errorf("ListRepliesFiltered = n %d err %v, want 0 and nil", n, err)
	}
	if _, err := forum.IncrementViewCount(ctx, bad); !errors.Is(err, models.ErrNotFound) {
		t.Errorf("IncrementViewCount = %v, want ErrNotFound", err)
	}
	if posts, err := forum.GetPostsByIDs(ctx, []string{bad}); err != nil || len(posts) != 0 {
		t.Errorf("GetPostsByIDs = %v err %v, want empty", posts, err)
	}
	if err := forum.AddUpvote(ctx, "nobody", bad); !errors.Is(err, models.ErrNotFound) {
		t.Errorf("AddUpvote = %v, want ErrNotFound", err)
	}
	if got, err := forum.BatchHasUpvoted(ctx, "nobody", []string{bad}); err != nil || len(got) != 0 {
		t.Errorf("BatchHasUpvoted = %v err %v, want empty", got, err)
	}
	if err := forum.AwardBounty(ctx, bad, "nobody"); !errors.Is(err, models.ErrInvalidInput) {
		t.Errorf("AwardBounty = %v, want ErrInvalidInput", err)
	}
	if n, err := forum.CountAutoRepliesByPeerOnPost(ctx, "nobody", bad); err != nil || n != 0 {
		t.Errorf("CountAutoRepliesByPeerOnPost = %d err %v, want 0 and nil", n, err)
	}
	if askers, err := forum.AskerPeerIDs(ctx, bad); err != nil || len(askers) != 0 {
		t.Errorf("AskerPeerIDs = %v err %v, want empty", askers, err)
	}
	if err := forum.SetPostHidden(ctx, bad, true); !errors.Is(err, models.ErrNotFound) {
		t.Errorf("SetPostHidden = %v, want ErrNotFound", err)
	}
	if err := forum.SetReplyHidden(ctx, bad, true); !errors.Is(err, models.ErrNotFound) {
		t.Errorf("SetReplyHidden = %v, want ErrNotFound", err)
	}
	if err := forum.SetAcceptedReply(ctx, bad, nil); !errors.Is(err, models.ErrNotFound) {
		t.Errorf("SetAcceptedReply = %v, want ErrNotFound", err)
	}
	if _, err := forum.RaiseBounty(ctx, bad, 10, "r", time.Now()); !errors.Is(err, models.ErrInvalidInput) {
		t.Errorf("RaiseBounty = %v, want ErrInvalidInput", err)
	}
	if _, err := forum.IncrementTokenOfferPaid(ctx, bad); !errors.Is(err, models.ErrNotFound) && !errors.Is(err, models.ErrInvalidInput) {
		t.Errorf("IncrementTokenOfferPaid = %v, want ErrNotFound or ErrInvalidInput", err)
	}
}
