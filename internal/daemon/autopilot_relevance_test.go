// Package: internal/daemon
// Purpose: Tests for Autopilot phase 3: keyword extraction, relevance
// scoring, the library index, self-tuning and the ask arithmetic (pure parts)
// plus the watcher runs that use them.

package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stonkagents/agent/internal/config"
	"github.com/stonkagents/agent/internal/daemon/storage"
)

func TestRelevanceKeywords_Deterministic(t *testing.T) {
	text := "Need a Go reviewer! Looking for help reviewing a Go service; the service uses gRPC and Postgres. go GO"
	got := autopilotKeywords(text)
	want := []string{"service", "grpc", "postgres", "reviewer", "reviewing", "uses"}
	// "go" is two letters (dropped), "need", "looking", "for", "help", "the", "and", "a" are stop words or short.
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("keywords = %v, want %v", got, want)
	}
	if again := autopilotKeywords(text); strings.Join(again, ",") != strings.Join(got, ",") {
		t.Error("extraction must be deterministic")
	}
	// Frequency first, then alphabetical; capped at 12.
	var sb strings.Builder
	for i := 0; i < 20; i++ {
		sb.WriteString("word" + string(rune('a'+i)) + " ")
	}
	sb.WriteString("zeta zeta zeta")
	got = autopilotKeywords(sb.String())
	if len(got) != autopilotMaxKeywords || got[0] != "zeta" || got[1] != "worda" {
		t.Errorf("top 12 = %v", got)
	}
	if len(autopilotKeywords("")) != 0 || len(autopilotKeywords("the and for")) != 0 {
		t.Error("empty and stop-word-only text must yield nothing")
	}
	// Non-letters split, case folds, accents are letters.
	got = autopilotKeywords("Ünïcode-Tokens_here 123 four4four")
	if strings.Join(got, ",") != "four,tokens,ünïcode" { // "here" is a stop word
		t.Errorf("unicode split = %v", got)
	}
}

func TestRelevanceScore_Weights(t *testing.T) {
	kw := []string{"grpc", "postgres", "service", "reviewer"}
	lib := map[string]bool{"grpc": true, "postgres": true, "unrelated": true}
	cases := []struct {
		name string
		in   autopilotRelevanceInput
		want float64
		sig  autopilotRelevanceSignals
	}{
		// No library index and no instruction: only history (0.25) and routed (0.15) apply, renormalised over 0.40.
		{"nothing", autopilotRelevanceInput{keywords: kw}, 0, autopilotRelevanceSignals{}},
		{"history alone", autopilotRelevanceInput{keywords: kw, history: true}, 0.625, autopilotRelevanceSignals{History: 1}},
		{"routed alone", autopilotRelevanceInput{keywords: kw, routed: true}, 0.375, autopilotRelevanceSignals{Routed: 1}},
		// Instruction present, no library: weights 0.25 + 0.15 + 0.15 = 0.55.
		{"instruction one shared", autopilotRelevanceInput{keywords: kw, instruction: map[string]bool{"grpc": true}}, 0.273, autopilotRelevanceSignals{Instruction: 1}},
		{"instruction none shared", autopilotRelevanceInput{keywords: kw, instruction: map[string]bool{"python": true}}, 0, autopilotRelevanceSignals{}},
		{"instruction and routed", autopilotRelevanceInput{keywords: kw, instruction: map[string]bool{"grpc": true}, routed: true}, 0.545, autopilotRelevanceSignals{Instruction: 1, Routed: 1}},
		// Library present, no instruction: weights 0.45 + 0.25 + 0.15 = 0.85.
		{"library half", autopilotRelevanceInput{keywords: kw, library: lib}, 0.265, autopilotRelevanceSignals{Library: 0.5}},
		{"library none shared", autopilotRelevanceInput{keywords: kw, library: map[string]bool{"solana": true}}, 0, autopilotRelevanceSignals{}},
		{"no keywords with library", autopilotRelevanceInput{library: lib, routed: true}, 0.176, autopilotRelevanceSignals{Routed: 1}},
		// Everything applies: plain 0.45 / 0.25 / 0.15 / 0.15.
		{"all", autopilotRelevanceInput{keywords: kw, library: map[string]bool{"grpc": true, "postgres": true, "service": true, "reviewer": true}, history: true, instruction: map[string]bool{"grpc": true}, routed: true}, 1, autopilotRelevanceSignals{Library: 1, History: 1, Instruction: 1, Routed: 1}},
		{"library half with instruction", autopilotRelevanceInput{keywords: kw, library: lib, instruction: map[string]bool{"python": true}}, 0.225, autopilotRelevanceSignals{Library: 0.5}},
		{"routed with library and instruction", autopilotRelevanceInput{keywords: kw, library: lib, instruction: map[string]bool{"python": true}, routed: true}, 0.375, autopilotRelevanceSignals{Library: 0.5, Routed: 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			score, sig := autopilotRelevance(tc.in)
			if score != tc.want || sig != tc.sig {
				t.Fatalf("got %v %+v, want %v %+v", score, sig, tc.want, tc.sig)
			}
		})
	}
}

