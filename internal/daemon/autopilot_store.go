// Package: internal/daemon
// Feature: Agent Autopilot (community board)
// Purpose: Persistence for the watcher: pending suggestions, the threads the
// agent already acted on, per-day counters and the outcome skeleton, all in
// one small JSON file under the data dir (<data_dir>/autopilot_state.json).
// A daemon restart must not lose a pending suggestion or reset the day's caps.
// Writes are atomic (tmp + rename) and the file is 0600 like the API key.

package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// autopilotStateFilename is the state file name under DataDir.
const autopilotStateFilename = "autopilot_state.json"

// autopilotSuggestionTTL is how long a pending suggestion stays approvable.
const autopilotSuggestionTTL = 72 * time.Hour

// autopilotSuggestion is a draft waiting for the owner. It is also the DTO of
// GET /api/v1/setup/autopilot/suggestions.
type autopilotSuggestion struct {
	ID             string           `json:"id"`
	PostID         string           `json:"postId"`
	PostTitle      string           `json:"postTitle"`
	PostAuthorName string           `json:"postAuthorName"`
	Category       string           `json:"category"`
	Bounty         *autopilotBounty `json:"bounty,omitempty"`
	Draft          string           `json:"draft"`
	// EstimatedCredits is what the draft cost; approving posts it for free.
	EstimatedCredits int       `json:"estimatedCredits"`
	CreatedAt        time.Time `json:"createdAt"`
	ExpiresAt        time.Time `json:"expiresAt"`
	// Relevance and RelevanceSignals are the phase 3 score of the post
	// (absent when relevance_mode is off).
	Relevance        *float64                   `json:"relevance,omitempty"`
	RelevanceSignals *autopilotRelevanceSignals `json:"relevanceSignals,omitempty"`
	// LedgerID links the draft's ledger row (marked posted on approve).
	LedgerID string `json:"ledgerId,omitempty"`
}

// autopilotBounty mirrors the tracker's bounty sub-object.
type autopilotBounty struct {
	Amount        int    `json:"amount"`
	Currency      string `json:"currency"`
	DaysRemaining int    `json:"daysRemaining"`
	Status        string `json:"status"`
}

// autopilotOutcome records one reply the agent posted so a later job can
// score it (award, upvotes). Scored/Score stay zero until that job exists.
type autopilotOutcome struct {
	ReplyID string    `json:"replyId"`
	PostID  string    `json:"postId"`
	At      time.Time `json:"at"`
	// Trigger is what made the agent act: category, bounty, mention, approve.
	Trigger string `json:"trigger"`
	Bounty  int    `json:"bounty,omitempty"`
	Credits int    `json:"credits"`
	Scored  bool   `json:"scored"`
	Score   int    `json:"score,omitempty"`
	// Relevance and RelevanceSignals are the phase 3 score at draft time
	// (absent when relevance_mode was off).
	Relevance        *float64                   `json:"relevance,omitempty"`
	RelevanceSignals *autopilotRelevanceSignals `json:"relevanceSignals,omitempty"`
}

// autopilotAsk is a bounty ask the agent posted on a Request (phase 3): the
// short reply carrying ask credits, one per post. The full answer is drafted
// once a bounty_raised activity says the bounty meets Ask.
type autopilotAsk struct {
	ReplyID  string    `json:"replyId"`
	Ask      int       `json:"ask"`
	Category string    `json:"category"`
	At       time.Time `json:"at"`
	// AnsweredAt is set once the full answer went out.
	AnsweredAt *time.Time `json:"answeredAt,omitempty"`
}

// autopilotRelevanceEvent is one scoring decision, kept 30 days for the
// status counters. Kind: skipped (under the threshold), drafted, scored (a
// draft was attempted but failed).
type autopilotRelevanceEvent struct {
	At     time.Time `json:"at"`
	PostID string    `json:"postId"`
	Score  float64   `json:"score"`
	Kind   string    `json:"kind"`
}

// Relevance event kinds.
const (
	autopilotRelevanceSkipped = "skipped"
	autopilotRelevanceDrafted = "drafted"
	autopilotRelevanceScored  = "scored"
)

// autopilotRelevanceCounters is status.relevance.
type autopilotRelevanceCounters struct {
	Scored  int `json:"scored"`
	Skipped int `json:"skipped"`
	Drafted int `json:"drafted"`
}

