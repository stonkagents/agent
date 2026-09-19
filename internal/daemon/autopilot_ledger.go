// Package: internal/daemon
// Feature: Agent Autopilot (community board, phase 3)
// Purpose: The local outcome ledger, <data_dir>/autopilot_ledger.json: one
// row per draft the watcher paid for (suggested, posted, or a bounty ask),
// with its relevance score and, once posted, the outcome the tracker reports
// (GET /api/v1/tracker/autopilot/outcomes, refreshed every tick for rows
// younger than 30 days). The ledger feeds the history relevance signal (90
// days), the per-category self-tuning and the status card's 30-day strip.
// Same file discipline as the state store: atomic write, 0600.

package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/stonkagents/agent/internal/config"
)

// autopilotLedgerFilename is the ledger file name under DataDir.
const autopilotLedgerFilename = "autopilot_ledger.json"

const (
	// autopilotLedgerWindow is the outcome refresh and status window.
	autopilotLedgerWindow = 30 * 24 * time.Hour
	// autopilotLedgerKeep is how long rows stay in the file (the history
	// signal looks back 90 days).
	autopilotLedgerKeep = autopilotHistoryWindow
	// autopilotLedgerIgnoredAfter: a posted reply with no signal after this
	// long is recorded as ignored.
	autopilotLedgerIgnoredAfter = 7 * 24 * time.Hour
	// autopilotLedgerMaxRows bounds the file.
	autopilotLedgerMaxRows = 5000
)

// Ledger row kinds.
const (
	autopilotLedgerKindReply = "reply" // a full answer (suggested or posted)
	autopilotLedgerKindAsk   = "ask"   // a bounty ask (phase 3 negotiation)
)

// Ledger outcomes, in precedence order (the strongest known signal wins).
// upvoted is part of the contract's enum but never produced: the tracker's
// upvotes are the post's, not the reply's (they are kept on the row).
const (
	autopilotOutcomePending  = "pending"
	autopilotOutcomeIgnored  = "ignored"
	autopilotOutcomeUpvoted  = "upvoted"
	autopilotOutcomeAccepted = "accepted"
	autopilotOutcomeAwarded  = "awarded"
	autopilotOutcomeHidden   = "hidden"
)

// autopilotLedgerRow is one draft. Keys follow the phase 3 contract (snake
// case in the file).
type autopilotLedgerRow struct {
	ID           string                    `json:"id"`
	Kind         string                    `json:"kind,omitempty"` // reply (default) | ask
	PostID       string                    `json:"post_id"`
	ReplyID      string                    `json:"reply_id,omitempty"`
	Category     string                    `json:"category"`
	Relevance    float64                   `json:"relevance"`
	Signals      autopilotRelevanceSignals `json:"signals"`
	Keywords     []string                  `json:"keywords,omitempty"`
	BountyAmount int                       `json:"bounty_amount"`
	Ask          int                       `json:"ask,omitempty"`
	DraftedAt    time.Time                 `json:"drafted_at"`
	PostedAt     *time.Time                `json:"posted_at,omitempty"`
	CreditsSpent int                       `json:"credits_spent"`
	Outcome      string                    `json:"outcome"`
	OutcomeAt    *time.Time                `json:"outcome_at,omitempty"`
	CreditsWon   int                       `json:"credits_won"`
	Upvotes      int                       `json:"upvotes,omitempty"`
	Reported     bool                      `json:"reported,omitempty"`
}

// hit reports whether the row counts as a hit: accepted or awarded. Upvotes
// are the post's count, not the reply's (replies carry none yet), so they are
// kept on the row for display and never make a hit.
func (r autopilotLedgerRow) hit() bool {
	return r.Outcome == autopilotOutcomeAccepted || r.Outcome == autopilotOutcomeAwarded
}

// settled reports whether the outcome is known (anything but pending): only
// settled rows are tuning samples, so five answers posted yesterday do not
// read as five misses today.
func (r autopilotLedgerRow) settled() bool {
	return r.Outcome != "" && r.Outcome != autopilotOutcomePending
}

func (r autopilotLedgerRow) isAsk() bool { return r.Kind == autopilotLedgerKindAsk }

// autopilotLedgerDoc is the on-disk document.
type autopilotLedgerDoc struct {
	Rows []autopilotLedgerRow `json:"rows"`
}

// autopilotLedger is the in-memory copy of the ledger file.
type autopilotLedger struct {
	mu   sync.Mutex
	path string // empty = memory only
	now  func() time.Time
	doc  autopilotLedgerDoc
}

func newAutopilotLedger(dataDir string, now func() time.Time) *autopilotLedger {
	l := &autopilotLedger{now: now}
	if dataDir != "" {
		l.path = filepath.Join(dataDir, autopilotLedgerFilename)
	}
	return l
}