func TestRelevanceLibraryIndex_FromChunkStoreWithTTL(t *testing.T) {
	store, err := storage.NewSQLiteChunkStore(filepath.Join(t.TempDir(), "chunks.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.StoreFile("cid-md", "go-review-checklist.md", 40, 1, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.StoreChunk("cid-md", 0, "c0", []byte("Checklist for reviewing gRPC services with Postgres.")); err != nil {
		t.Fatal(err)
	}
	if err := store.StoreFile("cid-bin", "holiday-photo.png", 40, 1, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.StoreChunk("cid-bin", 0, "c1", []byte("kubernetes kubernetes")); err != nil { // binary: content not indexed
		t.Fatal(err)
	}
	big := strings.Repeat("filler ", 400) + "beyondlimit" // "beyondlimit" sits past the first 2 KB
	if err := store.StoreFile("cid-big", "notes.txt", int64(len(big)), 1, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.StoreChunk("cid-big", 0, "c2", []byte(big)); err != nil {
		t.Fatal(err)
	}

	s := &Server{chunkStore: store}
	ix := &autopilotLibraryIndex{source: s.autopilotLibrarySource}
	words := ix.keywords(apNow)
	for _, w := range []string{"review", "checklist", "grpc", "postgres", "holiday", "photo", "notes", "filler"} {
		if !words[w] {
			t.Errorf("missing %q in %v", w, words)
		}
	}
	if words["kubernetes"] || words["beyondlimit"] || words["png"] == false {
		t.Errorf("binary content or bytes past 2 KB indexed: %v", words)
	}
	if ix.files != 3 {
		t.Errorf("files = %d", ix.files)
	}
	// Within the TTL the set is served from memory; after it, rebuilt.
	if err := store.StoreFile("cid-new", "solana-anchor.md", 1, 0, nil); err != nil {
		t.Fatal(err)
	}
	if ix.keywords(apNow.Add(5 * time.Minute))["anchor"] {
		t.Error("index rebuilt inside the TTL")
	}
	if !ix.keywords(apNow.Add(11 * time.Minute))["anchor"] {
		t.Error("index not rebuilt after the TTL")
	}
	ix.invalidate()
	if !ix.keywords(apNow.Add(11 * time.Minute))["solana"] {
		t.Error("invalidate must rebuild")
	}
	// No chunk store: empty set, no panic.
	none := &autopilotLibraryIndex{source: (&Server{}).autopilotLibrarySource}
	if got := none.keywords(apNow); len(got) != 0 {
		t.Errorf("no store = %v", got)
	}
}

func TestRelevanceTuneThresholds_Table(t *testing.T) {
	policy := config.DefaultAutopilotConfig() // threshold 0.25
	policy.RelevanceThresholdByCategory = map[string]float64{"general": 0.5}
	stats := map[string]autopilotCategoryStats{
		"request":     {Posted: 10, Hits: 1}, // 10% -> up to 0.45
		"bounty":      {Posted: 6, Hits: 4},  // 67% -> down to 0.15
		"token-offer": {Posted: 8, Hits: 2},  // 25% -> kept
		"discovery":   {Posted: 4, Hits: 0},  // too few samples
		"general":     {Posted: 20, Hits: 0}, // pinned: never tuned
	}
	got := autopilotTuneThresholds(policy, stats)
	if len(got) != 3 {
		t.Fatalf("tuned = %+v", got)
	}
	if r := got["request"]; r.Threshold != 0.45 || r.Direction != "up" || r.HitRate != 0.1 || r.Posted != 10 {
		t.Errorf("request = %+v", r)
	}
	if b := got["bounty"]; b.Threshold != 0.15 || b.Direction != "down" || b.HitRate != 0.667 {
		t.Errorf("bounty = %+v", b)
	}
	if o := got["token-offer"]; o.Threshold != 0.25 || o.Direction != "" {
		t.Errorf("token-offer = %+v", o)
	}
	// Cap and floor; tuning up never lands below what the owner set.
	policy.RelevanceThreshold = 0.8
	if r := autopilotTuneThresholds(policy, stats)["request"]; r.Threshold != 0.9 || r.Direction != "up" {
		t.Errorf("cap = %+v", r)
	}
	policy.RelevanceThreshold = 0.95
	if r := autopilotTuneThresholds(policy, stats)["request"]; r.Threshold != 0.95 || r.Direction != "" {
		t.Errorf("above cap = %+v", r)
	}
	policy.RelevanceThreshold = 0.15
	if b := autopilotTuneThresholds(policy, stats)["bounty"]; b.Threshold != 0.1 {
		t.Errorf("floor = %+v", b)
	}
	// Effective threshold: pin, then tuned, then policy.
	policy.RelevanceThreshold = 0.25
	tuned := autopilotTuneThresholds(policy, stats)
	if v := autopilotEffectiveThreshold(policy, tuned, "general"); v != 0.5 {
		t.Errorf("pinned = %v", v)
	}
	if v := autopilotEffectiveThreshold(policy, tuned, "request"); v != 0.45 {
		t.Errorf("tuned = %v", v)
	}
	if v := autopilotEffectiveThreshold(policy, tuned, "discovery"); v != 0.25 {
		t.Errorf("policy = %v", v)
	}
}

func TestAskAmount_AndTrim(t *testing.T) {
	policy := config.DefaultAutopilotConfig() // multiple 3 x 10 = 30
	cases := []struct {
		bounty int
		want   int
	}{
		{0, 30}, {5, 30}, {20, 30}, {25, 40}, {31, 50}, {40, 50}, {99, 110},
	}
	for _, tc := range cases {
		var b *autopilotBounty
		if tc.bounty > 0 {
			b = &autopilotBounty{Amount: tc.bounty, Currency: "credits", Status: "open"}
		}
		if got := autopilotAskAmount(policy, b); got != tc.want {
			t.Errorf("bounty %d: ask = %d, want %d", tc.bounty, got, tc.want)
		}
	}
	// A bounty that is not open (or not in credits) counts as none.
	for _, b := range []*autopilotBounty{
		{Amount: 500, Currency: "credits", Status: "completed"},
		{Amount: 500, Currency: "credits", Status: "expired"},
		{Amount: 500, Currency: "STONK", Status: "open"},
	} {
		if got := autopilotAskAmount(policy, b); got != 30 {
			t.Errorf("non-open bounty %+v: ask = %d, want 30", b, got)
		}
	}
	policy.MinBountyMultiple = 2.5
	if got := autopilotAskAmount(policy, nil); got != 30 {
		t.Errorf("2.5 x 10 = 25 rounds to %d", got)
	}
	long := strings.Repeat("word ", 100)
	if got := autopilotTrimAsk(long); len([]rune(got)) > autopilotAskMaxChars || strings.HasSuffix(got, " ") {
		t.Errorf("trim = %q (%d)", got, len([]rune(got)))
	}
	if got := autopilotTrimAsk("I can review it " + string(rune(0x2014)) + " ask 30 credits."); strings.ContainsRune(got, 0x2014) || got != "I can review it, ask 30 credits." {
		t.Errorf("dash scrub = %q", got)
	}
}

// --- watcher runs with scoring on --------------------------------------

// withLibrary gives the test server a chunk store holding one Go review
// document, so posts about Go reviews score on the library signal.
func withLibrary(t *testing.T, s *Server) {
	t.Helper()
	store, err := storage.NewSQLiteChunkStore(filepath.Join(t.TempDir(), "chunks.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.StoreFile("cid-md", "go-service-review-checklist.md", 60, 1, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.StoreChunk("cid-md", 0, "c0", []byte("How I review a Go service: reviewer notes on gRPC, Postgres and reviewing concurrency.")); err != nil {
		t.Fatal(err)
	}
	s.chunkStore = store
}

func scoringOn(p *config.AutopilotConfig) { p.RelevanceMode = config.AutopilotRelevanceSkip }

func TestAutopilotRun_RelevanceSkipsNotesAndRecords(t *testing.T) {
	trk, fb := newFakeBoard(t)
	fb.set(func(f *fakeBoard) {
		f.posts = []autopilotPost{
			post("go", apOther, "request", 1), // "Need a Go reviewer" + "reviewing a Go service": service, reviewer, reviewing in the library
			post("py", apOther, "request", 1, func(p *autopilotPost) {
				p.Title, p.Content = "Python data pipeline", "Anyone able to build an Airflow pipeline with Pandas?"
			}),
		}
	})
	s := newAutopilotTestServer(t, trk.URL, policyWith(config.AutopilotSuggest, scoringOn))
	withLibrary(t, s)
	res := s.runAutopilotOnce(context.Background())
	if res.Suggested != 1 || res.RelevanceSkipped != 1 || res.Credits != 10 {
		t.Fatalf("run = %+v", res)
	}
	list := s.autopilotSt().store.pendingSuggestions()
	// Library present, no instruction: 0.45 / 0.85.
	if len(list) != 1 || list[0].PostID != "go" || list[0].Relevance == nil || *list[0].Relevance != 0.529 ||
		list[0].RelevanceSignals == nil || list[0].RelevanceSignals.Library != 1 || list[0].RelevanceSignals.Routed != 0 || list[0].LedgerID == "" {
		t.Errorf("suggestion = %+v", list)
	}
	// The skipped post is in cooldown (not rescored every tick) and counted once.
	if seen, ok := s.autopilotSt().store.lastSeen("py"); !ok || !seen.Equal(apNow) {
		t.Error("relevance_low must start the cooldown")
	}
	if res := s.runAutopilotOnce(context.Background()); res.RelevanceSkipped != 0 || res.Credits != 0 {
		t.Errorf("rerun = %+v", res)
	}
	dto := s.autopilotDTOFor()
	if dto.Status.Relevance != (autopilotRelevanceCounters{Scored: 2, Skipped: 1, Drafted: 1}) {
		t.Errorf("counters = %+v", dto.Status.Relevance)
	}
	if dto.Status.Ledger.Last30.Drafted != 1 || dto.Status.Ledger.Last30.Posted != 0 || len(dto.Status.Ledger.Days) != 30 ||
		dto.Status.Ledger.Days[29].Date != "2026-09-15" || dto.Status.Ledger.Days[29].Drafted != 1 || dto.Status.Ledger.Days[0].Date != "2026-08-17" {
		t.Errorf("ledger = %+v", dto.Status.Ledger)
	}
	rows := s.autopilotSt().ledger.rows()
	if len(rows) != 1 || rows[0].PostID != "go" || rows[0].Relevance != 0.529 || rows[0].Outcome != "pending" || rows[0].CreditsSpent != 10 || len(rows[0].Keywords) == 0 {
		t.Errorf("ledger rows = %+v", rows)
	}
	if _, err := os.Stat(filepath.Join(s.config.DataDir, autopilotLedgerFilename)); err != nil {
		t.Errorf("ledger file: %v", err)
	}

	// Note mode drafts the low post anyway and records the score.
	trk2, fb2 := newFakeBoard(t)
	fb2.set(func(f *fakeBoard) {
		f.posts = []autopilotPost{post("py", apOther, "request", 1, func(p *autopilotPost) {
			p.Title, p.Content = "Python data pipeline", "Anyone able to build an Airflow pipeline with Pandas?"
		})}
	})
	s2 := newAutopilotTestServer(t, trk2.URL, policyWith(config.AutopilotSuggest, func(p *config.AutopilotConfig) { p.RelevanceMode = config.AutopilotRelevanceNote }))
	if res := s2.runAutopilotOnce(context.Background()); res.Suggested != 1 || res.RelevanceSkipped != 0 {
		t.Fatalf("note run = %+v", res)
	}
	if got := s2.autopilotSt().store.pendingSuggestions(); len(got) != 1 || got[0].Relevance == nil || *got[0].Relevance != 0 {
		t.Errorf("note suggestion = %+v", got)
	}
	if c := s2.autopilotDTOFor().Status.Relevance; c != (autopilotRelevanceCounters{Scored: 1, Drafted: 1}) {
		t.Errorf("note counters = %+v", c)
	}

	// No library, no instruction: a routed post alone scores 0.15 / 0.40 = 0.375 and passes
	// the default threshold (an agent with nothing shared must not skip what is sent to it).
	trk4, fb4 := newFakeBoard(t)
	fb4.set(func(f *fakeBoard) {
		p := post("py", apOther, "request", 1, func(p *autopilotPost) {
			p.Title, p.Content = "Python data pipeline", "Anyone able to build an Airflow pipeline with Pandas?"
		})
		f.byID[p.ID] = p
		f.routedIDs = []string{"py"}
		f.posts = []autopilotPost{post("feed", apOther, "request", 1)}
	})
	s4 := newAutopilotTestServer(t, trk4.URL, policyWith(config.AutopilotSuggest, scoringOn))
	if res := s4.runAutopilotOnce(context.Background()); res.Suggested != 1 || res.Routed != 1 || res.RelevanceSkipped != 1 {
		t.Fatalf("bare routed run = %+v", res)
	}
	if got := s4.autopilotSt().store.pendingSuggestions(); len(got) != 1 || got[0].PostID != "py" || *got[0].Relevance != 0.375 {
		t.Errorf("bare routed suggestion = %+v", got)
	}

	// The gate only holds back unrouted, unmentioned feed posts: a routed post with a library
	// and no overlap (0.15 / 0.85 = 0.176) and a direct @mention scoring 0 are both drafted,
	// score recorded; the plain feed post scoring 0 is skipped.
	trk5, fb5 := newFakeBoard(t)
	fb5.set(func(f *fakeBoard) {
		p := post("py", apOther, "request", 1, func(p *autopilotPost) {
			p.Title, p.Content = "Python data pipeline", "Anyone able to build an Airflow pipeline with Pandas?"
		})
		f.byID[p.ID] = p
		f.routedIDs = []string{"py"}
		f.posts = []autopilotPost{
			post("ment", apOther, "general", 1, func(p *autopilotPost) {
				p.Title, p.Content = "Solana question", "@Atlas Prime can you look at my anchor program?"
			}),
			post("feed", apOther, "request", 1, func(p *autopilotPost) { p.Title, p.Content = "Solana question", "Anchor program audit wanted." }),
		}
	})
	s5 := newAutopilotTestServer(t, trk5.URL, policyWith(config.AutopilotSuggest, scoringOn))
	withLibrary(t, s5)
	if res := s5.runAutopilotOnce(context.Background()); res.Suggested != 2 || res.RelevanceSkipped != 1 || res.Routed != 1 {
		t.Fatalf("bypass run = %+v", res)
	}
	byPost := map[string]autopilotSuggestion{}
	for _, sg := range s5.autopilotSt().store.pendingSuggestions() {
		byPost[sg.PostID] = sg
	}
	if sg, ok := byPost["py"]; !ok || *sg.Relevance != 0.176 || sg.RelevanceSignals.Routed != 1 {
		t.Errorf("routed with library = %+v", sg)
	}
	if sg, ok := byPost["ment"]; !ok || *sg.Relevance != 0 {
		t.Errorf("mention = %+v", sg)
	}
	if _, ok := byPost["feed"]; ok {
		t.Error("plain feed post under the threshold must be skipped")
	}
	if c := s5.autopilotDTOFor().Status.Relevance; c != (autopilotRelevanceCounters{Scored: 3, Skipped: 1, Drafted: 2}) {
		t.Errorf("bypass counters = %+v", c)
	}

	// Instruction and routed signals, no library: (0.15 + 0.15) / 0.55.
	trk3, fb3 := newFakeBoard(t)
	fb3.set(func(f *fakeBoard) {
		p := post("py", apOther, "request", 1, func(p *autopilotPost) {
			p.Title, p.Content = "Python data pipeline", "Anyone able to build an Airflow pipeline with Pandas?"
		})
		f.byID[p.ID] = p
		f.routedIDs = []string{"py"}
	})
	s3 := newAutopilotTestServer(t, trk3.URL, policyWith(config.AutopilotSuggest, scoringOn, func(p *config.AutopilotConfig) { p.Instruction = "Offer Airflow and Go reviews." }))
	if res := s3.runAutopilotOnce(context.Background()); res.Suggested != 1 || res.Routed != 1 {
		t.Fatalf("routed run = %+v", res)
	}
	if got := s3.autopilotSt().store.pendingSuggestions(); len(got) != 1 || *got[0].Relevance != 0.545 || got[0].RelevanceSignals.Instruction != 1 || got[0].RelevanceSignals.Routed != 1 {
		t.Errorf("routed suggestion = %+v", got)
	}
}

func TestAutopilotRun_RelevanceRichBountyOverrideAndPins(t *testing.T) {
	trk, fb := newFakeBoard(t)
	fb.set(func(f *fakeBoard) {
		f.posts = []autopilotPost{
			post("rich", apOther, "request", 1, withBounty(60, "open"), func(p *autopilotPost) { p.Title, p.Content = "Kubernetes helm chart", "Need a helm chart." }),
			post("meh", apOther, "request", 1, withBounty(59, "open"), func(p *autopilotPost) { p.Title, p.Content = "Kubernetes helm chart", "Need a helm chart." }),
		}
	})
	// Bounty mode, 2 x 3 x 10 = 60: the rich bounty is answered under the threshold as long
	// as some signal fires (instruction shares "helm": 0.15 / 0.55 = 0.273, threshold 0.5).
	s := newAutopilotTestServer(t, trk.URL, policyWith(config.AutopilotBounty, scoringOn, func(p *config.AutopilotConfig) {
		p.Instruction = "Helm charts welcome."
		p.RelevanceThreshold = 0.5
	}))
	res := s.runAutopilotOnce(context.Background())
	if res.Posted != 1 || res.RelevanceSkipped != 1 || fb.replyPostIDs[0] != "rich" {
		t.Fatalf("run = %+v replies=%v", res, fb.replyPostIDs)
	}
	out := s.autopilotSt().store.outcomes()
	if len(out) != 1 || out[0].Relevance == nil || *out[0].Relevance != 0.273 {
		t.Errorf("posted reply must carry the score: %+v", out)
	}
	if rows := s.autopilotSt().ledger.rows(); len(rows) != 1 || rows[0].ReplyID != "r-1" || rows[0].PostedAt == nil || rows[0].BountyAmount != 60 {
		t.Errorf("ledger = %+v", rows)
	}

	// Suggest mode with the request threshold pinned at 0.1: a zero score is still under it,
	// but pinning general at 0.1 does not help a request post.
	trk2, fb2 := newFakeBoard(t)
	fb2.set(func(f *fakeBoard) {
		f.posts = []autopilotPost{post("p", apOther, "request", 1, func(p *autopilotPost) { p.Title, p.Content = "Kubernetes helm chart", "Need a helm chart." })}
	})
	s2 := newAutopilotTestServer(t, trk2.URL, policyWith(config.AutopilotSuggest, scoringOn, func(p *config.AutopilotConfig) {
		p.Instruction = "Helm charts welcome."
		p.RelevanceThresholdByCategory = map[string]float64{"request": 0.5}
	}))
	if res := s2.runAutopilotOnce(context.Background()); res.RelevanceSkipped != 1 {
		t.Fatalf("pinned high = %+v", res)
	}
	s2.setAutopilotPolicy(policyWith(config.AutopilotSuggest, scoringOn, func(p *config.AutopilotConfig) {
		p.Instruction = "Helm charts welcome."
		p.RelevanceThresholdByCategory = map[string]float64{"request": 0.1}
	}))
	s2.autopilotSt().now = func() time.Time { return apNow.Add(25 * time.Hour) } // past the cooldown the skip started
	s2.autopilotSt().store.now = s2.autopilotSt().now
	fb2.set(func(f *fakeBoard) { f.posts[0].Time = apNow.Add(24 * time.Hour).Format(time.RFC3339) })
	if res := s2.runAutopilotOnce(context.Background()); res.Suggested != 1 {
		t.Fatalf("pinned low = %+v", res)
	}
}

func TestAutopilotRun_OutcomesRefreshAndTuning(t *testing.T) {
	trk, fb := newFakeBoard(t)
	fb.set(func(f *fakeBoard) {
		f.posts = []autopilotPost{post("a", apOther, "request", 1), post("b", apOther, "request", 1)}
	})
	s := newAutopilotTestServer(t, trk.URL, policyWith(config.AutopilotAuto, scoringOn, func(p *config.AutopilotConfig) {
		p.Instruction = "Go service reviewer." // shares "service": 0.15 / 0.55 = 0.273; request pinned low so tuning leaves it alone
		p.RelevanceThresholdByCategory = map[string]float64{"request": 0.1}
	}))
	if res := s.runAutopilotOnce(context.Background()); res.Posted != 2 {
		t.Fatalf("run = %+v", res)
	}
	if len(fb.outcomesSince) != 0 {
		t.Error("nothing posted before the run: outcomes must not be asked for")
	}
	st := s.autopilotSt()
	// Tracker reports: r-1 accepted and awarded (snake_case), r-2 only the post's upvotes
	// (camelCase MVP shape): upvotes are kept on the row but settle nothing and make no hit.
	fb.set(func(f *fakeBoard) {
		f.outcomes = []map[string]interface{}{
			{"reply_id": "r-1", "post_id": "a", "category": "request", "bounty_amount": 50, "posted_at": apNow.Format(time.RFC3339), "upvotes": 0, "accepted": true, "awarded": true, "awarded_amount": 50, "hidden": false, "reported": false},
			{"replyId": "r-2", "postId": "b", "postedAt": apNow.Format(time.RFC3339), "upvotes": 3, "awarded": false},
		}
	})
	st.now = func() time.Time { return apNow.Add(time.Hour) }
	st.store.now, st.ledger.now = st.now, st.now
	res := s.runAutopilotOnce(context.Background())
	if res.OutcomesRefreshed != 2 {
		t.Fatalf("refresh = %+v", res)
	}
	if got := fb.outcomesSince; len(got) != 1 || got[0] != apNow.Add(-time.Minute).Format(time.RFC3339) {
		t.Errorf("since = %v", got)
	}
	rows := st.ledger.rows()
	if len(rows) != 2 || rows[0].Outcome != "awarded" || rows[0].CreditsWon != 50 || rows[0].OutcomeAt == nil || rows[1].Outcome != "pending" || rows[1].Upvotes != 3 || rows[1].OutcomeAt != nil {
		t.Errorf("rows = %+v", rows)
	}
	dto := s.autopilotDTOFor()
	if l := dto.Status.Ledger.Last30; l.Drafted != 2 || l.Posted != 2 || l.Hits != 1 || l.HitRate != 0.5 || l.CreditsSpent != 20 || l.CreditsWon != 50 {
		t.Errorf("last30 = %+v", l)
	}
	if d := dto.Status.Ledger.Days[29]; d.Drafted != 2 || d.Hits != 1 {
		t.Errorf("today = %+v", d)
	}
	// History signal: an awarded reply on a similar post lifts the next one.
	res3 := s.autopilotScore(s.autopilotPolicy(), post("c", apOther, "request", 1), false, st.now())
	if res3.signals.History != 1 || res3.score != 0.727 { // (0.25 + 0.15) / 0.55, no library
		t.Errorf("history score = %+v", res3)
	}
	if h := s.autopilotScore(s.autopilotPolicy(), post("d", apOther, "request", 1, func(p *autopilotPost) { p.Title, p.Content = "Solana anchor", "Anchor program audit" }), false, st.now()); h.signals.History != 0 {
		t.Errorf("unrelated history = %+v", h)
	}

	// Tuning: first run tuned nothing (no samples); 24 h later, with 5 settled (ignored)
	// replies in "general" and no hits, general goes up; 5 still-pending replies in
	// "discovery" are not samples; request is pinned and untouched.
	if _, at := st.store.tuned(); at.IsZero() {
		t.Error("tuning must run on the first tick")
	}
	for i := 0; i < 5; i++ {
		id := "g" + string(rune('0'+i))
		if err := st.ledger.add(autopilotLedgerRow{ID: id, PostID: id, Category: "general", DraftedAt: apNow, CreditsSpent: 10, Outcome: "ignored"}); err != nil {
			t.Fatal(err)
		}
		if err := st.ledger.markPosted(id, "rg-"+id, apNow); err != nil {
			t.Fatal(err)
		}
		id = "d" + string(rune('0'+i))
		if err := st.ledger.add(autopilotLedgerRow{ID: id, PostID: id, Category: "discovery", DraftedAt: apNow, CreditsSpent: 10}); err != nil {
			t.Fatal(err)
		}
		if err := st.ledger.markPosted(id, "rd-"+id, apNow); err != nil {
			t.Fatal(err)
		}
	}
	if res := s.runAutopilotOnce(context.Background()); len(s.autopilotDTOFor().Status.TunedThresholds) != 0 {
		t.Errorf("tuned inside 24 h: %+v %+v", res, s.autopilotDTOFor().Status.TunedThresholds)
	}
	st.now = func() time.Time { return apNow.Add(25 * time.Hour) }
	st.store.now, st.ledger.now = st.now, st.now
	fb.set(func(f *fakeBoard) { f.posts = nil })
	s.runAutopilotOnce(context.Background())
	tuned := s.autopilotDTOFor().Status
	if g := tuned.TunedThresholds["general"]; g.Threshold != 0.45 || g.Direction != "up" || g.Posted != 5 || g.HitRate != 0 {
		t.Errorf("tuned general = %+v (all %+v)", g, tuned.TunedThresholds)
	}
	if _, ok := tuned.TunedThresholds["request"]; ok {
		t.Error("pinned category tuned")
	}
	if _, ok := tuned.TunedThresholds["discovery"]; ok {
		t.Error("pending rows must not be tuning samples")
	}
	if tuned.TunedAt == "" {
		t.Error("tunedAt missing")
	}
	if raw, _ := json.Marshal(tuned.TunedThresholds["general"]); string(raw) != `{"threshold":0.45,"hitRate":0,"samples":5,"direction":"up"}` {
		t.Errorf("tuned wire shape = %s", raw)
	}
	// A policy threshold change recomputes on the next tick.
	s.setAutopilotPolicy(policyWith(config.AutopilotAuto, scoringOn, func(p *config.AutopilotConfig) { p.RelevanceThreshold = 0.5 }))
	s.runAutopilotOnce(context.Background())
	if g := s.autopilotDTOFor().Status.TunedThresholds["general"]; g.Threshold != 0.7 {
		t.Errorf("retuned general = %+v", g)
	}
	// 8 days on with no signal, the pending rows (the five discovery ones and r-2, whose
	// post upvotes settle nothing) become ignored; the outcomes endpoint knows nothing of them.
	st.now = func() time.Time { return apNow.Add(9 * 24 * time.Hour) }
	st.store.now, st.ledger.now = st.now, st.now
	if res := s.runAutopilotOnce(context.Background()); res.OutcomesRefreshed != 6 {
		t.Errorf("ignored refresh = %+v", res)
	}
	for _, r := range st.ledger.rows() {
		if (r.Category == "discovery" || r.ID == rows[1].ID) && r.Outcome != "ignored" {
			t.Errorf("row %s = %s", r.ID, r.Outcome)
		}
	}
	// Outcomes route failing is logged, never fatal.
	fb.set(func(f *fakeBoard) { f.outcomesStatus = http.StatusInternalServerError })
	if res := s.runAutopilotOnce(context.Background()); res.Skipped != "" || res.OutcomesRefreshed != 0 {
		t.Errorf("outcomes 500 = %+v", res)
	}
}

func TestAutopilotLedger_ReloadAndCorrupt(t *testing.T) {
	dir := t.TempDir()
	now := apNow
	l := newAutopilotLedger(dir, func() time.Time { return now })
	if err := l.load(); err != nil {
		t.Fatal(err)
	}
	old := autopilotLedgerRow{ID: "old", PostID: "p0", Category: "request", DraftedAt: now.Add(-91 * 24 * time.Hour), Outcome: "accepted", Keywords: []string{"grpc", "postgres"}}
	fresh := autopilotLedgerRow{ID: "new", PostID: "p1", Category: "request", DraftedAt: now.Add(-10 * 24 * time.Hour), Outcome: "accepted", Keywords: []string{"grpc", "postgres"}}
	if err := l.add(old); err != nil {
		t.Fatal(err)
	}
	if err := l.add(fresh); err != nil {
		t.Fatal(err)
	}
	l2 := newAutopilotLedger(dir, func() time.Time { return now })
	if err := l2.load(); err != nil {
		t.Fatal(err)
	}
	if rows := l2.rows(); len(rows) != 1 || rows[0].ID != "new" {
		t.Errorf("rows older than 90 days must be pruned on save: %+v", rows)
	}
	if !l2.historyMatch([]string{"grpc", "postgres", "other"}, now) || l2.historyMatch([]string{"grpc"}, now) || l2.historyMatch([]string{"grpc", "solana"}, now) {
		t.Error("history match")
	}
	raw, _ := os.ReadFile(filepath.Join(dir, autopilotLedgerFilename))
	for _, key := range []string{`"post_id"`, `"drafted_at"`, `"credits_spent"`, `"outcome"`, `"bounty_amount"`} {
		if !strings.Contains(string(raw), key) {
			t.Errorf("ledger file lacks %s: %s", key, raw)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, autopilotLedgerFilename), []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	l3 := newAutopilotLedger(dir, func() time.Time { return now })
	if err := l3.load(); err == nil || len(l3.rows()) != 0 {
		t.Error("corrupt ledger must be reported and start empty")
	}
}

func TestAutopilotRun_BountyAskThenRaiseThenAnswer(t *testing.T) {
	trk, fb := newFakeBoard(t)
	req := post("req", apOther, "request", 1) // no bounty
	fb.set(func(f *fakeBoard) { f.posts = []autopilotPost{req} })
	s := newAutopilotTestServer(t, trk.URL, policyWith(config.AutopilotBounty, scoringOn, func(p *config.AutopilotConfig) {
		p.Instruction = "Go reviewer for hire." // shares "reviewer": 0.15
		p.RelevanceThresholdByCategory = map[string]float64{"request": 0.1}
	}))
	withLibrary(t, s)

	// Tick 1: an ask goes out (one Fast draft, ask 30, auto, at most 280 chars).
	res := s.runAutopilotOnce(context.Background())
	if res.Asked != 1 || res.Posted != 0 || res.Suggested != 0 || res.Credits != 10 {
		t.Fatalf("ask run = %+v", res)
	}
	body := fb.replyBodies[0]
	if body["ask"] != float64(30) || body["auto"] != true || len([]rune(body["body"].(string))) > autopilotAskMaxChars || fb.replyHeaders[0].Get(AutopilotAutoHeader) != "1" {
		t.Errorf("ask body = %v", body)
	}
	// The score the draft was gated on rides along: library 1 + instruction 1 = 0.6 / 1.0.
	if sig, _ := body["relevance_signals"].(map[string]interface{}); body["relevance"] != float64(0.6) || sig == nil || sig["library"] != float64(1) || sig["instruction"] != float64(1) || sig["routed"] != float64(0) {
		t.Errorf("ask relevance = %v %v", body["relevance"], body["relevance_signals"])
	}
	msgs := fb.drafts[0]["messages"].([]interface{})
	sys := msgs[0].(map[string]interface{})["content"].(string)
	if !strings.Contains(sys, "30 credits") || !strings.Contains(sys, "280 characters") || !strings.Contains(sys, "Go reviewer for hire.") {
		t.Errorf("ask prompt = %q", sys)
	}
	if fb.draftHeaders[0].Get(AutopilotAutoHeader) != "1" {
		t.Errorf("ask draft must be marked auto: %v", fb.draftHeaders[0])
	}
	st := s.autopilotSt()
	if a, ok := st.store.ask("req"); !ok || a.Ask != 30 || a.ReplyID != "r-1" || a.AnsweredAt != nil {
		t.Errorf("ask record = %+v", a)
	}
	if rows := st.ledger.rows(); len(rows) != 1 || rows[0].Kind != "ask" || rows[0].Ask != 30 || rows[0].ReplyID != "r-1" {
		t.Errorf("ledger = %+v", rows)
	}
	if l := s.autopilotDTOFor().Status.Ledger.Last30; l.Drafted != 1 || l.Posted != 0 || l.CreditsSpent != 10 {
		t.Errorf("asks count as drafts, not posted samples: %+v", l)
	}

	// Tick 2: nothing (ask pending), even though the tracker lists our ask reply.
	if res := s.runAutopilotOnce(context.Background()); res.Credits != 0 || res.Asked != 0 || res.Posted != 0 {
		t.Errorf("pending run = %+v", res)
	}

	// The author raises to 20: under the ask, the activity is consumed, nothing drafted.
	fb.set(func(f *fakeBoard) {
		p := req
		p.Bounty = &autopilotBounty{Amount: 20, Currency: "credits", Status: "open", DaysRemaining: 7}
		f.byID["req"] = p
		f.raisedIDs = []string{"req"}
	})
	res = s.runAutopilotOnce(context.Background())
	if res.Raised != 1 || res.Credits != 0 || res.Posted != 0 || !strings.Contains(strings.Join(fb.readIDs, ","), "raise-req") {
		t.Errorf("under-ask raise = %+v read=%v", res, fb.readIDs)
	}

	// A second raise to 30 meets the ask: the full answer is posted on the next tick
	// (a normal auto reply: counts against the daily cap, recorded, ask marked answered).
	fb.set(func(f *fakeBoard) {
		p := req
		p.Bounty = &autopilotBounty{Amount: 30, Currency: "credits", Status: "open", DaysRemaining: 7}
		f.byID["req"] = p
		f.readIDs = nil // a new bounty_raised row
	})
	res = s.runAutopilotOnce(context.Background())
	if res.Raised != 1 || res.Posted != 1 || res.Credits != 10 || res.RoutedRead != 1 {
		t.Fatalf("raised run = %+v", res)
	}
	if fb.replyCount() != 2 || fb.replyPostIDs[1] != "req" || fb.replyBodies[1]["ask"] != nil || fb.replyBodies[1]["auto"] != true {
		t.Errorf("answer = %v", fb.replyBodies)
	}
	if a, _ := st.store.ask("req"); a.AnsweredAt == nil {
		t.Error("ask not marked answered")
	}
	if !st.store.replied("req") {
		t.Error("answer not recorded")
	}
	if out := st.store.outcomes(); len(out) != 1 || out[0].Trigger != "bounty" || out[0].Bounty != 30 {
		t.Errorf("outcomes = %+v", out)
	}
	if replies, _ := st.store.counters(); replies != 2 {
		t.Errorf("ask and answer both count: %d", replies)
	}
	rows := st.ledger.rows()
	if len(rows) != 2 || rows[1].Kind != "reply" || rows[1].ReplyID != "r-2" {
		t.Errorf("ledger = %+v", rows)
	}
	// Tick after: replied, nothing more.
	fb.set(func(f *fakeBoard) { f.readIDs = nil })
	if res := s.runAutopilotOnce(context.Background()); res.Credits != 0 || res.Posted != 0 {
		t.Errorf("after answer = %+v", res)
	}

	// A raise on a post we never asked on is consumed without a draft.
	fb.set(func(f *fakeBoard) { f.raisedIDs = []string{"other"}; f.readIDs = nil })
	if res := s.runAutopilotOnce(context.Background()); res.Raised != 0 || res.Credits != 0 || !strings.Contains(strings.Join(fb.readIDs, ","), "raise-other") {
		t.Errorf("raise without ask = %+v read=%v", res, fb.readIDs)
	}
}

func TestAutopilotRun_AskNeedsRelevanceAndTrackerRefusal(t *testing.T) {
	trk, fb := newFakeBoard(t)
	fb.set(func(f *fakeBoard) { f.posts = []autopilotPost{post("req", apOther, "request", 1)} })
	// Scoring on, empty library, no instruction: 0 < 0.25, so no ask (skip mode).
	s := newAutopilotTestServer(t, trk.URL, policyWith(config.AutopilotBounty, scoringOn))
	if res := s.runAutopilotOnce(context.Background()); res.Asked != 0 || res.RelevanceSkipped != 1 {
		t.Errorf("low relevance ask = %+v", res)
	}
	// Note mode still requires the threshold for an ask (never for a full draft).
	s = newAutopilotTestServer(t, trk.URL, policyWith(config.AutopilotBounty, func(p *config.AutopilotConfig) { p.RelevanceMode = config.AutopilotRelevanceNote }))
	if res := s.runAutopilotOnce(context.Background()); res.Asked != 0 || res.RelevanceSkipped != 1 {
		t.Errorf("note mode ask = %+v", res)
	}
	// Scoring off: the ask goes out. The tracker refusing it (older tracker, 400)
	// costs the draft, parks nothing and starts the cooldown.
	fb.set(func(f *fakeBoard) { f.replyStatus = http.StatusBadRequest })
	s = newAutopilotTestServer(t, trk.URL, policyWith(config.AutopilotBounty))
	res := s.runAutopilotOnce(context.Background())
	if res.Asked != 0 || res.Suggested != 0 || res.Credits != 10 || fb.replyCount() != 1 {
		t.Errorf("refused ask = %+v", res)
	}
	if _, ok := s.autopilotSt().store.ask("req"); ok {
		t.Error("refused ask recorded")
	}
	if _, seen := s.autopilotSt().store.lastSeen("req"); !seen {
		t.Error("refused ask must start the cooldown")
	}
	if res := s.runAutopilotOnce(context.Background()); res.Credits != 0 {
		t.Errorf("cooldown ignored: %+v", res)
	}
}

func TestSetupAutopilot_Phase3FieldsRoundTrip(t *testing.T) {
	trk, _ := newFakeBoard(t)
	s := newAutopilotTestServer(t, trk.URL, policyWith(config.AutopilotOff))
	w := do(s, setupRequest(http.MethodGet, "/api/v1/setup/autopilot", ""))
	for _, key := range []string{`"relevanceThreshold":0.25`, `"relevanceMode":"off"`, `"relevance":{"scored":0,"skipped":0,"drafted":0}`, `"ledger":{"last30":{"drafted":0,"posted":0,"hits":0,"hitRate":0,"creditsSpent":0,"creditsWon":0},"days":[{"date":"2026-08-17","drafts":0,"hits":0},`, `"tunedThresholds":{}`} {
		if !strings.Contains(w.Body.String(), key) {
			t.Errorf("missing %s in %s", key, w.Body.String())
		}
	}
	if strings.Contains(w.Body.String(), "relevanceThresholdByCategory") || strings.Contains(w.Body.String(), "tunedAt") {
		t.Errorf("empty pins or tunedAt must be absent: %s", w.Body.String())
	}

	body := `{"relevanceThreshold":0.4,"relevanceMode":"note","relevanceThresholdByCategory":{"request":0.6,"General":0.2}}`
	got := autopilotData(t, do(s, setupRequest(http.MethodPost, "/api/v1/setup/autopilot", body)))
	if got.Policy.RelevanceThreshold != 0.4 || got.Policy.RelevanceMode != "note" || got.Policy.RelevanceThresholdByCategory["request"] != 0.6 || got.Policy.RelevanceThresholdByCategory["general"] != 0.2 {
		t.Errorf("post = %+v", got.Policy)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Autopilot.RelevanceThreshold != 0.4 || cfg.Autopilot.RelevanceMode != "note" || cfg.Autopilot.RelevanceThresholdByCategory["general"] != 0.2 {
		t.Errorf("config.yaml = %+v", cfg.Autopilot)
	}
	// Partial: absent keeps the pins; null clears them.
	got = autopilotData(t, do(s, setupRequest(http.MethodPost, "/api/v1/setup/autopilot", `{"relevanceThreshold":0.3}`)))
	if got.Policy.RelevanceThreshold != 0.3 || len(got.Policy.RelevanceThresholdByCategory) != 2 {
		t.Errorf("partial = %+v", got.Policy)
	}
	got = autopilotData(t, do(s, setupRequest(http.MethodPost, "/api/v1/setup/autopilot", `{"relevanceThresholdByCategory":null}`)))
	if len(got.Policy.RelevanceThresholdByCategory) != 0 {
		t.Errorf("clear = %+v", got.Policy)
	}
	for name, bad := range map[string]string{
		"threshold high": `{"relevanceThreshold":1.2}`,
		"threshold low":  `{"relevanceThreshold":0.01}`,
		"mode":           `{"relevanceMode":"sometimes"}`,
		"pin key":        `{"relevanceThresholdByCategory":{"weird":0.3}}`,
		"pin value":      `{"relevanceThresholdByCategory":{"request":3}}`,
		"pin shape":      `{"relevanceThresholdByCategory":[0.3]}`,
	} {
		if w := do(s, setupRequest(http.MethodPost, "/api/v1/setup/autopilot", bad)); w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d body=%s", name, w.Code, w.Body.String())
		}
	}
	// Suggestion DTO keys.
	var sg autopilotSuggestion
	score := 0.45
	sg.Relevance, sg.RelevanceSignals = &score, &autopilotRelevanceSignals{Library: 1}
	raw, _ := json.Marshal(sg)
	if !strings.Contains(string(raw), `"relevance":0.45`) || !strings.Contains(string(raw), `"relevanceSignals":{"library":1,"history":0,"instruction":0,"routed":0}`) {
		t.Errorf("suggestion json = %s", raw)
	}
}

func TestAutopilotRun_RichBountyNeedsASignal(t *testing.T) {
	trk, fb := newFakeBoard(t)
	fb.set(func(f *fakeBoard) {
		f.posts = []autopilotPost{
			post("spam", apOther, "request", 1, withBounty(1000, "open"), func(p *autopilotPost) { p.Title, p.Content = "Post", "Post post post." }),
			post("helm", apOther, "request", 1, withBounty(1000, "open"), func(p *autopilotPost) { p.Title, p.Content = "Kubernetes helm chart", "Need a helm chart." }),
		}
	})
	// Bounty mode, instruction shares "helm" with the second post only: 0.15 / 0.55 = 0.273
	// (under a 0.5 threshold, but rich); the first scores 0 and is skipped whatever the bounty.
	s := newAutopilotTestServer(t, trk.URL, policyWith(config.AutopilotBounty, scoringOn, func(p *config.AutopilotConfig) {
		p.Instruction = "Helm charts welcome."
		p.RelevanceThreshold = 0.5
	}))
	res := s.runAutopilotOnce(context.Background())
	if res.Posted != 1 || res.RelevanceSkipped != 1 || fb.replyCount() != 1 || fb.replyPostIDs[0] != "helm" {
		t.Fatalf("run = %+v replies=%v", res, fb.replyPostIDs)
	}
	if got := fb.replyBodies[0]["relevance"]; got != float64(0.273) {
		t.Errorf("rich reply relevance = %v", got)
	}
	if _, seen := s.autopilotSt().store.lastSeen("spam"); !seen {
		t.Error("spam bounty must be skipped with the cooldown like any relevance_low post")
	}
}

func TestAutopilotRun_TrackerReplyCapIsNotABackoff(t *testing.T) {
	trk, fb := newFakeBoard(t)
	fb.set(func(f *fakeBoard) {
		f.posts = []autopilotPost{post("a", apOther, "request", 1), post("b", apOther, "request", 1)}
		f.replyStatus, f.replyCode = http.StatusTooManyRequests, "AUTO_REPLY_LIMIT"
	})
	s := newAutopilotTestServer(t, trk.URL, policyWith(config.AutopilotAuto))
	st := s.autopilotSt()
	// A posted row from earlier so the outcome refresh has something to ask for.
	if err := st.ledger.add(autopilotLedgerRow{ID: "old", PostID: "old", Category: "request", DraftedAt: apNow.Add(-time.Hour), CreditsSpent: 10}); err != nil {
		t.Fatal(err)
	}
	if err := st.ledger.markPosted("old", "r-old", apNow.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	fb.set(func(f *fakeBoard) {
		f.outcomes = []map[string]interface{}{{"reply_id": "r-old", "post_id": "old", "accepted": true, "posted_at": apNow.Add(-time.Hour).Format(time.RFC3339)}}
	})

	res := s.runAutopilotOnce(context.Background())
	if res.BackedOff || res.StoppedFor != "tracker_reply_cap" || res.Posted != 0 || res.Suggested != 1 || fb.replyCount() != 1 || res.OutcomesRefreshed != 1 {
		t.Fatalf("run = %+v replies=%d", res, fb.replyCount())
	}
	want := "tracker daily reply cap reached, resumes at 2026-09-16T00:00:00Z"
	if dto := s.autopilotDTOFor(); dto.Status.DowngradedReason != want {
		t.Errorf("downgradedReason = %q", dto.Status.DowngradedReason)
	}
	if got := st.store.pendingSuggestions(); len(got) != 1 || got[0].PostID != "a" {
		t.Errorf("draft not parked: %+v", got)
	}
	// Until UTC midnight: auto runs stop at the gate, outcomes still refresh, no backoff.
	fb.set(func(f *fakeBoard) {
		f.outcomes = []map[string]interface{}{{"reply_id": "r-old", "post_id": "old", "accepted": true, "awarded": true, "awarded_amount": 40, "posted_at": apNow.Add(-time.Hour).Format(time.RFC3339)}}
	})
	if res := s.runAutopilotOnce(context.Background()); res.Skipped != "tracker_reply_cap" || res.OutcomesRefreshed != 1 || fb.replyCount() != 1 {
		t.Errorf("gated run = %+v", res)
	}
	if st.now().Before(st.backoffUntil) {
		t.Error("the tracker cap must not enter the backoff")
	}
	// Suggest mode keeps drafting suggestions meanwhile; the reason stays visible.
	s.setAutopilotPolicy(policyWith(config.AutopilotSuggest))
	if res := s.runAutopilotOnce(context.Background()); res.Skipped != "" || res.Suggested != 1 {
		t.Errorf("suggest run = %+v", res)
	}
	if dto := s.autopilotDTOFor(); dto.Status.DowngradedReason != want {
		t.Errorf("suggest downgradedReason = %q", dto.Status.DowngradedReason)
	}
	// Approving during the cap answers 429 without a backoff either.
	sg := st.store.pendingSuggestions()[0]
	if w := do(s, setupRequest(http.MethodPost, "/api/v1/setup/autopilot/suggestions/"+sg.ID+"/approve", "")); w.Code != http.StatusTooManyRequests {
		t.Errorf("approve during cap = %d %s", w.Code, w.Body.String())
	}
	if st.now().Before(st.backoffUntil) {
		t.Error("approve hitting the cap must not back off")
	}
	// Next UTC day: back to normal.
	s.setAutopilotPolicy(policyWith(config.AutopilotAuto))
	st.now = func() time.Time { return time.Date(2026, 9, 16, 0, 1, 0, 0, time.UTC) }
	st.store.now, st.ledger.now = st.now, st.now
	fb.set(func(f *fakeBoard) {
		f.replyStatus, f.replyCode = 0, ""
		f.posts = []autopilotPost{post("c", apOther, "request", 1, func(p *autopilotPost) { p.Time = st.now().Add(-time.Hour).Format(time.RFC3339) })}
	})
	if res := s.runAutopilotOnce(context.Background()); res.Skipped != "" || res.Posted != 1 {
		t.Errorf("after midnight = %+v", res)
	}
	if dto := s.autopilotDTOFor(); dto.Status.DowngradedReason != "" {
		t.Errorf("reason not cleared: %q", dto.Status.DowngradedReason)
	}
	// A plain 429 (rate limit) still backs off.
	fb.set(func(f *fakeBoard) {
		f.replyStatus = http.StatusTooManyRequests
		f.posts = []autopilotPost{post("d", apOther, "request", 1, func(p *autopilotPost) { p.Time = st.now().Add(-time.Hour).Format(time.RFC3339) })}
	})
	if res := s.runAutopilotOnce(context.Background()); !res.BackedOff {
		t.Errorf("plain 429 = %+v", res)
	}
}

func TestSetupAutopilot_ApproveClosesTheAsk(t *testing.T) {
	trk, fb := newFakeBoard(t)
	fb.set(func(f *fakeBoard) {
		f.posts = []autopilotPost{post("req", apOther, "request", 1, withBounty(40, "open"))}
	})
	s := newAutopilotTestServer(t, trk.URL, policyWith(config.AutopilotSuggest))
	st := s.autopilotSt()
	// An ask posted earlier (in bounty mode) on the post, before the owner switched to suggest.
	if err := st.store.recordAsk("req", autopilotAsk{ReplyID: "r-ask", Ask: 40, Category: "request", At: apNow.Add(-2 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	fb.set(func(f *fakeBoard) { f.raisedIDs = []string{"req"}; f.byID["req"] = f.posts[0] })
	if res := s.runAutopilotOnce(context.Background()); res.Raised != 1 || res.Suggested != 1 {
		t.Fatalf("raise run = %+v", res)
	}
	sg := st.store.pendingSuggestions()[0]
	if w := do(s, setupRequest(http.MethodPost, "/api/v1/setup/autopilot/suggestions/"+sg.ID+"/approve", "")); w.Code != http.StatusOK {
		t.Fatalf("approve = %d %s", w.Code, w.Body.String())
	}
	if a, ok := st.store.ask("req"); !ok || a.AnsweredAt == nil || !a.AnsweredAt.Equal(apNow) {
		t.Errorf("ask not closed by approve: %+v", a)
	}
	if !st.store.replied("req") {
		t.Error("answer not recorded")
	}
}

func TestAutopilotPhase3_NoDashesInUserFacingStrings(t *testing.T) {
	for _, f := range []string{"autopilot_relevance.go", "autopilot_ledger.go"} {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.ContainsAny(string(raw), string([]rune{0x2014, 0x2013})) {
			t.Errorf("%s contains an em or en dash", f)
		}
	}
}
