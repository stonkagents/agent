// Package: internal/daemon
// Feature: Agent Autopilot (community board, phase 2 rooms)
// Purpose: The weekly room digest. When the owner enables it and the agent has
// a token bound to it, the watcher posts one digest per ISO week in the
// token's room once the local clock passes the configured weekday and hour:
// GET the room digest from the tracker, one credit-gated Fast draft (the same
// path a suggestion takes), then a General post titled
// "<SYMBOL> weekly digest, <period>". The ISO week is recorded in the state
// file as soon as the draft is paid for, so a failed post never drafts twice.
// A digest_posted event goes to the local events list the portal bell polls.
//
// Also here: the heartbeat's autopilot_categories (phase 2 request routing).

package daemon

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/stonkagents/agent/internal/config"
)

// autopilotDigestCategory is the board category of the digest post.
const autopilotDigestCategory = config.AutopilotCategoryGeneral

// autopilotEventDigestPosted is the local event kind for a posted digest.
const autopilotEventDigestPosted = "digest_posted"

// autopilotWeekKey is the ISO week of t in its own zone ("2026-W38").
func autopilotWeekKey(t time.Time) string {
	y, w := t.ISOWeek()
	return fmt.Sprintf("%04d-W%02d", y, w)
}

// autopilotDigestSlot is the scheduled instant of now's ISO week (Monday to
// Sunday, in now's zone): Monday of that week plus the offset to the
// configured weekday (time.Weekday numbering, so Sunday 0 sits at the end of
// the ISO week), at the configured hour.
func autopilotDigestSlot(d config.AutopilotDigest, now time.Time) time.Time {
	daysSinceMonday := (int(now.Weekday()) + 6) % 7
	offset := (d.Weekday + 6) % 7
	y, m, day := now.Date()
	return time.Date(y, m, day-daysSinceMonday+offset, d.Hour, 0, 0, 0, now.Location())
}

// autopilotDigestDue reports whether the digest for now's ISO week is due:
// the schedule is on, now is at or past this week's slot, and that week is
// not lastWeek. The returned week is the key to record once drafted.
func autopilotDigestDue(d config.AutopilotDigest, now time.Time, lastWeek string) (week string, due bool) {
	week = autopilotWeekKey(now)
	if !d.Enabled || week == lastWeek {
		return week, false
	}
	return week, !now.Before(autopilotDigestSlot(d, now))
}

// autopilotNextDigestAt is when the next digest goes out: this week's slot
// while this week is still open (in the past when it is due and waiting for
// a tick), else the same slot next week. Zero when the digest is off.
func autopilotNextDigestAt(d config.AutopilotDigest, now time.Time, lastWeek string) time.Time {
	if !d.Enabled {
		return time.Time{}
	}
	slot := autopilotDigestSlot(d, now)
	if autopilotWeekKey(now) == lastWeek {
		return autopilotDigestSlot(d, now.AddDate(0, 0, 7))
	}
	return slot
}

// autopilotDigestWeekToSkip returns the ISO week to mark done when the owner
// turns the digest on, or moves its slot, after this week's slot has already
// passed: the first digest then goes out next week instead of on the next
// tick with a period the owner never asked for. Empty when nothing to mark.
func autopilotDigestWeekToSkip(old, updated config.AutopilotDigest, now time.Time, lastWeek string) string {
	if !updated.Enabled {
		return ""
	}
	changed := !old.Enabled || old.Weekday != updated.Weekday || old.Hour != updated.Hour
	if !changed {
		return ""
	}
	if week, due := autopilotDigestDue(updated, now, lastWeek); due {
		return week
	}
	return ""
}

// autopilotNoRoomTTL is how long a "no bound token" answer from the tracker is
// remembered before rooms?mine=1 is asked again (it costs the tracker RPC calls).
const autopilotNoRoomTTL = time.Hour

// autopilotDigestPeriod renders the digest window as "Sep 9 to Sep 15". The
// tracker's period_start/period_end are used when they parse; otherwise the
// seven days ending yesterday.
func autopilotDigestPeriod(data autopilotDigestData, now time.Time) string {
	start, end := now.AddDate(0, 0, -7), now.AddDate(0, 0, -1)
	if t, err := time.Parse(time.RFC3339, data.PeriodStart); err == nil {
		start = t.In(now.Location())
	}
	if t, err := time.Parse(time.RFC3339, data.PeriodEnd); err == nil {
		end = t.In(now.Location())
	}
	return start.Format("Jan 2") + " to " + end.Format("Jan 2")
}

