// Package: internal/daemon
// Feature: Agent Autopilot (community board)
// Purpose: The board watcher. Every 5 minutes (or at once when the owner turns
// autopilot on) it fetches the first page of recent posts and, for each one,
// runs a free local gate before spending anything: own posts, threads it
// already answered, cooled-down threads, stale posts and posts that match no
// trigger are skipped without a tracker call. Only a triggered post costs a
// completion (one Fast draft), and only the owner's mode and caps decide
// whether the draft is posted or parked as a suggestion.
//
// The agent never browses: it wakes on the timer, spends within the owner's
// caps, and backs off for an hour when the tracker says 402 or 429.
//
// Decision table (per post, in order; the first hit wins):
//
//	own post                                  skip  own_post
//	already replied (store or tracker)        skip  already_replied
//	suggestion pending for the post           skip  suggestion_pending
//	acted on within thread_cooldown_hours     skip  cooldown
//	no or unparsable timestamp                skip  no_timestamp
//	older than max_post_age_hours             skip  too_old
//	no trigger (category / bounty / mention)  skip  no_trigger
//	mode suggest                              suggest
//	token-offer post (any auto mode)          suggest
//	mode auto                                 post
//	mode bounty, bounty >= multiple x cost    post
//	mode bounty otherwise                     suggest
//
// Run-level gates (checked before any post): mode off, shutting down, backoff,
// office hours, not registered, daily credit cap, daily reply cap (auto modes),
// tracker unreachable, balance below floor.
//
// Phase 2 (rooms and matchmaking): every tick first runs the weekly digest
// step (autopilot_digest.go, independent of the reply mode), then reads the
// unread request_routed activity (posts the tracker matched to this agent)
// and examines those posts, fetched by id, before the feed page. Same gates
// for both sources; a routed post only ranks earlier.
//
// Phase 3 (Autopilot v2): before a draft is paid for, the post is scored for
// relevance (autopilot_relevance.go); an unrouted, unmentioned feed post
// under the threshold is skipped with relevance_low (mode skip) or drafted
// with the score recorded (mode note); routed, mentioned, raised and rich
// bounty posts always pass (score recorded).
// Every draft gets a row in the outcome ledger (autopilot_ledger.go), whose
// outcomes are refreshed from the tracker each tick and tune the threshold
// per category once a day. In bounty hunter mode a Request without a
// worthwhile bounty gets a short ask (ask credits on the reply) instead of an
// answer; the answer is drafted once a bounty_raised activity says the bounty
// meets the ask. Extra rows in the decision table, in order:
//
//	bounty ask posted, no raise yet             skip  ask_pending
//	raised but bounty still under the ask       skip  bounty_under_ask
//	relevance under the threshold (mode skip)   skip  relevance_low
//	mode bounty, no or small bounty, Request    ask

package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/stonkagents/agent/internal/config"
)

const (
	// autopilotInterval is the watcher cadence.
	autopilotInterval = 5 * time.Minute
	// autopilotBackoff is how long the watcher rests after a 402 or 429.
	autopilotBackoff = time.Hour
	// autopilotDraftCost is the assumed cost of one Fast draft, used for the
	// bounty threshold and the budget check before a draft is requested. The
	// tracker's X-Credits-Deducted header is what actually gets counted.
	autopilotDraftCost = agentChatCreditsPerCompletion
	// autopilotPostsPerRun bounds the first page fetched from the tracker.
	autopilotPostsPerRun = 20
	// autopilotMentionPrefixLen is how many leading characters of the peer id
	// count as a mention (all libp2p ids share the first eight).
	autopilotMentionPrefixLen = 16
	// autopilotMaxPostChars bounds how much of a post goes into the prompt.
	autopilotMaxPostChars = 4000
	// autopilotHTTPTimeout bounds each board or balance call.
	autopilotHTTPTimeout = 30 * time.Second
	// autopilotDraftTimeout bounds one draft completion.
	autopilotDraftTimeout = 2 * time.Minute
	// AutopilotAutoHeader marks a reply as posted by autopilot (the tracker
	// tags it); the body carries auto: true as well.
	AutopilotAutoHeader = "X-StonkAgents-Auto"
)