// autopilotRelevanceEventsKeep is how long scoring events are kept.
const autopilotRelevanceEventsKeep = 30 * 24 * time.Hour

// autopilotMaxRelevanceEvents bounds the events in the state file.
const autopilotMaxRelevanceEvents = 3000

// autopilotEvent is a local notification for the portal bell (GET
// /api/v1/setup/autopilot/events): something the watcher did on its own that
// the owner should hear about. Kinds: digest_posted.
type autopilotEvent struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	CreatedAt time.Time `json:"createdAt"`
	PostID    string    `json:"postId"`
	Mint      string    `json:"mint"`
	Symbol    string    `json:"symbol"`
	Title     string    `json:"title"`
}

// autopilotMaxEvents bounds the events kept in the state file.
const autopilotMaxEvents = 50

// autopilotPersisted is the on-disk document.
type autopilotPersisted struct {
	// Day is the UTC day ("2006-01-02") the counters belong to.
	Day               string                `json:"day"`
	RepliesToday      int                   `json:"repliesToday"`
	CreditsSpentToday int                   `json:"creditsSpentToday"`
	Suggestions       []autopilotSuggestion `json:"suggestions"`
	// Replied maps post id to the reply id we posted there (never twice).
	Replied map[string]string `json:"replied"`
	// Seen maps post id to when the agent last acted on it (cooldown).
	Seen     map[string]time.Time `json:"seen"`
	Outcomes []autopilotOutcome   `json:"outcomes"`
	// DigestLastWeek is the ISO week ("2026-W38") the weekly digest was last
	// drafted for; one digest per week whatever happens after the draft.
	DigestLastWeek string `json:"digestLastWeek,omitempty"`
	// Events are the newest local notifications, oldest first.
	Events []autopilotEvent `json:"events,omitempty"`
	// Phase 3: scoring decisions of the last 30 days, bounty asks by post id,
	// and the self-tuned per-category thresholds with when they were computed.
	RelevanceEvents []autopilotRelevanceEvent          `json:"relevanceEvents,omitempty"`
	Asks            map[string]autopilotAsk            `json:"asks,omitempty"`
	TunedThresholds map[string]autopilotTunedThreshold `json:"tunedThresholds,omitempty"`
	TunedAt         time.Time                          `json:"tunedAt,omitempty"`
}

// autopilotStore is the in-memory copy of the state file.
type autopilotStore struct {
	mu   sync.Mutex
	path string // empty = memory only (tests without a data dir)
	now  func() time.Time
	doc  autopilotPersisted
}

func newAutopilotStore(dataDir string, now func() time.Time) *autopilotStore {
	st := &autopilotStore{now: now, doc: autopilotPersisted{Replied: map[string]string{}, Seen: map[string]time.Time{}, Asks: map[string]autopilotAsk{}}}
	if dataDir != "" {
		st.path = filepath.Join(dataDir, autopilotStateFilename)
	}
	return st
}

// load reads the state file; a missing or unreadable file starts empty.
func (st *autopilotStore) load() error {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.path == "" {
		return nil
	}
	raw, err := os.ReadFile(st.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var doc autopilotPersisted
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("autopilot state unreadable, starting empty: %w", err)
	}
	if doc.Replied == nil {
		doc.Replied = map[string]string{}
	}
	if doc.Seen == nil {
		doc.Seen = map[string]time.Time{}
	}
	if doc.Asks == nil {
		doc.Asks = map[string]autopilotAsk{}
	}
	st.doc = doc
	return nil
}

// saveLocked writes the document; the caller holds st.mu.
func (st *autopilotStore) saveLocked() error {
	if st.path == "" {
		return nil
	}
	out, err := json.MarshalIndent(st.doc, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(st.path), 0o700); err != nil {
		return err
	}
	tmp := st.path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, st.path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// rollDayLocked resets the counters when the UTC day changed.
func (st *autopilotStore) rollDayLocked() {
	day := st.now().UTC().Format("2006-01-02")
	if st.doc.Day != day {
		st.doc.Day = day
		st.doc.RepliesToday = 0
		st.doc.CreditsSpentToday = 0
	}
}

// counters returns today's reply and credit counters.
func (st *autopilotStore) counters() (replies, credits int) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.rollDayLocked()
	return st.doc.RepliesToday, st.doc.CreditsSpentToday
}