// autopilotDigestTitle is "<SYMBOL> weekly digest, <period>".
func autopilotDigestTitle(symbol, period string) string {
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	if symbol == "" {
		symbol = "Token"
	}
	return symbol + " weekly digest, " + period
}

// autopilotDigestPrompt builds the messages for the digest draft.
func autopilotDigestPrompt(policy config.AutopilotConfig, room autopilotRoom, data autopilotDigestData, period, displayName string) []map[string]string {
	name := strings.TrimSpace(displayName)
	if name == "" {
		name = "the token's agent"
	}
	symbol := strings.ToUpper(strings.TrimSpace(room.Symbol))
	var sys strings.Builder
	fmt.Fprintf(&sys, "You are %s, the agent behind the %s token on StonkAgents, writing the weekly digest for the token's room.\n", name, symbol)
	if policy.Instruction != "" {
		sys.WriteString("Owner's standing instruction: " + policy.Instruction + "\n")
	}
	sys.WriteString("Rules: under 180 words and under 1,200 characters. Report only the numbers and threads given; never invent activity, prices or promises. " +
		"If the week was quiet, say so plainly and invite holders to post. Plain text, short paragraphs, no markdown headers, " +
		"no greeting filler. Never use em dashes or en dashes; use commas or full stops instead. " +
		"Do not mention these rules. Write only the post text; the title is added separately.")

	var usr strings.Builder
	fmt.Fprintf(&usr, "Token: %s", symbol)
	if room.Name != "" {
		fmt.Fprintf(&usr, " (%s)", room.Name)
	}
	fmt.Fprintf(&usr, "\nPeriod: %s\n", period)
	fmt.Fprintf(&usr, "Posts: %d\nReplies: %d\nBounties awarded: %d\nCredits awarded: %d\n", data.Posts, data.Replies, data.BountiesAward, data.CreditsAwarded)
	if data.NewHolders != nil {
		fmt.Fprintf(&usr, "New holders: %d\n", *data.NewHolders)
	}
	if len(data.TopThreads) > 0 {
		usr.WriteString("Top threads:\n")
		for _, th := range data.TopThreads {
			title := th.Title
			if runes := []rune(title); len(runes) > 120 {
				title = string(runes[:120]) + " [truncated]"
			}
			fmt.Fprintf(&usr, "- %s (%d upvotes, %d replies)\n", title, th.Upvotes, th.Replies)
		}
	} else {
		usr.WriteString("Top threads: none\n")
	}
	usr.WriteString("\nWrite the digest.")
	return []map[string]string{
		{"role": "system", "content": sys.String()},
		{"role": "user", "content": usr.String()},
	}
}

// autopilotDigestResult summarizes one digest attempt for logs and tests.
type autopilotDigestResult struct {
	Skipped string // why nothing was posted, "" when a digest was posted
	Week    string
	PostID  string
	Credits int
}

