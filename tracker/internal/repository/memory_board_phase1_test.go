// Package repository: Tests for the community board phase 1 memory repositories: display
// name lookups for mentions and autocomplete, the forum visibility and pin rules, reports,
// watches and token offer payments.
package repository

import (
	"context"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
)

func TestMemoryPeerRepo_DisplayNameLookups(t *testing.T) {
	r := NewMemoryPeerRepository()
	ctx := context.Background()
	for id, name := range map[string]string{"a": "Alice", "b": "alice_b", "c": "Bob", "d": "", "e": "al"} {
		_ = r.Create(ctx, &models.Peer{PeerID: id, DisplayName: name})
	}
	got, err := r.PeerIDsByDisplayNames(ctx, []string{"alice", "ALICE_B", "nobody", ""})
	if err != nil || len(got) != 2 || got["alice"] != "a" || got["alice_b"] != "b" {
		t.Errorf("PeerIDsByDisplayNames = %v err=%v", got, err)
	}
	matches, err := r.SearchDisplayNames(ctx, "AL", 10)
	if err != nil || len(matches) != 3 || matches[0].DisplayName != "al" || matches[1].DisplayName != "Alice" || matches[2].DisplayName != "alice_b" {
		t.Errorf("SearchDisplayNames = %v err=%v (shortest first)", matches, err)
	}
	if matches, _ := r.SearchDisplayNames(ctx, "al", 1); len(matches) != 1 {
		t.Errorf("limit: %v", matches)
	}
}

func TestMemoryForumRepo_Phase1(t *testing.T) {
	r := NewMemoryForumRepository()
	ctx := context.Background()
	now := time.Date(2026, 9, 16, 9, 0, 0, 0, time.UTC)
	a := &models.ForumPost{AuthorPeerID: "alice", Title: "a", Description: "a", CreatedAt: now}
	b := &models.ForumPost{AuthorPeerID: "bob", Title: "b", Description: "b", CreatedAt: now.Add(time.Minute)}
	_ = r.CreatePost(ctx, a)
	_ = r.CreatePost(ctx, b)
	rp := &models.ForumReply{PostID: a.ID, AuthorPeerID: "bob", Body: "r", CreatedAt: now.Add(time.Second), MentionPeerIDs: []string{"alice"}}
	_ = r.CreateReply(ctx, rp)

	got, err := r.GetReplyByID(ctx, rp.ID)
	if err != nil || got.AuthorPeerID != "bob" || len(got.MentionPeerIDs) != 1 {
		t.Errorf("GetReplyByID = %+v err=%v", got, err)
	}
	if _, err := r.GetReplyByID(ctx, "nope"); err != models.ErrNotFound {
		t.Errorf("unknown reply: %v", err)
	}
	id := rp.ID
	if err := r.SetAcceptedReply(ctx, a.ID, &id); err != nil {
		t.Fatalf("accept: %v", err)
	}
	if p, _ := r.GetPostByID(ctx, a.ID); p.AcceptedReplyID == nil || *p.AcceptedReplyID != id {
		t.Error("accepted reply not stored")
	}
	_ = r.SetAcceptedReply(ctx, a.ID, nil)
	if p, _ := r.GetPostByID(ctx, a.ID); p.AcceptedReplyID != nil {
		t.Error("accepted reply not cleared")
	}
	// Pin: a first despite being older; pin b replaces.
	_ = r.PinPost(ctx, a.ID)
	posts, _, _ := r.QueryPosts(ctx, PostQuery{})
	if posts[0].ID != a.ID {
		t.Errorf("pinned first: %s", posts[0].ID)
	}
	_ = r.PinPost(ctx, b.ID)
	if p, _ := r.GetPostByID(ctx, a.ID); p.Pinned {
		t.Error("pin must replace")
	}
	_ = r.UnpinPost(ctx, b.ID)
	// Hidden post filter.
	_ = r.SetPostHidden(ctx, a.ID, true)
	if posts, total, _ := r.QueryPosts(ctx, PostQuery{}); total != 1 || len(posts) != 1 {
		t.Errorf("hidden visible anonymously: %d", total)
	}
	if _, total, _ := r.QueryPosts(ctx, PostQuery{Viewer: "alice"}); total != 2 {
		t.Errorf("hidden not visible to author: %d", total)
	}
	if _, total, _ := r.QueryPosts(ctx, PostQuery{ShowHidden: true}); total != 2 {
		t.Errorf("hidden not visible with ShowHidden: %d", total)
	}
	if c, _ := r.CountPosts(ctx, now, ""); c.All != 1 {
		t.Errorf("counts include hidden: %d", c.All)
	}
	// Hidden reply filter.
	_ = r.SetReplyHidden(ctx, rp.ID, true)
	if replies, total, _ := r.ListRepliesFiltered(ctx, a.ID, ReplyQuery{}); total != 0 || len(replies) != 0 {
		t.Errorf("hidden reply visible: %d", total)
	}
	if replies, _, _ := r.ListRepliesFiltered(ctx, a.ID, ReplyQuery{Viewer: "bob"}); len(replies) != 1 || !replies[0].Hidden {
		t.Errorf("hidden reply not visible to author: %v", replies)
	}
	if replies, _, _ := r.ListRepliesByPostID(ctx, a.ID, 10, 0); len(replies) != 1 {
		t.Error("ListRepliesByPostID must include hidden replies (service internals)")
	}
	if n, err := r.IncrementTokenOfferPaid(ctx, a.ID); err != nil || n != 1 {
		t.Errorf("paid = %d err=%v", n, err)
	}
	if _, err := r.IncrementTokenOfferPaid(ctx, "nope"); err != models.ErrNotFound {
		t.Errorf("paid unknown: %v", err)
	}
	if ids, _ := r.BoardPeerIDs(ctx); len(ids) != 2 || ids[0] != "alice" || ids[1] != "bob" {
		t.Errorf("BoardPeerIDs = %v", ids)
	}
}