// load reads the ledger file; a missing or unreadable file starts empty.
func (l *autopilotLedger) load() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.path == "" {
		return nil
	}
	raw, err := os.ReadFile(l.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var doc autopilotLedgerDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("autopilot ledger unreadable, starting empty: %w", err)
	}
	l.doc = doc
	return nil
}

// saveLocked prunes rows older than autopilotLedgerKeep and writes the file;
// the caller holds l.mu.
func (l *autopilotLedger) saveLocked() error {
	cutoff := l.now().Add(-autopilotLedgerKeep)
	kept := l.doc.Rows[:0]
	for _, r := range l.doc.Rows {
		if r.DraftedAt.After(cutoff) {
			kept = append(kept, r)
		}
	}
	if n := len(kept); n > autopilotLedgerMaxRows {
		kept = append([]autopilotLedgerRow{}, kept[n-autopilotLedgerMaxRows:]...)
	}
	l.doc.Rows = kept
	if l.path == "" {
		return nil
	}
	out, err := json.MarshalIndent(l.doc, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(l.path), 0o700); err != nil {
		return err
	}
	tmp := l.path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, l.path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// add appends a row (outcome pending when empty).
func (l *autopilotLedger) add(row autopilotLedgerRow) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if row.Outcome == "" {
		row.Outcome = autopilotOutcomePending
	}
	if row.Kind == "" {
		row.Kind = autopilotLedgerKindReply
	}
	l.doc.Rows = append(l.doc.Rows, row)
	return l.saveLocked()
}

// markPosted records that the draft in row id went out as replyID at t.
func (l *autopilotLedger) markPosted(id, replyID string, t time.Time) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i := range l.doc.Rows {
		if l.doc.Rows[i].ID == id {
			at := t
			l.doc.Rows[i].ReplyID = replyID
			l.doc.Rows[i].PostedAt = &at
			return l.saveLocked()
		}
	}
	return nil
}

// rows returns a copy of every row, oldest first.
func (l *autopilotLedger) rows() []autopilotLedgerRow {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]autopilotLedgerRow, len(l.doc.Rows))
	copy(out, l.doc.Rows)
	return out
}

// oldestOpenPostedAt returns the posted_at of the oldest row posted within
// the window whose outcome may still change, ok false when there is none
// (nothing to refresh).
func (l *autopilotLedger) oldestOpenPostedAt(now time.Time) (time.Time, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := now.Add(-autopilotLedgerWindow)
	var oldest time.Time
	found := false
	for _, r := range l.doc.Rows {
		if r.ReplyID == "" || r.PostedAt == nil || !r.PostedAt.After(cutoff) {
			continue
		}
		if !found || r.PostedAt.Before(oldest) {
			oldest, found = *r.PostedAt, true
		}
	}
	return oldest, found
}

// autopilotTrackerOutcome is one item of GET /api/v1/tracker/autopilot/outcomes.
// Both the phase 3 snake_case keys and the MVP camelCase keys are accepted.
type autopilotTrackerOutcome struct {
	ReplyID       string `json:"reply_id"`
	ReplyIDCamel  string `json:"replyId"`
	PostID        string `json:"post_id"`
	PostIDCamel   string `json:"postId"`
	Category      string `json:"category"`
	BountyAmount  int    `json:"bounty_amount"`
	BountyCamel   int    `json:"bountyAmount"`
	PostedAt      string `json:"posted_at"`
	PostedAtCamel string `json:"postedAt"`
	Upvotes       int    `json:"upvotes"`
	Accepted      bool   `json:"accepted"`
	Awarded       bool   `json:"awarded"`
	AwardedAmount int    `json:"awarded_amount"`
	AwardedCamel  int    `json:"awardedAmount"`
	Hidden        bool   `json:"hidden"`
	Reported      bool   `json:"reported"`
}

func (o autopilotTrackerOutcome) replyID() string {
	if o.ReplyID != "" {
		return o.ReplyID
	}
	return o.ReplyIDCamel
}

func (o autopilotTrackerOutcome) awardedAmount() int {
	if o.AwardedAmount != 0 {
		return o.AwardedAmount
	}
	if o.AwardedCamel != 0 {
		return o.AwardedCamel
	}
	if o.Awarded {
		if o.BountyAmount != 0 {
			return o.BountyAmount
		}
		return o.BountyCamel
	}
	return 0
}

// outcome maps a tracker item to the ledger outcome: hidden beats awarded
// beats accepted; nothing yet is pending. The post's upvotes never settle a
// reply (see autopilotOutcomeUpvoted).
func (o autopilotTrackerOutcome) outcome() string {
	switch {
	case o.Hidden:
		return autopilotOutcomeHidden
	case o.Awarded:
		return autopilotOutcomeAwarded
	case o.Accepted:
		return autopilotOutcomeAccepted
	}
	return autopilotOutcomePending
}