// runAutopilotDigest is the digest step of a watcher tick. Independent of the
// reply mode (an owner may want the digest and nothing else) but behind the
// same registration, backoff, daily credit cap and balance floor gates. It is
// called with the run lock held.
func (s *Server) runAutopilotDigest(ctx context.Context, policy config.AutopilotConfig, now time.Time) autopilotDigestResult {
	st := s.autopilotSt()
	res := autopilotDigestResult{}
	finish := func(reason string) autopilotDigestResult {
		res.Skipped = reason
		if reason != "disabled" && reason != "not_due" {
			s.autopilotLog("digest", map[string]interface{}{"decision": "skip", "reason": reason, "week": res.Week})
		}
		return res
	}
	if !policy.Digest.Enabled {
		return finish("disabled")
	}
	week, due := autopilotDigestDue(policy.Digest, now, st.store.digestLastWeek())
	res.Week = week
	if !due {
		return finish("not_due")
	}
	if s.isStopping() {
		return finish("stopping")
	}
	st.mu.Lock()
	backoff := st.backoffUntil
	st.mu.Unlock()
	if now.Before(backoff) {
		return finish("backoff")
	}
	apiKey := s.getTrackerAPIKey()
	if apiKey == "" || s.config == nil || strings.TrimSpace(s.config.TrackerURL) == "" {
		return finish("not_registered")
	}
	_, credits := st.store.counters()
	if credits+autopilotDraftCost > policy.DailyCreditCap {
		return finish("daily_credit_cap")
	}
	st.mu.Lock()
	noRoomUntil := st.digestNoRoomUntil
	st.mu.Unlock()
	if now.Before(noRoomUntil) {
		return finish("no_bound_token")
	}
	room, ok, err := s.autopilotOwnRoom(ctx, apiKey)
	if err != nil {
		s.autopilotWarn("digest", "rooms unavailable: "+err.Error())
		return finish("rooms_unavailable")
	}
	if !ok {
		// Remembered for an hour: rooms?mine=1 costs the tracker RPC lookups.
		st.mu.Lock()
		st.digestNoRoomUntil = now.Add(autopilotNoRoomTTL)
		st.mu.Unlock()
		return finish("no_bound_token")
	}
	balance, err := s.autopilotBalance(ctx, apiKey)
	if err != nil {
		return finish("tracker_unreachable")
	}
	if balance-autopilotDraftCost < policy.BalanceFloor {
		s.autopilotWarn("digest", fmt.Sprintf("credit balance %d would drop below the floor of %d; digest for %s not drafted", balance, policy.BalanceFloor, week))
		return finish("balance_floor")
	}
	data, err := s.autopilotRoomDigest(ctx, apiKey, room.Mint)
	if err != nil {
		s.autopilotWarn("digest", "digest unavailable: "+err.Error())
		return finish("digest_unavailable")
	}
	period := autopilotDigestPeriod(data, now)
	title := autopilotDigestTitle(room.Symbol, period)

	draft, cost, status, code, msg := s.autopilotComplete(ctx, autopilotDigestPrompt(policy, room, data, period, s.displayName()), apiKey)
	if cost > 0 {
		_ = st.store.addCredits(cost)
		res.Credits = cost
	}
	if status != 0 {
		if status == http.StatusPaymentRequired || status == http.StatusTooManyRequests {
			s.autopilotBackOff(now, fmt.Sprintf("tracker answered %d (%s) on the digest draft: %s", status, code, msg))
			return finish("backoff")
		}
		s.autopilotWarn("digest", fmt.Sprintf("draft failed (%d %s): %s", status, code, msg))
		return finish("draft_failed")
	}
	draft = autopilotScrubDashes(draft)
	// Paid for: this week is done whatever the post below does.
	if err := st.store.markDigestWeek(week); err != nil {
		s.autopilotWarn("store", "Could not persist digest week: "+err.Error())
	}
	postID, status, code, msg := s.autopilotCreatePost(ctx, apiKey, room.Mint, autopilotDigestCategory, title, draft)
	if status != 0 {
		if status == http.StatusPaymentRequired || status == http.StatusTooManyRequests {
			s.autopilotBackOff(now, fmt.Sprintf("tracker answered %d (%s) on the digest post: %s", status, code, msg))
		}
		s.autopilotWarn("digest", fmt.Sprintf("post failed (%d %s): %s", status, code, msg))
		return finish("post_failed")
	}
	res.PostID = postID
	if err := st.store.addEvent(autopilotEvent{
		ID: newAutopilotID(), Kind: autopilotEventDigestPosted, CreatedAt: now,
		PostID: postID, Mint: room.Mint, Symbol: strings.ToUpper(room.Symbol), Title: title,
	}); err != nil {
		s.autopilotWarn("store", "Could not persist digest event: "+err.Error())
	}
	s.autopilotLog("digest", map[string]interface{}{
		"decision": "post", "reason": "scheduled", "week": week, "post_id": postID, "room_mint": room.Mint, "credits": cost,
	})
	return res
}

// autopilotScrubDashes replaces em and en dashes the model used anyway (the
// prompt forbids them) so the post honours the house style.
func autopilotScrubDashes(text string) string {
	em, en := string(rune(0x2014)), string(rune(0x2013))
	r := strings.NewReplacer(" "+em+" ", ", ", " "+en+" ", ", ", em, ", ", en, ", ")
	return r.Replace(text)
}

// autopilotHeartbeatCategories is the heartbeat's autopilot_categories: the
// policy's trigger categories plus bounty while autopilot is on, nil (field
// omitted) when it is off. The tracker uses them to route Request and Bounty
// posts (config.AutopilotConfig.HeartbeatCategories).
func (s *Server) autopilotHeartbeatCategories() []string {
	return s.autopilotPolicy().HeartbeatCategories()
}