// addCredits records credits spent on drafts today.
func (st *autopilotStore) addCredits(n int) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.rollDayLocked()
	st.doc.CreditsSpentToday += n
	return st.saveLocked()
}

// recordReply marks a thread as replied, bumps the day counter and stores
// the outcome skeleton.
func (st *autopilotStore) recordReply(o autopilotOutcome) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.rollDayLocked()
	st.doc.RepliesToday++
	st.doc.Replied[o.PostID] = o.ReplyID
	st.doc.Seen[o.PostID] = o.At
	st.doc.Outcomes = append(st.doc.Outcomes, o)
	return st.saveLocked()
}

// markReplied records a reply found on the tracker (posted before the store
// knew about it) without counting it against today.
func (st *autopilotStore) markReplied(postID, replyID string) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.doc.Replied[postID] = replyID
	return st.saveLocked()
}

// replied reports whether the agent already replied in the thread.
func (st *autopilotStore) replied(postID string) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	_, ok := st.doc.Replied[postID]
	return ok
}

// touch records that the agent acted on the thread now (cooldown clock).
func (st *autopilotStore) touch(postID string) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.doc.Seen[postID] = st.now()
	return st.saveLocked()
}

// lastSeen returns when the agent last acted on the thread.
func (st *autopilotStore) lastSeen(postID string) (time.Time, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	t, ok := st.doc.Seen[postID]
	return t, ok
}

// addSuggestion stores a pending draft (replacing any pending one for the
// same post) and marks the thread seen.
func (st *autopilotStore) addSuggestion(sg autopilotSuggestion) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	kept := st.doc.Suggestions[:0]
	for _, existing := range st.doc.Suggestions {
		if existing.PostID != sg.PostID {
			kept = append(kept, existing)
		}
	}
	st.doc.Suggestions = append(kept, sg)
	st.doc.Seen[sg.PostID] = sg.CreatedAt
	return st.saveLocked()
}

// pendingSuggestions returns the unexpired suggestions, newest first, after
// dropping expired ones from the file.
func (st *autopilotStore) pendingSuggestions() []autopilotSuggestion {
	st.mu.Lock()
	defer st.mu.Unlock()
	now := st.now()
	kept := st.doc.Suggestions[:0]
	for _, sg := range st.doc.Suggestions {
		if sg.ExpiresAt.After(now) {
			kept = append(kept, sg)
		}
	}
	if len(kept) != len(st.doc.Suggestions) {
		st.doc.Suggestions = kept
		_ = st.saveLocked()
	}
	out := make([]autopilotSuggestion, len(kept))
	copy(out, kept)
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// hasPendingFor reports whether an unexpired suggestion exists for the post.
func (st *autopilotStore) hasPendingFor(postID string) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	now := st.now()
	for _, sg := range st.doc.Suggestions {
		if sg.PostID == postID && sg.ExpiresAt.After(now) {
			return true
		}
	}
	return false
}

// takeSuggestion removes and returns the pending suggestion with id.
func (st *autopilotStore) takeSuggestion(id string) (autopilotSuggestion, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	now := st.now()
	for i, sg := range st.doc.Suggestions {
		if sg.ID != id {
			continue
		}
		st.doc.Suggestions = append(st.doc.Suggestions[:i], st.doc.Suggestions[i+1:]...)
		st.doc.Seen[sg.PostID] = now
		_ = st.saveLocked()
		if !sg.ExpiresAt.After(now) {
			return autopilotSuggestion{}, false
		}
		return sg, true
	}
	return autopilotSuggestion{}, false
}

// restoreSuggestion puts a taken suggestion back (approve failed upstream).
func (st *autopilotStore) restoreSuggestion(sg autopilotSuggestion) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.doc.Suggestions = append(st.doc.Suggestions, sg)
	_ = st.saveLocked()
}

// outcomes returns a copy of the recorded auto replies (for tests and a
// future scoring job).
func (st *autopilotStore) outcomes() []autopilotOutcome {
	st.mu.Lock()
	defer st.mu.Unlock()
	out := make([]autopilotOutcome, len(st.doc.Outcomes))
	copy(out, st.doc.Outcomes)
	return out
}

// digestLastWeek returns the ISO week the digest was last drafted for.
func (st *autopilotStore) digestLastWeek() string {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.doc.DigestLastWeek
}