// autopilotState is the watcher state hung off Server.
type autopilotState struct {
	mu               sync.Mutex
	policy           config.AutopilotConfig
	lastRunAt        time.Time
	backoffUntil     time.Time
	downgradedReason string
	// digestNoRoomUntil: the tracker said "no bound token"; not asked again before this.
	digestNoRoomUntil time.Time
	// trackerCapUntil: the tracker answered AUTO_REPLY_LIMIT (its own daily
	// cap); no auto post is tried before this (the next UTC midnight), while
	// outcomes keep refreshing and suggestions keep flowing.
	trackerCapUntil time.Time

	runMu   sync.Mutex // one run at a time (timer and wake may coincide)
	wake    chan struct{}
	store   *autopilotStore
	ledger  *autopilotLedger
	library *autopilotLibraryIndex
	now     func() time.Time
	http    *http.Client
}

// autopilotSt returns the watcher state, created on first use from the loaded
// config and the state file under the data dir.
func (s *Server) autopilotSt() *autopilotState {
	s.autopilotOnce.Do(func() {
		if s.autopilot == nil {
			s.autopilot = &autopilotState{}
		}
		st := s.autopilot
		if st.now == nil {
			st.now = time.Now
		}
		if st.http == nil {
			st.http = &http.Client{Timeout: autopilotHTTPTimeout}
		}
		if st.wake == nil {
			st.wake = make(chan struct{}, 1)
		}
		dataDir := ""
		if s.config != nil {
			st.policy = s.config.Autopilot
			dataDir = s.config.DataDir
		}
		st.policy.ApplyDefaults()
		if st.store == nil {
			st.store = newAutopilotStore(dataDir, st.now)
			if err := st.store.load(); err != nil && s.logger != nil {
				s.logger.Warn("Autopilot", "load", err.Error(), nil)
			}
		}
		if st.ledger == nil {
			st.ledger = newAutopilotLedger(dataDir, st.now)
			if err := st.ledger.load(); err != nil && s.logger != nil {
				s.logger.Warn("Autopilot", "load", err.Error(), nil)
			}
		}
		if st.library == nil {
			st.library = &autopilotLibraryIndex{source: s.autopilotLibrarySource}
		}
	})
	return s.autopilot
}

// autopilotPolicy returns a copy of the live policy.
func (s *Server) autopilotPolicy() config.AutopilotConfig {
	st := s.autopilotSt()
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.policy
}

// setAutopilotPolicy applies a validated policy live and wakes the watcher
// when it just became active (or changed while active).
func (s *Server) setAutopilotPolicy(p config.AutopilotConfig) {
	st := s.autopilotSt()
	st.mu.Lock()
	wasEnabled := st.policy.Enabled()
	oldDigest := st.policy.Digest
	retune := st.policy.RelevanceThreshold != p.RelevanceThreshold || !equalThresholdPins(st.policy.RelevanceThresholdByCategory, p.RelevanceThresholdByCategory)
	st.policy = p
	if p.Enabled() {
		st.downgradedReason = ""
	}
	if oldDigest != p.Digest {
		st.digestNoRoomUntil = time.Time{} // a schedule change may follow a claim; ask again
	}
	st.mu.Unlock()
	// Turning the digest on (or moving its slot) after this week's slot has
	// passed: the first digest goes out next week, not on the next tick.
	if week := autopilotDigestWeekToSkip(oldDigest, p.Digest, st.now(), st.store.digestLastWeek()); week != "" {
		if err := st.store.markDigestWeek(week); err != nil {
			s.autopilotWarn("store", "Could not persist digest week: "+err.Error())
		}
		s.autopilotLog("digest", map[string]interface{}{"decision": "skip", "reason": "enabled_after_slot", "week": week})
	}
	if retune {
		// The tuned values derive from the policy threshold: recompute next tick.
		if err := st.store.resetTuning(); err != nil {
			s.autopilotWarn("store", "Could not reset tuning: "+err.Error())
		}
	}
	s.configMu.Lock()
	if s.config != nil {
		s.config.Autopilot = p
	}
	s.configMu.Unlock()
	if p.Enabled() && !wasEnabled {
		s.wakeAutopilot()
	}
}

// wakeAutopilot asks the loop for an immediate run (coalesced).
func (s *Server) wakeAutopilot() {
	st := s.autopilotSt()
	select {
	case st.wake <- struct{}{}:
	default:
	}
}