func TestMemoryBoardPhase1Repos(t *testing.T) {
	ctx := context.Background()
	now := time.Now()

	reports := NewMemoryBoardReportRepository()
	first := &models.BoardReport{TargetType: "post", TargetID: "p1", TargetAuthorPeerID: "alice", ReporterPeerID: "bob", Reason: "spam", CreatedAt: now}
	if err := reports.Create(ctx, first); err != nil || first.ID == "" || first.Status != "open" {
		t.Fatalf("create: %+v err=%v", first, err)
	}
	if err := reports.Create(ctx, &models.BoardReport{TargetType: "post", TargetID: "p1", ReporterPeerID: "bob", Reason: "abuse"}); err != models.ErrAlreadyExists {
		t.Errorf("duplicate: %v", err)
	}
	second := &models.BoardReport{TargetType: "post", TargetID: "p1", TargetAuthorPeerID: "alice", ReporterPeerID: "carol", Reason: "scam", CreatedAt: now.Add(time.Second)}
	_ = reports.Create(ctx, second)
	if ids, _ := reports.ReporterPeerIDs(ctx, "post", "p1"); len(ids) != 2 {
		t.Errorf("reporters = %v", ids)
	}
	_ = reports.SetStatus(ctx, second.ID, "dismissed", now)
	if ids, _ := reports.ReporterPeerIDs(ctx, "post", "p1"); len(ids) != 1 || ids[0] != "bob" {
		t.Errorf("dismissed reporter still counted: %v", ids)
	}
	_ = reports.SetStatus(ctx, first.ID, "upheld", now)
	if n, _ := reports.CountUpheldAgainst(ctx, "alice"); n != 1 {
		t.Errorf("upheld against alice = %d", n)
	}
	if list, _ := reports.List(ctx, "", 10); len(list) != 2 || list[0].ID != second.ID {
		t.Errorf("list newest first: %v", list)
	}
	if list, _ := reports.List(ctx, "open", 10); len(list) != 0 {
		t.Errorf("open list: %v", list)
	}
	if err := reports.SetStatus(ctx, "nope", "upheld", now); err != models.ErrNotFound {
		t.Errorf("set status unknown: %v", err)
	}

	watches := NewMemoryBoardWatchRepository()
	_ = watches.AddIfAbsent(ctx, "p1", "alice", now)
	_ = watches.Set(ctx, "p1", "alice", false, now)
	_ = watches.AddIfAbsent(ctx, "p1", "alice", now)
	if w, _ := watches.IsWatching(ctx, "p1", "alice"); w {
		t.Error("AddIfAbsent must not undo an explicit unwatch")
	}
	_ = watches.AddIfAbsent(ctx, "p1", "bob", now)
	_ = watches.Set(ctx, "p1", "carol", true, now)
	if ws, _ := watches.Watchers(ctx, "p1"); len(ws) != 2 || ws[0] != "bob" || ws[1] != "carol" {
		t.Errorf("watchers = %v", ws)
	}
	if m, _ := watches.WatchingByPostIDs(ctx, "carol", []string{"p1", "p2"}); !m["p1"] || m["p2"] {
		t.Errorf("watching by ids = %v", m)
	}

	payments := NewMemoryTokenOfferPaymentRepository()
	pay := &models.TokenOfferPayment{Signature: "s1", PostID: "p1", ReplyID: "r1", FromWallet: "a", ToWallet: "b", AmountRaw: 5, VerifiedAt: now}
	if err := payments.Create(ctx, pay); err != nil {
		t.Fatalf("create payment: %v", err)
	}
	if err := payments.Create(ctx, &models.TokenOfferPayment{Signature: "s1", PostID: "p1", ReplyID: "r2"}); err != models.ErrAlreadyExists {
		t.Errorf("same signature: %v", err)
	}
	if err := payments.Create(ctx, &models.TokenOfferPayment{Signature: "s2", PostID: "p1", ReplyID: "r1"}); err != models.ErrAlreadyExists {
		t.Errorf("same reply: %v", err)
	}
	if seen, _ := payments.HasSignature(ctx, "s1"); !seen {
		t.Error("signature not seen")
	}
	if paid, _ := payments.PaidReplyIDs(ctx, "p1"); !paid["r1"] || len(paid) != 1 {
		t.Errorf("paid = %v", paid)
	}

	rep := NewMemoryBoardReputationRepository()
	_ = rep.Upsert(ctx, &models.PeerReputation{PeerID: "alice", Score: 30, Tier: "active"})
	_ = rep.Upsert(ctx, &models.PeerReputation{PeerID: "alice", Score: 31, Tier: "active"})
	if got, err := rep.Get(ctx, "alice"); err != nil || got.Score != 31 {
		t.Errorf("upsert replaces: %+v err=%v", got, err)
	}
	if _, err := rep.Get(ctx, "nobody"); err != models.ErrNotFound {
		t.Errorf("unknown: %v", err)
	}
	if m, _ := rep.GetByIDs(ctx, []string{"alice", "nobody"}); len(m) != 1 || m["alice"].Score != 31 {
		t.Errorf("by ids = %v", m)
	}
}