// markDigestWeek records that the digest for week was drafted (paid for), so
// the scheduler never drafts twice in one ISO week.
func (st *autopilotStore) markDigestWeek(week string) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.doc.DigestLastWeek = week
	return st.saveLocked()
}

// addEvent appends a local event, keeping the newest autopilotMaxEvents.
func (st *autopilotStore) addEvent(ev autopilotEvent) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.doc.Events = append(st.doc.Events, ev)
	if n := len(st.doc.Events); n > autopilotMaxEvents {
		st.doc.Events = append([]autopilotEvent{}, st.doc.Events[n-autopilotMaxEvents:]...)
	}
	return st.saveLocked()
}

// events returns the local events, newest first.
func (st *autopilotStore) events() []autopilotEvent {
	st.mu.Lock()
	defer st.mu.Unlock()
	out := make([]autopilotEvent, len(st.doc.Events))
	for i, ev := range st.doc.Events {
		out[len(out)-1-i] = ev
	}
	return out
}

// --- phase 3: relevance events, asks, tuned thresholds ----------------------

// addRelevanceEvent records one scoring decision, dropping events older than
// 30 days and keeping at most autopilotMaxRelevanceEvents.
func (st *autopilotStore) addRelevanceEvent(ev autopilotRelevanceEvent) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	cutoff := ev.At.Add(-autopilotRelevanceEventsKeep)
	kept := st.doc.RelevanceEvents[:0]
	for _, e := range st.doc.RelevanceEvents {
		if e.At.After(cutoff) {
			kept = append(kept, e)
		}
	}
	kept = append(kept, ev)
	if n := len(kept); n > autopilotMaxRelevanceEvents {
		kept = append([]autopilotRelevanceEvent{}, kept[n-autopilotMaxRelevanceEvents:]...)
	}
	st.doc.RelevanceEvents = kept
	return st.saveLocked()
}

// relevanceCounters returns the scoring counters of the last 30 days.
func (st *autopilotStore) relevanceCounters(now time.Time) autopilotRelevanceCounters {
	st.mu.Lock()
	defer st.mu.Unlock()
	cutoff := now.Add(-autopilotRelevanceEventsKeep)
	var c autopilotRelevanceCounters
	for _, e := range st.doc.RelevanceEvents {
		if !e.At.After(cutoff) {
			continue
		}
		c.Scored++
		switch e.Kind {
		case autopilotRelevanceSkipped:
			c.Skipped++
		case autopilotRelevanceDrafted:
			c.Drafted++
		}
	}
	return c
}

// recordAsk stores the ask posted on postID and starts the thread cooldown.
func (st *autopilotStore) recordAsk(postID string, ask autopilotAsk) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.rollDayLocked()
	st.doc.RepliesToday++
	st.doc.Asks[postID] = ask
	st.doc.Seen[postID] = ask.At
	return st.saveLocked()
}

// ask returns the ask posted on postID, if any.
func (st *autopilotStore) ask(postID string) (autopilotAsk, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	a, ok := st.doc.Asks[postID]
	return a, ok
}

// markAskAnswered records that the full answer went out after the ask.
func (st *autopilotStore) markAskAnswered(postID string, at time.Time) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	a, ok := st.doc.Asks[postID]
	if !ok {
		return nil
	}
	t := at
	a.AnsweredAt = &t
	st.doc.Asks[postID] = a
	return st.saveLocked()
}

// tuned returns the self-tuned thresholds and when they were computed.
func (st *autopilotStore) tuned() (map[string]autopilotTunedThreshold, time.Time) {
	st.mu.Lock()
	defer st.mu.Unlock()
	out := make(map[string]autopilotTunedThreshold, len(st.doc.TunedThresholds))
	for k, v := range st.doc.TunedThresholds {
		out[k] = v
	}
	return out, st.doc.TunedAt
}

// setTuned stores a fresh tuning result.
func (st *autopilotStore) setTuned(m map[string]autopilotTunedThreshold, at time.Time) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(m) == 0 {
		m = nil
	}
	st.doc.TunedThresholds, st.doc.TunedAt = m, at
	return st.saveLocked()
}

// resetTuning forgets when the thresholds were tuned so the next tick
// recomputes them (the policy threshold they derive from changed).
func (st *autopilotStore) resetTuning() error {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.doc.TunedAt = time.Time{}
	return st.saveLocked()
}