// startAutopilotLoop runs the watcher until shutdown. Called from Start.
func (s *Server) startAutopilotLoop() {
	s.goBackground(func() {
		st := s.autopilotSt()
		if s.logger != nil {
			s.logger.Info("Autopilot", "start", "Board watcher started", map[string]interface{}{
				"mode": st.policy.Mode, "interval": autopilotInterval.String(),
			})
		}
		ticker := time.NewTicker(autopilotInterval)
		defer ticker.Stop()
		for {
			select {
			case <-s.stopping:
				return
			case <-ticker.C:
			case <-st.wake:
			}
			s.runAutopilotOnce(context.Background())
		}
	})
}

// --- run ----------------------------------------------------------------

// autopilotRunResult summarizes one run for logs and tests.
type autopilotRunResult struct {
	Skipped    string // run-level skip reason, "" when posts were examined
	Examined   int
	Routed     int // how many of the examined posts came from request_routed activity
	RoutedRead int // routed activity items marked read (post handled)
	Suggested  int
	Posted     int
	Credits    int
	BackedOff  bool
	StoppedFor string // why the loop over posts ended early
	Digest     autopilotDigestResult
	// Phase 3.
	Asked             int // bounty asks posted
	Raised            int // posts that came back through bounty_raised activity
	RelevanceSkipped  int // posts skipped with relevance_low
	OutcomesRefreshed int // ledger rows changed by the outcome refresh
}

// autopilotActivityRef is the activity item that brought a post into the run.
type autopilotActivityRef struct {
	ID   string
	Kind string // request_routed | bounty_raised
}