// refresh applies the tracker's outcomes to the posted rows inside the
// window; a pending row older than autopilotLedgerIgnoredAfter becomes
// ignored. Returns how many rows changed.
func (l *autopilotLedger) refresh(items []autopilotTrackerOutcome, now time.Time) (int, error) {
	byReply := make(map[string]autopilotTrackerOutcome, len(items))
	for _, it := range items {
		if id := it.replyID(); id != "" {
			byReply[id] = it
		}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := now.Add(-autopilotLedgerWindow)
	changed := 0
	for i := range l.doc.Rows {
		r := &l.doc.Rows[i]
		if r.ReplyID == "" || r.PostedAt == nil || !r.PostedAt.After(cutoff) {
			continue
		}
		next, upvotes, won, reported := r.Outcome, r.Upvotes, r.CreditsWon, r.Reported
		if it, ok := byReply[r.ReplyID]; ok {
			if o := it.outcome(); o != autopilotOutcomePending {
				next = o
			} else if r.Outcome == autopilotOutcomePending || r.Outcome == autopilotOutcomeIgnored {
				next = autopilotOutcomePending
			}
			upvotes, reported = it.Upvotes, it.Reported
			if it.Awarded {
				won = it.awardedAmount()
			}
		}
		if next == autopilotOutcomePending && now.Sub(*r.PostedAt) > autopilotLedgerIgnoredAfter {
			next = autopilotOutcomeIgnored
		}
		if next == r.Outcome && upvotes == r.Upvotes && won == r.CreditsWon && reported == r.Reported {
			continue
		}
		if next != r.Outcome {
			at := now
			r.OutcomeAt = &at
		}
		r.Outcome, r.Upvotes, r.CreditsWon, r.Reported = next, upvotes, won, reported
		changed++
	}
	if changed == 0 {
		return 0, nil
	}
	return changed, l.saveLocked()
}

// historyMatch reports whether an accepted or awarded reply in the last 90
// days sits on a post sharing at least autopilotHistoryMinShared keywords.
func (l *autopilotLedger) historyMatch(keywords []string, now time.Time) bool {
	if len(keywords) < autopilotHistoryMinShared {
		return false
	}
	want := make(map[string]bool, len(keywords))
	for _, k := range keywords {
		want[k] = true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := now.Add(-autopilotHistoryWindow)
	for _, r := range l.doc.Rows {
		if r.isAsk() || !r.DraftedAt.After(cutoff) {
			continue
		}
		if r.Outcome != autopilotOutcomeAccepted && r.Outcome != autopilotOutcomeAwarded {
			continue
		}
		if autopilotSharedKeywords(r.Keywords, want) >= autopilotHistoryMinShared {
			return true
		}
	}
	return false
}

// autopilotLedgerLast30 is status.ledger.last30.
type autopilotLedgerLast30 struct {
	Drafted      int     `json:"drafted"`
	Posted       int     `json:"posted"`
	Hits         int     `json:"hits"`
	HitRate      float64 `json:"hitRate"`
	CreditsSpent int     `json:"creditsSpent"`
	CreditsWon   int     `json:"creditsWon"`
}

// autopilotLedgerDay is one bar of the status card's 30-day strip.
type autopilotLedgerDay struct {
	Date    string `json:"date"` // UTC day, 2006-01-02
	Drafted int    `json:"drafts"`
	Hits    int    `json:"hits"`
}

// autopilotLedgerDTO is status.ledger.
type autopilotLedgerDTO struct {
	Last30 autopilotLedgerLast30 `json:"last30"`
	Days   []autopilotLedgerDay  `json:"days"`
}

// summary builds status.ledger for the 30 days ending now: totals, and one
// entry per UTC day (oldest first, 30 entries, hits counted on the day the
// reply was drafted). Asks count as drafts and credits spent, never as
// posted samples or hits.
func (l *autopilotLedger) summary(now time.Time) autopilotLedgerDTO {
	l.mu.Lock()
	defer l.mu.Unlock()
	today := now.UTC().Truncate(24 * time.Hour)
	first := today.AddDate(0, 0, -29)
	days := make([]autopilotLedgerDay, 30)
	index := map[string]int{}
	for i := range days {
		d := first.AddDate(0, 0, i).Format("2006-01-02")
		days[i].Date = d
		index[d] = i
	}
	var out autopilotLedgerDTO
	for _, r := range l.doc.Rows {
		if r.DraftedAt.Before(first) {
			continue
		}
		out.Last30.Drafted++
		out.Last30.CreditsSpent += r.CreditsSpent
		i, inStrip := index[r.DraftedAt.UTC().Format("2006-01-02")]
		if inStrip {
			days[i].Drafted++
		}
		if r.isAsk() {
			continue
		}
		if r.ReplyID != "" {
			out.Last30.Posted++
		}
		out.Last30.CreditsWon += r.CreditsWon
		if r.hit() {
			out.Last30.Hits++
			if inStrip {
				days[i].Hits++
			}
		}
	}
	if out.Last30.Posted > 0 {
		out.Last30.HitRate = autopilotRound3(float64(out.Last30.Hits) / float64(out.Last30.Posted))
	}
	out.Days = days
	return out
}

// categoryStats returns settled posted samples and hits per category over
// the last 30 days (asks and still-pending rows excluded), the input of the
// self-tuning.
func (l *autopilotLedger) categoryStats(now time.Time) map[string]autopilotCategoryStats {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := now.Add(-autopilotLedgerWindow)
	out := map[string]autopilotCategoryStats{}
	for _, r := range l.doc.Rows {
		if r.isAsk() || r.ReplyID == "" || r.PostedAt == nil || !r.PostedAt.After(cutoff) || !r.settled() {
			continue
		}
		st := out[r.Category]
		st.Posted++
		if r.hit() {
			st.Hits++
		}
		out[r.Category] = st
	}
	return out
}

// sortedCategories is a stable order for logs and tests.
func sortedCategories(m map[string]autopilotTunedThreshold) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// --- server side: ledger rows, outcome refresh, tuning ---------------------

// newAutopilotLedgerRow is the ledger row of a draft just paid for.
func (s *Server) newAutopilotLedgerRow(post autopilotPost, rel *autopilotRelevanceResult, cost int, now time.Time) autopilotLedgerRow {
	row := autopilotLedgerRow{
		ID: newAutopilotID(), Kind: autopilotLedgerKindReply, PostID: post.ID, Category: post.Category,
		DraftedAt: now, CreditsSpent: cost, Outcome: autopilotOutcomePending,
	}
	if post.Bounty != nil {
		row.BountyAmount = post.Bounty.Amount
	}
	if rel != nil {
		row.Relevance, row.Signals, row.Keywords = rel.score, rel.signals, rel.keywords
	} else {
		row.Keywords = autopilotPostKeywords(post)
	}
	return row
}

// autopilotRefreshOutcomes pulls this peer's auto reply outcomes from the
// tracker for the ledger rows posted in the last 30 days and applies them.
// Returns how many rows changed; a tracker error is logged, never fatal.
func (s *Server) autopilotRefreshOutcomes(ctx context.Context, now time.Time) int {
	st := s.autopilotSt()
	since, ok := st.ledger.oldestOpenPostedAt(now)
	if !ok {
		return 0
	}
	st.mu.Lock()
	backoff := st.backoffUntil
	st.mu.Unlock()
	if now.Before(backoff) {
		return 0
	}
	apiKey := s.getTrackerAPIKey()
	if apiKey == "" || s.config == nil || strings.TrimSpace(s.config.TrackerURL) == "" {
		return 0
	}
	items, err := s.autopilotOutcomes(ctx, apiKey, since.Add(-time.Minute))
	if err != nil {
		s.autopilotWarn("outcomes", "outcomes unavailable: "+err.Error())
		return 0
	}
	changed, err := st.ledger.refresh(items, now)
	if err != nil {
		s.autopilotWarn("ledger", "Could not persist outcomes: "+err.Error())
	}
	if changed > 0 {
		s.autopilotLog("outcomes", map[string]interface{}{"decision": "refreshed", "changed": changed, "items": len(items)})
	}
	return changed
}

// autopilotTune returns the per-category tuned thresholds, recomputing them
// from the ledger once every autopilotTuneInterval (or right after the
// policy threshold changed).
func (s *Server) autopilotTune(policy config.AutopilotConfig, now time.Time) map[string]autopilotTunedThreshold {
	st := s.autopilotSt()
	tuned, at := st.store.tuned()
	if !at.IsZero() && now.Sub(at) < autopilotTuneInterval {
		return tuned
	}
	tuned = autopilotTuneThresholds(policy, st.ledger.categoryStats(now))
	if err := st.store.setTuned(tuned, now); err != nil {
		s.autopilotWarn("store", "Could not persist tuned thresholds: "+err.Error())
	}
	for _, cat := range sortedCategories(tuned) {
		t := tuned[cat]
		s.autopilotLog("tune", map[string]interface{}{"decision": "tuned", "category": cat, "threshold": t.Threshold, "hit_rate": t.HitRate, "posted": t.Posted, "direction": t.Direction})
	}
	return tuned
}
