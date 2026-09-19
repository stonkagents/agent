// Package repository: Postgres integration test for the dev seeding hardening (migration
// 029). Runs only with TRACKER_TEST_DATABASE_URL set, like the phase 1 to 3 tests; rows are
// scoped to a random peer id prefix and deleted at the end. Covers the peer profile columns
// (NOT NULL after the backfill, blank rows scan through FindByID / FindByIDs / the lists) and
// the report resolution on auto hide (ResolveOpenForTarget, resolution_note round trip).
package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stonkagents/agent/tracker/internal/models"
)

func TestPostgres_HardeningPeerProfileAndReportResolution(t *testing.T) {
	pool := pgTestPool(t)
	ctx := context.Background()
	run := "it-" + uuid.New().String()[:8] + "-"
	alice, bob, carol := run+"alice", run+"bob", run+"carol"
	now := time.Now().UTC().Truncate(time.Microsecond)

	peers := NewPostgresPeerRepository(pool)
	t.Cleanup(func() {
		for _, q := range []string{
			`DELETE FROM board_reports WHERE reporter_peer_id LIKE $1`,
			`DELETE FROM peers WHERE peer_id LIKE $1`,
		} {
			if _, err := pool.Exec(ctx, q, run+"%"); err != nil {
				t.Logf("cleanup %q: %v", q, err)
			}
		}
	})

	// Migration 029: the three profile columns are NOT NULL DEFAULT '' and a NULL is refused.
	for _, col := range []string{"country", "region", "masked_peer_id"} {
		var nullable, def string
		err := pool.QueryRow(ctx, `SELECT is_nullable, COALESCE(column_default, '') FROM information_schema.columns
			WHERE table_name = 'peers' AND column_name = $1`, col).Scan(&nullable, &def)
		if err != nil || nullable != "NO" || def == "" {
			t.Errorf("peers.%s: nullable=%q default=%q err=%v, want NOT NULL with a default", col, nullable, def, err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO peers (peer_id, ed25519_pubkey, country) VALUES ($1, 'pk', NULL)`, run+"null"); err == nil {
		t.Error("inserting a NULL country must fail after migration 029")
	}
	// A row inserted without the columns gets '' (the default), not NULL, and scans.
	if _, err := pool.Exec(ctx, `INSERT INTO peers (peer_id, ed25519_pubkey, first_seen, last_seen) VALUES ($1, 'pk', $2, $2)`, alice, now.Add(-48*time.Hour)); err != nil {
		t.Fatalf("insert alice: %v", err)
	}
	for _, p := range []*models.Peer{
		{PeerID: bob, PublicKey: "pk-" + bob, Multiaddrs: []string{}, FirstSeen: now.Add(-48 * time.Hour), LastSeen: now},
		{PeerID: carol, PublicKey: "pk-" + carol, Multiaddrs: []string{}, FirstSeen: now.Add(-48 * time.Hour), LastSeen: now, Country: "SE", Region: "Stockholm", MaskedPeerID: "abcd"},
	} {
		if err := peers.Create(ctx, p); err != nil {
			t.Fatalf("create peer %s: %v", p.PeerID, err)
		}
	}
	// The auto hide lookup: every reporter comes back, blank profile or not.
	got, err := peers.FindByIDs(ctx, []string{alice, bob, carol})
	if err != nil || len(got) != 3 {
		t.Fatalf("FindByIDs = %d peers err=%v, want 3", len(got), err)
	}
	for _, p := range got {
		if p.FirstSeen.IsZero() {
			t.Errorf("%s: first_seen not scanned", p.PeerID)
		}
		if p.PeerID == carol && (p.Country != "SE" || p.Region != "Stockholm" || p.MaskedPeerID != "abcd") {
			t.Errorf("carol profile: %+v", p)
		}
		if p.PeerID == alice && (p.Country != "" || p.Region != "" || p.MaskedPeerID != "") {
			t.Errorf("alice profile must be blank: %+v", p)
		}
	}
	if p, err := peers.FindByID(ctx, alice); err != nil || p.PeerID != alice {
		t.Errorf("FindByID alice: %+v err=%v", p, err)
	}
	if _, err := peers.RecentlyJoined(ctx, 5); err != nil {
		t.Errorf("RecentlyJoined: %v", err)
	}
	if _, err := peers.ListPeersByUploadBytes(ctx, 5, 0, false); err != nil {
		t.Errorf("ListPeersByUploadBytes: %v", err)
	}

	// Reports: three open reports on one target, one already dismissed, one on another target.
	reports := NewPostgresBoardReportRepository(pool)
	target := run + "post"
	open := []*models.BoardReport{}
	for i, who := range []string{alice, bob, carol} {
		r := &models.BoardReport{TargetType: "post", TargetID: target, TargetAuthorPeerID: run + "author", ReporterPeerID: who, Reason: "spam", CreatedAt: now.Add(time.Duration(i) * time.Second)}
		if err := reports.Create(ctx, r); err != nil {
			t.Fatalf("create report: %v", err)
		}
		open = append(open, r)
	}
	if err := reports.SetStatus(ctx, open[0].ID, models.ReportStatusDismissed, now); err != nil {
		t.Fatalf("dismiss: %v", err)
	}
	other := &models.BoardReport{TargetType: "reply", TargetID: run + "reply", TargetAuthorPeerID: run + "author", ReporterPeerID: bob, Reason: "abuse", CreatedAt: now}
	if err := reports.Create(ctx, other); err != nil {
		t.Fatalf("create other report: %v", err)
	}
	resolvedAt := now.Add(time.Minute)
	n, err := reports.ResolveOpenForTarget(ctx, "post", target, models.ReportStatusUpheld, models.ReportResolutionAutoHidden, resolvedAt)
	if err != nil || n != 2 {
		t.Fatalf("ResolveOpenForTarget = %d err=%v, want 2 (the dismissed one and the other target are untouched)", n, err)
	}
	for _, r := range open[1:] {
		got, err := reports.Get(ctx, r.ID)
		if err != nil || got.Status != models.ReportStatusUpheld || got.ResolutionNote != models.ReportResolutionAutoHidden || got.ResolvedAt == nil || !got.ResolvedAt.Equal(resolvedAt) {
			t.Errorf("report %s after auto hide: %+v err=%v", r.ID, got, err)
		}
	}
	if got, _ := reports.Get(ctx, open[0].ID); got.Status != models.ReportStatusDismissed || got.ResolutionNote != "" {
		t.Errorf("dismissed report touched: %+v", got)
	}
	if got, _ := reports.Get(ctx, other.ID); got.Status != models.ReportStatusOpen {
		t.Errorf("other target touched: %+v", got)
	}
	// Auto-upheld reports carry no reputation penalty; a platform uphold (SetStatus) of one of
	// them clears the note and counts.
	if n, err := reports.CountUpheldAgainst(ctx, run+"author"); err != nil || n != 0 {
		t.Errorf("CountUpheldAgainst after auto hide = %d err=%v, want 0", n, err)
	}
	if err := reports.SetStatus(ctx, open[1].ID, models.ReportStatusUpheld, resolvedAt.Add(time.Minute)); err != nil {
		t.Fatalf("platform uphold: %v", err)
	}
	if got, _ := reports.Get(ctx, open[1].ID); got.Status != models.ReportStatusUpheld || got.ResolutionNote != "" {
		t.Errorf("confirmed report: %+v", got)
	}
	if n, err := reports.CountUpheldAgainst(ctx, run+"author"); err != nil || n != 1 {
		t.Errorf("CountUpheldAgainst after the platform uphold = %d err=%v, want 1", n, err)
	}
	// A second pass finds nothing open.
	if n, err := reports.ResolveOpenForTarget(ctx, "post", target, models.ReportStatusUpheld, models.ReportResolutionAutoHidden, resolvedAt); err != nil || n != 0 {
		t.Errorf("second ResolveOpenForTarget = %d err=%v, want 0", n, err)
	}
	// The list carries the note.
	list, err := reports.List(ctx, models.ReportStatusUpheld, 500)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	seen := 0
	for _, r := range list {
		if r.TargetID == target && r.ResolutionNote == models.ReportResolutionAutoHidden {
			seen++
		}
	}
	if seen != 1 {
		t.Errorf("upheld list with note = %d, want 1 (the other was confirmed by the platform)", seen)
	}
}