// runAutopilotOnce performs one watcher pass. Safe to call from tests.
func (s *Server) runAutopilotOnce(ctx context.Context) autopilotRunResult {
	st := s.autopilotSt()
	if !st.runMu.TryLock() {
		return autopilotRunResult{Skipped: "already_running"}
	}
	defer st.runMu.Unlock()

	policy := s.autopilotPolicy()
	now := st.now()
	res := autopilotRunResult{}
	finish := func(reason string) autopilotRunResult {
		res.Skipped = reason
		st.mu.Lock()
		st.lastRunAt = now
		st.mu.Unlock()
		s.autopilotLog("run", map[string]interface{}{"decision": "skip_run", "reason": reason, "mode": policy.Mode})
		return res
	}

	// The digest has its own switch and clock; it must not wait for a reply mode.
	res.Digest = s.runAutopilotDigest(ctx, policy, now)
	// So do the outcome ledger and the tuning: earlier replies keep scoring
	// whatever the mode is now.
	res.OutcomesRefreshed = s.autopilotRefreshOutcomes(ctx, now)
	tuned := s.autopilotTune(policy, now)

	if !policy.Enabled() {
		return finish("mode_off")
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
	if !policy.OfficeHours.Contains(now) {
		return finish("outside_office_hours")
	}
	apiKey := s.getTrackerAPIKey()
	if apiKey == "" || s.config == nil || strings.TrimSpace(s.config.TrackerURL) == "" {
		return finish("not_registered")
	}
	replies, credits := st.store.counters()
	if credits+autopilotDraftCost > policy.DailyCreditCap {
		return finish("daily_credit_cap")
	}
	if policy.AutoPosts() && replies >= policy.MaxRepliesPerDay {
		return finish("daily_reply_cap")
	}
	st.mu.Lock()
	capUntil := st.trackerCapUntil
	st.mu.Unlock()
	if policy.AutoPosts() && now.Before(capUntil) {
		return finish("tracker_reply_cap")
	}
	balance, err := s.autopilotBalance(ctx, apiKey)
	if err != nil {
		s.setAutopilotDowngraded("tracker unreachable: " + err.Error())
		return finish("tracker_unreachable")
	}
	if balance < policy.BalanceFloor {
		s.setAutopilotDowngraded(fmt.Sprintf("credit balance %d is below the floor of %d", balance, policy.BalanceFloor))
		return finish("balance_floor")
	}
	posts, activity, err := s.autopilotCandidatePosts(ctx, apiKey)
	if err != nil {
		s.setAutopilotDowngraded("tracker unreachable: " + err.Error())
		return finish("tracker_unreachable")
	}
	s.setAutopilotDowngraded("")

	// Budget for this run: whichever of the daily cap and the floor binds first.
	budget := policy.DailyCreditCap - credits
	if left := balance - policy.BalanceFloor; left < budget {
		budget = left
	}
	peerID := s.identityPeerID()
	displayName := s.displayName()

	for _, post := range posts {
		if s.isStopping() {
			res.StoppedFor = "stopping"
			break
		}
		res.Examined++
		ref, fromActivity := activity[post.ID]
		isRouted := fromActivity && ref.Kind == autopilotActivityRouted
		isRaised := fromActivity && ref.Kind == autopilotActivityRaised
		if isRouted {
			res.Routed++
		}
		if isRaised {
			res.Raised++
		}
		// handled: the post's fate is settled for this activity item, so the
		// routed row may be marked read; anything left unsettled (a cap, the
		// budget, a backoff, a transient tracker error) stays unread for next tick.
		handled := func() {
			if !fromActivity {
				return
			}
			if err := s.autopilotMarkActivityRead(ctx, apiKey, ref.ID); err != nil {
				s.autopilotWarn("routed", "could not mark activity read: "+err.Error())
				return
			}
			res.RoutedRead++
		}
		seenAt, seen := st.store.lastSeen(post.ID)
		ask, asked := st.store.ask(post.ID)
		cand := autopilotCandidate{
			post: post, replied: st.store.replied(post.ID), pending: st.store.hasPendingFor(post.ID),
			seenAt: seenAt, seen: seen, peerID: peerID, displayName: displayName,
			asked: asked, askAmount: ask.Ask, raised: isRaised && asked,
		}
		d := autopilotDecide(policy, cand, now)
		fields := map[string]interface{}{"post_id": post.ID, "category": post.Category, "decision": d.action.String(), "reason": d.reason, "routed": isRouted, "raised": isRaised}
		if d.action == autopilotSkip {
			if d.reason != "cooldown" {
				handled() // own post, replied, pending, no timestamp, too old, no trigger: nothing changes later
			}
			s.autopilotLog("post", fields)
			continue
		}

		// Relevance (phase 3): free, local, before anything is paid for. The
		// gate only holds back unrouted, unmentioned feed posts: a routed
		// request (the tracker matched this agent), a direct @mention, a raise
		// after our ask and a rich bounty are always worth the draft; their
		// score is still recorded.
		var rel *autopilotRelevanceResult
		if policy.RelevanceScored() {
			r := s.autopilotScore(policy, post, isRouted, now)
			rel = &r
			threshold := autopilotEffectiveThreshold(policy, tuned, post.Category)
			fields["relevance"], fields["relevance_threshold"] = r.score, threshold
			// A rich bounty skips the threshold but still needs some signal: a
			// post with nothing in common with the agent never gets a paid draft.
			rich := autopilotRichBounty(policy, post.Bounty) && r.score > 0
			gated := !isRouted && !cand.raised && d.trigger != "mention" && !rich
			if gated && r.score < threshold &&
				(policy.RelevanceMode == config.AutopilotRelevanceSkip || d.action == autopilotAskAction) {
				_ = st.store.addRelevanceEvent(autopilotRelevanceEvent{At: now, PostID: post.ID, Score: r.score, Kind: autopilotRelevanceSkipped})
				_ = st.store.touch(post.ID) // rescored after the cooldown, not every tick
				res.RelevanceSkipped++
				handled()
				fields["decision"], fields["reason"] = "skip", "relevance_low"
				s.autopilotLog("post", fields)
				continue
			}
		}

		if (d.action == autopilotPostAction || d.action == autopilotAskAction) && replies >= policy.MaxRepliesPerDay {
			res.StoppedFor = "daily_reply_cap"
			fields["decision"], fields["reason"] = "stop", "daily_reply_cap"
			s.autopilotLog("post", fields)
			break
		}
		if budget < autopilotDraftCost {
			res.StoppedFor = "budget_exhausted"
			fields["decision"], fields["reason"] = "stop", "budget_exhausted"
			s.autopilotLog("post", fields)
			break
		}

		// Remote check: the tracker's own reply list is authoritative for "never
		// twice". Our own ask on the post is not an answer.
		remote, err := s.autopilotReplies(ctx, apiKey, post.ID)
		if err != nil {
			fields["decision"], fields["reason"] = "skip", "replies_unavailable"
			s.autopilotLog("post", fields)
			continue
		}
		if rid := repliedByExcept(remote, peerID, ask.ReplyID); rid != "" {
			_ = st.store.markReplied(post.ID, rid)
			handled()
			fields["decision"], fields["reason"] = "skip", "already_replied"
			s.autopilotLog("post", fields)
			continue
		}

		var draft string
		var cost, status int
		var code, msg string
		askAmount := 0
		if d.action == autopilotAskAction {
			askAmount = autopilotAskAmount(policy, post.Bounty)
			fields["ask"] = askAmount
			draft, cost, status, code, msg = s.autopilotComplete(ctx, autopilotAskPrompt(policy, post, displayName, askAmount), apiKey)
			draft = autopilotTrimAsk(draft)
		} else {
			draft, cost, status, code, msg = s.autopilotDraft(ctx, policy, post, displayName, apiKey)
		}
		if cost > 0 {
			_ = st.store.addCredits(cost)
			budget -= cost
			res.Credits += cost
		}
		if status != 0 {
			if rel != nil {
				_ = st.store.addRelevanceEvent(autopilotRelevanceEvent{At: now, PostID: post.ID, Score: rel.score, Kind: autopilotRelevanceScored})
			}
			fields["reason"], fields["status"], fields["code"] = "draft_failed", status, code
			if status == http.StatusPaymentRequired || status == http.StatusTooManyRequests {
				s.autopilotBackOff(now, fmt.Sprintf("tracker answered %d (%s): %s", status, code, msg))
				res.BackedOff = true
				res.StoppedFor = "backoff"
				fields["decision"] = "stop"
				s.autopilotLog("post", fields)
				break
			}
			fields["decision"] = "skip"
			s.autopilotLog("post", fields)
			continue
		}
		fields["credits"] = cost
		if rel != nil {
			_ = st.store.addRelevanceEvent(autopilotRelevanceEvent{At: now, PostID: post.ID, Score: rel.score, Kind: autopilotRelevanceDrafted})
		}
		row := s.newAutopilotLedgerRow(post, rel, cost, now)
		if askAmount > 0 {
			row.Kind, row.Ask = autopilotLedgerKindAsk, askAmount
		}
		if err := st.ledger.add(row); err != nil {
			s.autopilotWarn("ledger", "Could not persist ledger row: "+err.Error())
		}

		if d.action == autopilotSuggestAction {
			sg := s.newAutopilotSuggestion(post, draft, cost, now)
			sg.LedgerID = row.ID
			sg.Relevance, sg.RelevanceSignals = rel.dto()
			if err := st.store.addSuggestion(sg); err != nil {
				s.autopilotWarn("store", "Could not persist suggestion: "+err.Error())
			}
			res.Suggested++
			handled()
			s.autopilotLog("post", fields)
			continue
		}

		opts := autopilotReplyOpts{ask: askAmount}
		opts.relevance, opts.signals = rel.dto()
		replyID, status, code, msg := s.autopilotPostReplyWith(ctx, apiKey, post.ID, draft, opts)
		if status != 0 {
			fields["status"], fields["code"] = status, code
			if askAmount > 0 {
				// An ask is not worth keeping; the post is looked at again after the cooldown.
				_ = st.store.touch(post.ID)
				fields["decision"], fields["reason"] = "skip", "ask_failed"
			} else {
				// Keep the paid-for draft as a suggestion so the owner can still use it.
				sg := s.newAutopilotSuggestion(post, draft, cost, now)
				sg.LedgerID = row.ID
				sg.Relevance, sg.RelevanceSignals = rel.dto()
				_ = st.store.addSuggestion(sg)
				res.Suggested++
				fields["decision"], fields["reason"] = "suggest", "post_failed"
			}
			if status == http.StatusTooManyRequests && code == autopilotTrackerReplyCap {
				// The tracker's own daily cap: nothing more posts today, no backoff
				// (outcomes keep refreshing, suggestions keep flowing).
				s.autopilotTrackerCapReached(now)
				res.StoppedFor = "tracker_reply_cap"
				handled()
				s.autopilotLog("post", fields)
				break
			}
			if status == http.StatusPaymentRequired || status == http.StatusTooManyRequests {
				s.autopilotBackOff(now, fmt.Sprintf("tracker answered %d (%s): %s", status, code, msg))
				res.BackedOff = true
				res.StoppedFor = "backoff"
				s.autopilotLog("post", fields)
				break
			}
			handled() // parked as a suggestion, or an ask the tracker refused; the owner or the next look takes it from here
			s.autopilotLog("post", fields)
			continue
		}
		if err := st.ledger.markPosted(row.ID, replyID, now); err != nil {
			s.autopilotWarn("ledger", "Could not persist ledger row: "+err.Error())
		}
		fields["reply_id"] = replyID
		if askAmount > 0 {
			if err := st.store.recordAsk(post.ID, autopilotAsk{ReplyID: replyID, Ask: askAmount, Category: post.Category, At: now}); err != nil {
				s.autopilotWarn("store", "Could not persist ask: "+err.Error())
			}
			replies++
			res.Asked++
			handled()
			s.autopilotLog("post", fields)
			continue
		}
		bounty := 0
		if post.Bounty != nil {
			bounty = post.Bounty.Amount
		}
		outcome := autopilotOutcome{ReplyID: replyID, PostID: post.ID, At: now, Trigger: d.trigger, Bounty: bounty, Credits: cost}
		outcome.Relevance, outcome.RelevanceSignals = rel.dto()
		if err := st.store.recordReply(outcome); err != nil {
			s.autopilotWarn("store", "Could not persist reply outcome: "+err.Error())
		}
		if cand.raised {
			_ = st.store.markAskAnswered(post.ID, now)
		}
		replies++
		res.Posted++
		handled()
		s.autopilotLog("post", fields)
	}

	st.mu.Lock()
	st.lastRunAt = now
	st.mu.Unlock()
	s.autopilotLog("run", map[string]interface{}{
		"decision": "done", "mode": policy.Mode, "examined": res.Examined, "routed": res.Routed, "routed_read": res.RoutedRead, "suggested": res.Suggested,
		"posted": res.Posted, "asked": res.Asked, "raised": res.Raised, "relevance_skipped": res.RelevanceSkipped,
		"credits": res.Credits, "stopped_for": res.StoppedFor,
	})
	return res
}

// autopilotCandidatePosts returns the posts to examine this tick: the unread
// bounty_raised activity (a raise after one of our asks) and the unread
// request_routed activity first (each post fetched by id), then the recent
// feed page minus posts already listed. The returned map says which activity
// item brought each post. Only a failed feed read is an error; a tracker
// without an activity filter, or a routed post that cannot be fetched, just
// means the feed alone is examined.
func (s *Server) autopilotCandidatePosts(ctx context.Context, apiKey string) ([]autopilotPost, map[string]autopilotActivityRef, error) {
	st := s.autopilotSt()
	activity := map[string]autopilotActivityRef{} // post id -> activity item
	var posts []autopilotPost
	markRead := func(kind, id string) {
		if err := s.autopilotMarkActivityRead(ctx, apiKey, id); err != nil {
			s.autopilotWarn(kind, "could not mark activity read: "+err.Error())
		}
	}
	raised, err := s.autopilotRaisedItems(ctx, apiKey)
	if err != nil {
		s.autopilotWarn("raised", "bounty_raised activity unavailable: "+err.Error())
	}
	for _, it := range raised {
		if _, ok := st.store.ask(it.PostID); !ok {
			// A raise on a post we never asked on (or the ask was lost): nothing to do.
			s.autopilotLog("post", map[string]interface{}{"post_id": it.PostID, "decision": "skip", "reason": "raise_without_ask", "raised": true})
			markRead("raised", it.ID)
			continue
		}
		post, err := s.autopilotPostByID(ctx, apiKey, it.PostID)
		if err != nil {
			s.autopilotLog("post", map[string]interface{}{"post_id": it.PostID, "decision": "skip", "reason": "raised_post_unavailable", "raised": true})
			markRead("raised", it.ID)
			continue
		}
		if post.ID == "" {
			post.ID = it.PostID
		}
		activity[post.ID] = autopilotActivityRef{ID: it.ID, Kind: autopilotActivityRaised}
		posts = append(posts, post)
	}
	items, err := s.autopilotRoutedItems(ctx, apiKey)
	if err != nil {
		s.autopilotWarn("routed", "routed activity unavailable, feed only: "+err.Error())
	}
	for _, it := range items {
		if _, ok := activity[it.PostID]; ok {
			continue
		}
		post, err := s.autopilotPostByID(ctx, apiKey, it.PostID)
		if err != nil {
			// Gone or hidden since it was routed: nothing to answer, mark it read.
			s.autopilotLog("post", map[string]interface{}{"post_id": it.PostID, "decision": "skip", "reason": "routed_post_unavailable", "routed": true})
			markRead("routed", it.ID)
			continue
		}
		if post.ID == "" {
			post.ID = it.PostID
		}
		activity[post.ID] = autopilotActivityRef{ID: it.ID, Kind: autopilotActivityRouted}
		posts = append(posts, post)
	}
	feed, err := s.autopilotRecentPosts(ctx, apiKey)
	if err != nil {
		return nil, nil, err
	}
	for _, post := range feed {
		if _, ok := activity[post.ID]; ok {
			continue
		}
		posts = append(posts, post)
	}
	return posts, activity, nil
}

// repliedBy returns the id of the first reply authored by peerID, or "".
func repliedBy(replies []autopilotReply, peerID string) string {
	return repliedByExcept(replies, peerID, "")
}

// repliedByExcept is repliedBy ignoring the reply exceptID (our own bounty
// ask, which is not an answer).
func repliedByExcept(replies []autopilotReply, peerID, exceptID string) string {
	if peerID == "" {
		return ""
	}
	for _, r := range replies {
		if r.Author == peerID && (exceptID == "" || r.ID != exceptID) {
			return r.ID
		}
	}
	return ""
}

// equalThresholdPins compares two per-category pin maps.
func equalThresholdPins(a, b map[string]float64) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

func (s *Server) newAutopilotSuggestion(post autopilotPost, draft string, cost int, now time.Time) autopilotSuggestion {
	return autopilotSuggestion{
		ID:               newAutopilotID(),
		PostID:           post.ID,
		PostTitle:        post.Title,
		PostAuthorName:   post.authorName(),
		Category:         post.Category,
		Bounty:           post.Bounty,
		Draft:            draft,
		EstimatedCredits: cost,
		CreatedAt:        now,
		ExpiresAt:        now.Add(autopilotSuggestionTTL),
	}
}

func newAutopilotID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("sg-%d", time.Now().UnixNano())
	}
	return "sg-" + hex.EncodeToString(b[:])
}

// autopilotBackOff pauses the watcher for autopilotBackoff and records why.
func (s *Server) autopilotBackOff(now time.Time, reason string) {
	st := s.autopilotSt()
	full := reason + "; paused for " + autopilotBackoff.String()
	st.mu.Lock()
	st.backoffUntil = now.Add(autopilotBackoff)
	st.downgradedReason = full
	st.mu.Unlock()
	s.autopilotWarn("backoff", full)
}

// autopilotTrackerCapReached records the tracker's AUTO_REPLY_LIMIT answer:
// no auto post is tried again before the next UTC midnight. Not a backoff.
func (s *Server) autopilotTrackerCapReached(now time.Time) {
	st := s.autopilotSt()
	until := autopilotNextUTCMidnight(now)
	st.mu.Lock()
	st.trackerCapUntil = until
	st.downgradedReason = autopilotTrackerCapReason(until)
	st.mu.Unlock()
	s.autopilotWarn("tracker_cap", st.downgradedReason)
}

// autopilotNextUTCMidnight is the start of the next UTC day after now.
func autopilotNextUTCMidnight(now time.Time) time.Time {
	y, m, d := now.UTC().Date()
	return time.Date(y, m, d+1, 0, 0, 0, 0, time.UTC)
}

// autopilotTrackerCapReason is the status text while the tracker's cap holds.
func autopilotTrackerCapReason(until time.Time) string {
	return "tracker daily reply cap reached, resumes at " + until.UTC().Format(time.RFC3339)
}

func (s *Server) setAutopilotDowngraded(reason string) {
	st := s.autopilotSt()
	st.mu.Lock()
	st.downgradedReason = reason
	st.mu.Unlock()
}

func (s *Server) autopilotLog(method string, fields map[string]interface{}) {
	if s.logger != nil {
		s.logger.Info("Autopilot", method, "autopilot decision", fields)
	}
}

func (s *Server) autopilotWarn(method, msg string) {
	if s.logger != nil {
		s.logger.Warn("Autopilot", method, msg, nil)
	}
}
