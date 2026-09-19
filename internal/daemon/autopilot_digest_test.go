// Package: internal/daemon
// Purpose: Tests for the Autopilot phase 2 pieces: the weekly digest scheduler
// (time logic and the run), routed-first ordering in the watcher, the
// heartbeat's autopilot_categories and the digest DTO round trip.

package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stonkagents/agent/internal/config"
)

// --- scheduler time logic --------------------------------------------

func TestAutopilotDigestDue_Table(t *testing.T) {
	// 2026-09-14 is a Monday; ISO week 2026-W38 runs Mon 14 to Sun 20.
	at := func(day, hour, min int) time.Time { return time.Date(2026, 9, day, hour, min, 0, 0, time.UTC) }
	mon9 := config.AutopilotDigest{Enabled: true, Weekday: 1, Hour: 9}
	sun0 := config.AutopilotDigest{Enabled: true, Weekday: 0, Hour: 0}
	sat23 := config.AutopilotDigest{Enabled: true, Weekday: 6, Hour: 23}
	cases := []struct {
		name     string
		d        config.AutopilotDigest
		now      time.Time
		lastWeek string
		week     string
		due      bool
	}{
		{"disabled", config.AutopilotDigest{Enabled: false, Weekday: 1, Hour: 9}, at(15, 12, 0), "", "2026-W38", false},
		{"monday before the hour", mon9, at(14, 8, 59), "", "2026-W38", false},
		{"monday at the hour", mon9, at(14, 9, 0), "", "2026-W38", true},
		{"tuesday, still this week", mon9, at(15, 12, 0), "", "2026-W38", true},
		{"sunday night, still this week", mon9, at(20, 23, 59), "", "2026-W38", true},
		{"already done this week", mon9, at(15, 12, 0), "2026-W38", "2026-W38", false},
		{"done last week, new week begins", mon9, at(21, 9, 0), "2026-W38", "2026-W39", true},
		{"new week before the hour", mon9, at(21, 8, 0), "2026-W38", "2026-W39", false},
		{"sunday schedule sits at the end of the ISO week", sun0, at(19, 23, 0), "", "2026-W38", false},
		{"sunday schedule due on sunday", sun0, at(20, 0, 0), "", "2026-W38", true},
		{"saturday 23 on saturday 22", sat23, at(19, 22, 0), "", "2026-W38", false},
		{"saturday 23 on saturday 23", sat23, at(19, 23, 0), "", "2026-W38", true},
		{"iso year boundary", mon9, time.Date(2027, 1, 1, 12, 0, 0, 0, time.UTC), "", "2026-W53", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			week, due := autopilotDigestDue(tc.d, tc.now, tc.lastWeek)
			if week != tc.week || due != tc.due {
				t.Fatalf("got %s/%v, want %s/%v", week, due, tc.week, tc.due)
			}
		})
	}

	// The hour is read in the clock's own zone: 09:00 Stockholm is 07:00 UTC.
	sto, _ := time.LoadLocation("Europe/Stockholm")
	if _, due := autopilotDigestDue(mon9, time.Date(2026, 9, 14, 8, 30, 0, 0, sto), ""); due {
		t.Error("08:30 local must not be due")
	}
	if _, due := autopilotDigestDue(mon9, time.Date(2026, 9, 14, 9, 0, 0, 0, sto), ""); !due {
		t.Error("09:00 local must be due")
	}
}

func TestAutopilotDigest_TitleAndPeriod(t *testing.T) {
	data := autopilotDigestData{PeriodStart: "2026-09-08T00:00:00Z", PeriodEnd: "2026-09-14T23:59:59Z"}
	if got := autopilotDigestPeriod(data, apNow); got != "Sep 8 to Sep 14" {
		t.Errorf("period = %q", got)
	}
	if got := autopilotDigestPeriod(autopilotDigestData{}, apNow); got != "Sep 8 to Sep 14" {
		t.Errorf("fallback period = %q", got)
	}
	if got := autopilotDigestTitle("knots", "Sep 8 to Sep 14"); got != "KNOTS weekly digest, Sep 8 to Sep 14" {
		t.Errorf("title = %q", got)
	}
	holders := 12
	msgs := autopilotDigestPrompt(policyWith(config.AutopilotOff, func(p *config.AutopilotConfig) { p.Instruction = "Keep it upbeat." }),
		autopilotRoom{Mint: "M", Symbol: "KNOTS", Name: "Knots"},
		autopilotDigestData{Posts: 4, Replies: 9, BountiesAward: 1, CreditsAwarded: 250, NewHolders: &holders,
			TopThreads: []autopilotDigestThread{{ID: "t1", Title: "Best thread", Upvotes: 7, Replies: 3}}},
		"Sep 8 to Sep 14", "Atlas Prime")
	sys, usr := msgs[0]["content"], msgs[1]["content"]
	if !strings.Contains(sys, "Atlas Prime") || !strings.Contains(sys, "KNOTS") || !strings.Contains(sys, "Keep it upbeat.") {
		t.Errorf("system = %q", sys)
	}
	for _, want := range []string{"Posts: 4", "Replies: 9", "Bounties awarded: 1", "Credits awarded: 250", "New holders: 12", "Best thread (7 upvotes, 3 replies)", "Period: Sep 8 to Sep 14"} {
		if !strings.Contains(usr, want) {
			t.Errorf("user prompt missing %q: %q", want, usr)
		}
	}
}

// --- digest run --------------------------------------------------------

func digestPolicy(mode string, weekday, hour int) config.AutopilotConfig {
	return policyWith(mode, func(p *config.AutopilotConfig) {
		p.Digest = config.AutopilotDigest{Enabled: true, Weekday: weekday, Hour: hour}
	})
}

func withRoom(fb *fakeBoard, role string) {
	fb.rooms = []autopilotRoom{{Mint: "MintKNOTS111", Symbol: "knots", Name: "Knots", Role: role}}
	fb.digest = autopilotDigestData{Posts: 3, Replies: 5, PeriodStart: "2026-09-08T00:00:00Z", PeriodEnd: "2026-09-14T23:59:59Z",
		TopThreads: []autopilotDigestThread{{ID: "t1", Title: "Roadmap", Upvotes: 4, Replies: 2}}}
}

func TestAutopilotRun_DigestPostsOncePerWeek(t *testing.T) {
	trk, fb := newFakeBoard(t)
	fb.set(func(f *fakeBoard) { withRoom(f, "agent") })
	// Mode off: the digest runs on its own switch. apNow is Tuesday 12:00 UTC, past Monday 09:00.
	s := newAutopilotTestServer(t, trk.URL, digestPolicy(config.AutopilotOff, 1, 9))
	res := s.runAutopilotOnce(context.Background())
	if res.Skipped != "mode_off" || res.Digest.Skipped != "" || res.Digest.Week != "2026-W38" || res.Digest.PostID != "post-1" || res.Digest.Credits != 10 {
		t.Fatalf("run = %+v digest = %+v", res, res.Digest)
	}
	if fb.draftCount() != 1 || fb.postCount() != 1 {
		t.Fatalf("drafts=%d posts=%d", fb.draftCount(), fb.postCount())
	}
	body := fb.postBodies[0]
	title := "KNOTS weekly digest, Sep 8 to Sep 14"
	// title is its own field; the body is the draft alone (a repeated title line
	// used to show twice in the thread).
	if body["title"] != title || body["category"] != "general" || body["room_mint"] != "MintKNOTS111" || body["auto"] != true ||
		body["body"] != "I can help with that. Send me the spec." || strings.Contains(body["body"].(string), title) {
		t.Errorf("post body = %v", body)
	}
	if h := fb.postHeaders[0]; h.Get(AutopilotAutoHeader) != "1" || h.Get("X-API-Key") != apPeer {
		t.Errorf("post headers = %v", h)
	}
	if !strings.Contains(fb.drafts[0]["messages"].([]interface{})[0].(map[string]interface{})["content"].(string), "Never use em dashes or en dashes") {
		t.Error("digest prompt must forbid em and en dashes")
	}
	// The draft went through the same credit-gated Fast path as a suggestion.
	if _, has := fb.drafts[0]["model"]; has {
		t.Error("digest draft must use the Fast model")
	}
	if fb.draftHeaders[0].Get(AutopilotAutoHeader) != "1" {
		t.Errorf("digest draft must be marked auto: %v", fb.draftHeaders[0])
	}
	if usr := fb.drafts[0]["messages"].([]interface{})[1].(map[string]interface{})["content"].(string); !strings.Contains(usr, "Roadmap (4 upvotes, 2 replies)") {
		t.Errorf("digest prompt = %q", usr)
	}
	st := s.autopilotSt().store
	if st.digestLastWeek() != "2026-W38" {
		t.Errorf("digestLastWeek = %q", st.digestLastWeek())
	}
	if _, credits := st.counters(); credits != 10 {
		t.Errorf("credits = %d", credits)
	}
	// Local event for the bell, and the status field.
	w := do(s, setupRequest(http.MethodGet, "/api/v1/setup/autopilot/events", ""))
	if w.Code != http.StatusOK {
		t.Fatalf("events = %d %s", w.Code, w.Body.String())
	}
	var events autopilotEventsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &events); err != nil || len(events.Data) != 1 {
		t.Fatalf("events body = %s err=%v", w.Body.String(), err)
	}
	ev := events.Data[0]
	if ev.Kind != "digest_posted" || ev.PostID != "post-1" || ev.Mint != "MintKNOTS111" || ev.Symbol != "KNOTS" || ev.Title != title || !ev.CreatedAt.Equal(apNow) || ev.ID == "" {
		t.Errorf("event = %+v", ev)
	}
	for _, key := range []string{`"id"`, `"kind":"digest_posted"`, `"createdAt":"2026-09-15T12:00:00Z"`, `"postId":"post-1"`, `"mint":"MintKNOTS111"`, `"symbol":"KNOTS"`, `"title"`} {
		if !strings.Contains(w.Body.String(), key) {
			t.Errorf("events missing %s in %s", key, w.Body.String())
		}
	}
	if strings.Contains(w.Body.String(), `"credits"`) || strings.Contains(w.Body.String(), `"roomMint"`) {
		t.Errorf("events carry keys outside the contract: %s", w.Body.String())
	}
	if dto := s.autopilotDTOFor(); dto.Status.DigestLastPostedAt != apNow.Format(time.RFC3339) || dto.Status.NextDigestAt != "2026-09-21T09:00:00Z" {
		t.Errorf("status = %+v", dto.Status)
	}

	// Same week, later: nothing more.
	st2 := s.autopilotSt()
	st2.now = func() time.Time { return apNow.Add(3 * 24 * time.Hour) }
	if res := s.runAutopilotOnce(context.Background()); res.Digest.Skipped != "not_due" || fb.postCount() != 1 {
		t.Errorf("same week = %+v posts=%d", res.Digest, fb.postCount())
	}
	// Persisted: a restart over the same data dir still knows the week.
	s3 := newAutopilotTestServer(t, trk.URL, digestPolicy(config.AutopilotOff, 1, 9))
	s3.config.DataDir = s.config.DataDir
	if got := s3.autopilotSt().store.digestLastWeek(); got != "2026-W38" {
		t.Errorf("week lost across restart: %q", got)
	}
	if got := s3.autopilotSt().store.events(); len(got) != 1 {
		t.Errorf("events lost across restart: %+v", got)
	}
	// Next Monday 09:00: due again.
	st2.now = func() time.Time { return time.Date(2026, 9, 21, 9, 0, 0, 0, time.UTC) }
	if res := s.runAutopilotOnce(context.Background()); res.Digest.Skipped != "" || res.Digest.Week != "2026-W39" || fb.postCount() != 2 {
		t.Errorf("next week = %+v posts=%d", res.Digest, fb.postCount())
	}
}

func TestAutopilotRun_DigestGates(t *testing.T) {
	trk, fb := newFakeBoard(t)

	// Disabled: nothing touched.
	s := newAutopilotTestServer(t, trk.URL, policyWith(config.AutopilotOff))
	if res := s.runAutopilotOnce(context.Background()); res.Digest.Skipped != "disabled" || len(fb.calls) != 0 {
		t.Errorf("disabled = %+v calls=%v", res.Digest, fb.calls)
	}

	// Not yet the scheduled hour: no tracker call either.
	s = newAutopilotTestServer(t, trk.URL, digestPolicy(config.AutopilotOff, 2, 13)) // Tuesday 13:00, apNow is Tuesday 12:00
	if res := s.runAutopilotOnce(context.Background()); res.Digest.Skipped != "not_due" || len(fb.calls) != 0 {
		t.Errorf("not due = %+v calls=%v", res.Digest, fb.calls)
	}

	// No bound token (holder only): skipped, nothing drafted, week not marked,
	// and rooms?mine=1 is not asked again for an hour (it costs the tracker RPC).
	fb.set(func(f *fakeBoard) { withRoom(f, "holder"); f.roomsCalls = 0 })
	s = newAutopilotTestServer(t, trk.URL, digestPolicy(config.AutopilotOff, 1, 9))
	if res := s.runAutopilotOnce(context.Background()); res.Digest.Skipped != "no_bound_token" || fb.draftCount() != 0 || s.autopilotSt().store.digestLastWeek() != "" {
		t.Errorf("holder = %+v", res.Digest)
	}
	stH := s.autopilotSt()
	for i := 1; i <= 11; i++ {
		stH.now = func() time.Time { return apNow.Add(time.Duration(i) * 5 * time.Minute) }
		if res := s.runAutopilotOnce(context.Background()); res.Digest.Skipped != "no_bound_token" {
			t.Fatalf("tick %d = %+v", i, res.Digest)
		}
	}
	if fb.roomsCalls != 1 {
		t.Errorf("rooms asked %d times within the hour, want 1", fb.roomsCalls)
	}
	stH.now = func() time.Time { return apNow.Add(61 * time.Minute) }
	if res := s.runAutopilotOnce(context.Background()); res.Digest.Skipped != "no_bound_token" || fb.roomsCalls != 2 {
		t.Errorf("after an hour = %+v rooms=%d", res.Digest, fb.roomsCalls)
	}
	// Once the token is bound, the next check after the hour posts.
	fb.set(func(f *fakeBoard) { withRoom(f, "agent") })
	stH.now = func() time.Time { return apNow.Add(3 * time.Hour) }
	if res := s.runAutopilotOnce(context.Background()); res.Digest.Skipped != "" || fb.roomsCalls != 3 || fb.postCount() != 1 {
		t.Errorf("after claim = %+v rooms=%d posts=%d", res.Digest, fb.roomsCalls, fb.postCount())
	}
	fb.set(func(f *fakeBoard) { f.postBodies, f.drafts = nil, nil })

	// Balance floor blocks the draft: skipped with a reason and retried later (week not marked).
	fb.set(func(f *fakeBoard) { withRoom(f, "agent"); f.balance = 29 }) // 29 - 10 < floor 20
	s = newAutopilotTestServer(t, trk.URL, digestPolicy(config.AutopilotOff, 1, 9))
	if res := s.runAutopilotOnce(context.Background()); res.Digest.Skipped != "balance_floor" || fb.draftCount() != 0 || s.autopilotSt().store.digestLastWeek() != "" {
		t.Errorf("floor = %+v drafts=%d", res.Digest, fb.draftCount())
	}
	fb.set(func(f *fakeBoard) { f.balance = 30 })
	if res := s.runAutopilotOnce(context.Background()); res.Digest.Skipped != "" || fb.postCount() != 1 {
		t.Errorf("floor cleared = %+v", res.Digest)
	}

	// Daily credit cap already spent: skipped before any tracker call.
	fb.set(func(f *fakeBoard) { f.calls = nil })
	s = newAutopilotTestServer(t, trk.URL, digestPolicy(config.AutopilotOff, 1, 9))
	_ = s.autopilotSt().store.addCredits(25)
	if res := s.runAutopilotOnce(context.Background()); res.Digest.Skipped != "daily_credit_cap" || len(fb.calls) != 0 {
		t.Errorf("credit cap = %+v calls=%v", res.Digest, fb.calls)
	}

	// Post refused after a paid draft: the week is done (no second draft), no event.
	fb.set(func(f *fakeBoard) { f.postStatus = http.StatusForbidden; f.postBodies = nil; f.drafts = nil })
	s = newAutopilotTestServer(t, trk.URL, digestPolicy(config.AutopilotOff, 1, 9))
	if res := s.runAutopilotOnce(context.Background()); res.Digest.Skipped != "post_failed" || res.Digest.Credits != 10 || fb.draftCount() != 1 {
		t.Errorf("post failed = %+v", res.Digest)
	}
	if got := s.autopilotSt().store.digestLastWeek(); got != "2026-W38" {
		t.Errorf("week after failed post = %q", got)
	}
	if got := s.autopilotSt().store.events(); len(got) != 0 {
		t.Errorf("event on failed post: %+v", got)
	}
	if res := s.runAutopilotOnce(context.Background()); res.Digest.Skipped != "not_due" || fb.draftCount() != 1 {
		t.Errorf("retry after failed post drafted again: %+v", res.Digest)
	}

	// 402 on the draft backs the whole watcher off; the week stays open for after the pause.
	fb.set(func(f *fakeBoard) { f.postStatus = 0; f.draftStatus = http.StatusPaymentRequired; f.drafts = nil })
	s = newAutopilotTestServer(t, trk.URL, digestPolicy(config.AutopilotSuggest, 1, 9))
	res := s.runAutopilotOnce(context.Background())
	if res.Digest.Skipped != "backoff" || res.Skipped != "backoff" || s.autopilotSt().store.digestLastWeek() != "" {
		t.Errorf("402 = %+v / %+v", res.Digest, res)
	}
}

// --- routed-first ordering --------------------------------------------

func TestAutopilotRun_RoutedPostsBeforeFeed(t *testing.T) {
	trk, fb := newFakeBoard(t)
	fb.set(func(f *fakeBoard) {
		f.posts = []autopilotPost{
			post("f1", apOther, "request", 1),
			post("r1", apOther, "request", 1), // also routed: examined once, first
			post("f2", apOther, "request", 1),
		}
		f.byID["r2"] = post("r2", apOther, "request", 3, withBounty(50, "open")) // routed, not on the feed page
		f.routedIDs = []string{"r2", "r1", "gone"}
	})
	// Auto mode, one reply per day: the reply must land on the first routed post.
	s := newAutopilotTestServer(t, trk.URL, policyWith(config.AutopilotAuto, func(p *config.AutopilotConfig) { p.MaxRepliesPerDay = 1 }))
	res := s.runAutopilotOnce(context.Background())
	// r2 is answered; r1 is examined next and stops the loop on the cap.
	if res.Skipped != "" || res.Posted != 1 || res.Examined != 2 || res.Routed != 2 || res.StoppedFor != "daily_reply_cap" {
		t.Fatalf("run = %+v", res)
	}
	if fb.replyPostIDs[0] != "r2" {
		t.Errorf("first reply went to %s, want the routed r2", fb.replyPostIDs[0])
	}
	// Activity was read before the feed, and the routed posts were fetched by id.
	act, feed := fb.callIndex("GET /api/activity"), fb.callIndex("GET /api/board/posts")
	if act < 0 || feed < 0 || act > feed {
		t.Errorf("call order = %v", fb.calls)
	}
	if fb.callIndex("GET /api/board/posts/r2") < 0 || fb.callIndex("GET /api/board/posts/r1") < 0 {
		t.Errorf("routed posts not fetched by id: %v", fb.calls)
	}
	// Read marks: r2 was answered (read), "gone" is unfetchable (read), r1 was
	// stopped by the cap (still unread, comes back next tick).
	if got := strings.Join(fb.readIDs, ","); got != "a-gone,a-r2" || res.RoutedRead != 1 {
		t.Errorf("read ids = %q routedRead=%d", got, res.RoutedRead)
	}
	if res := s.runAutopilotOnce(context.Background()); res.Skipped != "daily_reply_cap" {
		t.Fatalf("second run = %+v", res)
	}
	fb.set(func(f *fakeBoard) { f.calls = nil })
	sNext := newAutopilotTestServer(t, trk.URL, policyWith(config.AutopilotSuggest))
	sNext.config.DataDir = s.config.DataDir
	if res := sNext.runAutopilotOnce(context.Background()); res.Routed != 1 || fb.callIndex("GET /api/board/posts/r1") < 0 || fb.callIndex("GET /api/board/posts/r2") >= 0 {
		t.Errorf("next tick must see only the unread r1: %+v calls=%v", res, fb.calls)
	}
	if got := strings.Join(fb.readIDs, ","); got != "a-gone,a-r2,a-r1" {
		t.Errorf("read ids after next tick = %q", got)
	}

	// Suggest mode, plenty of budget: order is r2, r1, f1, f2 and r1 is examined once.
	fb.set(func(f *fakeBoard) {
		f.replies = map[string][]autopilotReply{}
		f.drafts, f.calls, f.repliesCalled, f.readIDs = nil, nil, nil, nil
	})
	s = newAutopilotTestServer(t, trk.URL, policyWith(config.AutopilotSuggest, func(p *config.AutopilotConfig) { p.DailyCreditCap = 1000 }))
	res = s.runAutopilotOnce(context.Background())
	if res.Examined != 4 || res.Routed != 2 || res.Suggested != 4 {
		t.Fatalf("suggest run = %+v", res)
	}
	if got := strings.Join(fb.repliesCalled, ","); got != "r2,r1,f1,f2" {
		t.Errorf("examined order = %s", got)
	}

	// Routed posts obey the same gates: a routed post in an unticked category is
	// skipped, and that skip is final (marked read); a cooldown skip is not.
	fb.set(func(f *fakeBoard) {
		f.posts = nil
		f.byID["g1"] = post("g1", apOther, "general", 1)
		f.byID["c1"] = post("c1", apOther, "request", 1)
		f.routedIDs = []string{"g1", "c1"}
		f.drafts, f.readIDs = nil, nil
	})
	s = newAutopilotTestServer(t, trk.URL, policyWith(config.AutopilotSuggest))
	_ = s.autopilotSt().store.touch("c1") // acted on just now: cooldown
	if res := s.runAutopilotOnce(context.Background()); res.Examined != 2 || res.Routed != 2 || res.Suggested != 0 || res.RoutedRead != 1 || fb.draftCount() != 0 {
		t.Errorf("gated routed posts = %+v", res)
	}
	if got := strings.Join(fb.readIDs, ","); got != "a-g1" {
		t.Errorf("read ids = %q, want only the no_trigger skip", got)
	}
	// Budget gone before the routed post: stays unread.
	fb.set(func(f *fakeBoard) {
		f.byID["b1"] = post("b1", apOther, "request", 1)
		f.routedIDs = []string{"b1"}
		f.readIDs = nil
	})
	s = newAutopilotTestServer(t, trk.URL, policyWith(config.AutopilotSuggest))
	_ = s.autopilotSt().store.addCredits(25) // cap 30: one draft would exceed it
	if res := s.runAutopilotOnce(context.Background()); res.Skipped != "daily_credit_cap" || len(fb.readIDs) != 0 {
		t.Errorf("credit cap run = %+v read=%v", res, fb.readIDs)
	}
	// A read-route failure never breaks the run.
	s = newAutopilotTestServer(t, trk.URL, policyWith(config.AutopilotSuggest))
	fb.set(func(f *fakeBoard) { f.readStatus = http.StatusInternalServerError; f.drafts = nil })
	if res := s.runAutopilotOnce(context.Background()); res.Suggested != 1 || res.RoutedRead != 0 {
		t.Errorf("read failure run = %+v", res)
	}
	fb.set(func(f *fakeBoard) { f.readStatus = 0 })

	// A tracker without the routed filter (400) or an unreachable activity route: feed only.
	fb.set(func(f *fakeBoard) {
		f.posts = []autopilotPost{post("f1", apOther, "request", 1)}
		f.activityStatus = http.StatusBadRequest
		f.drafts = nil
	})
	s = newAutopilotTestServer(t, trk.URL, policyWith(config.AutopilotSuggest))
	if res := s.runAutopilotOnce(context.Background()); res.Skipped != "" || res.Examined != 1 || res.Routed != 0 || res.Suggested != 1 {
		t.Errorf("activity 400 = %+v", res)
	}
}

// --- heartbeat autopilot_categories ------------------------------------

func TestAutopilotHeartbeatCategories(t *testing.T) {
	trk, _ := newFakeBoard(t)
	cases := []struct {
		name   string
		policy config.AutopilotConfig
		want   []string // nil = field omitted
	}{
		{"off omits", policyWith(config.AutopilotOff, func(p *config.AutopilotConfig) { p.Categories = []string{"request", "general"} }), nil},
		{"suggest sends ticked plus bounty", policyWith(config.AutopilotSuggest, func(p *config.AutopilotConfig) { p.Categories = []string{"general", "request"} }), []string{"bounty", "general", "request"}},
		{"auto with no categories still sends bounty", policyWith(config.AutopilotAuto, func(p *config.AutopilotConfig) { p.Categories = []string{} }), []string{"bounty"}},
		{"token-offer", policyWith(config.AutopilotBounty, func(p *config.AutopilotConfig) { p.Categories = []string{"token-offer"} }), []string{"bounty", "token-offer"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newAutopilotTestServer(t, trk.URL, tc.policy)
			got := s.autopilotHeartbeatCategories()
			if (got == nil) != (tc.want == nil) || strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("got %#v, want %#v", got, tc.want)
			}
		})
	}
	// A live policy change is picked up by the next heartbeat.
	s := newAutopilotTestServer(t, trk.URL, policyWith(config.AutopilotOff))
	if got := s.autopilotHeartbeatCategories(); got != nil {
		t.Fatalf("off = %v", got)
	}
	_ = autopilotData(t, do(s, setupRequest(http.MethodPost, "/api/v1/setup/autopilot", `{"mode":"suggest","categories":["token-offer"]}`)))
	if got := s.autopilotHeartbeatCategories(); strings.Join(got, ",") != "bounty,token-offer" {
		t.Errorf("after enable = %v", got)
	}
}

// --- DTO round trip -------------------------------------------------------

func TestSetupAutopilot_DigestDTORoundTrip(t *testing.T) {
	trk, _ := newFakeBoard(t)
	s := newAutopilotTestServer(t, trk.URL, policyWith(config.AutopilotOff))

	// Defaults on the wire.
	w := do(s, setupRequest(http.MethodGet, "/api/v1/setup/autopilot", ""))
	got := autopilotData(t, w)
	if got.Policy.Digest.Enabled || got.Policy.Digest.Weekday != 1 || got.Policy.Digest.Hour != 9 || got.Status.DigestLastPostedAt != "" {
		t.Errorf("defaults = %+v status=%+v", got.Policy.Digest, got.Status)
	}
	if !strings.Contains(w.Body.String(), `"digest":{"enabled":false,"weekday":1,"hour":9}`) {
		t.Errorf("wire shape = %s", w.Body.String())
	}

	// Full object persists to config.yaml and applies live.
	got = autopilotData(t, do(s, setupRequest(http.MethodPost, "/api/v1/setup/autopilot", `{"digest":{"enabled":true,"weekday":5,"hour":18}}`)))
	if !got.Policy.Digest.Enabled || got.Policy.Digest.Weekday != 5 || got.Policy.Digest.Hour != 18 || got.Policy.Mode != "off" {
		t.Errorf("post = %+v", got.Policy)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Autopilot.Digest != (config.AutopilotDigest{Enabled: true, Weekday: 5, Hour: 18}) {
		t.Errorf("config.yaml digest = %+v", cfg.Autopilot.Digest)
	}
	if s.autopilotPolicy().Digest.Hour != 18 {
		t.Error("digest not applied live")
	}

	// Partial digest keeps the rest; an unrelated patch keeps the digest.
	got = autopilotData(t, do(s, setupRequest(http.MethodPost, "/api/v1/setup/autopilot", `{"digest":{"hour":7}}`)))
	if !got.Policy.Digest.Enabled || got.Policy.Digest.Weekday != 5 || got.Policy.Digest.Hour != 7 {
		t.Errorf("partial digest = %+v", got.Policy.Digest)
	}
	got = autopilotData(t, do(s, setupRequest(http.MethodPost, "/api/v1/setup/autopilot", `{"maxRepliesPerDay":4}`)))
	if !got.Policy.Digest.Enabled || got.Policy.Digest.Weekday != 5 || got.Policy.Digest.Hour != 7 || got.Policy.MaxRepliesPerDay != 4 {
		t.Errorf("unrelated patch = %+v", got.Policy)
	}
	// Explicit Sunday midnight survives the round trip.
	got = autopilotData(t, do(s, setupRequest(http.MethodPost, "/api/v1/setup/autopilot", `{"digest":{"weekday":0,"hour":0}}`)))
	if got.Policy.Digest.Weekday != 0 || got.Policy.Digest.Hour != 0 || !got.Policy.Digest.Enabled {
		t.Errorf("sunday midnight = %+v", got.Policy.Digest)
	}
	cfg, _ = config.Load()
	if cfg.Autopilot.Digest.Weekday != 0 || cfg.Autopilot.Digest.Hour != 0 {
		t.Errorf("config.yaml sunday midnight = %+v", cfg.Autopilot.Digest)
	}

	// Validation.
	for name, body := range map[string]string{
		"weekday 7":   `{"digest":{"weekday":7}}`,
		"hour 24":     `{"digest":{"hour":24}}`,
		"hour -1":     `{"digest":{"hour":-1}}`,
		"not object":  `{"digest":"monday"}`,
		"bad enabled": `{"digest":{"enabled":"yes"}}`,
	} {
		if w := do(s, setupRequest(http.MethodPost, "/api/v1/setup/autopilot", body)); w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d body=%s", name, w.Code, w.Body.String())
		}
	}
	if p := s.autopilotPolicy().Digest; p.Weekday != 0 || p.Hour != 0 {
		t.Errorf("rejected patch changed the policy: %+v", p)
	}

	// Events route: empty list, loopback only.
	w = do(s, setupRequest(http.MethodGet, "/api/v1/setup/autopilot/events", ""))
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != `{"data":[]}` {
		t.Errorf("events = %d %s", w.Code, w.Body.String())
	}
	req := setupRequest(http.MethodGet, "/api/v1/setup/autopilot/events", "")
	req.RemoteAddr = "203.0.113.9:4444"
	if w := do(s, req); w.Code != http.StatusForbidden {
		t.Errorf("events from remote = %d", w.Code)
	}
}

// --- events route -----------------------------------------------------------

func TestSetupAutopilot_EventsHandler(t *testing.T) {
	trk, _ := newFakeBoard(t)
	s := newAutopilotTestServer(t, trk.URL, policyWith(config.AutopilotOff))
	st := s.autopilotSt().store

	// Empty list is [] not null.
	w := do(s, setupRequest(http.MethodGet, "/api/v1/setup/autopilot/events", ""))
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != `{"data":[]}` {
		t.Fatalf("empty = %d %s", w.Code, w.Body.String())
	}

	// 55 appended: the route answers the newest 50, newest first, exact wire keys.
	for i := 0; i < 55; i++ {
		ev := autopilotEvent{ID: fmt.Sprintf("ev-%02d", i), Kind: "digest_posted", CreatedAt: apNow.Add(time.Duration(i) * time.Hour),
			PostID: fmt.Sprintf("p-%d", i), Mint: "Mint1", Symbol: "KNOTS", Title: fmt.Sprintf("KNOTS weekly digest %d", i)}
		if err := st.addEvent(ev); err != nil {
			t.Fatal(err)
		}
	}
	w = do(s, setupRequest(http.MethodGet, "/api/v1/setup/autopilot/events", ""))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data []map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Data) != 50 || resp.Data[0]["id"] != "ev-54" || resp.Data[49]["id"] != "ev-05" {
		t.Errorf("order/cap wrong: n=%d first=%v last=%v", len(resp.Data), resp.Data[0]["id"], resp.Data[len(resp.Data)-1]["id"])
	}
	first := resp.Data[0]
	want := map[string]interface{}{"id": "ev-54", "kind": "digest_posted", "createdAt": apNow.Add(54 * time.Hour).Format(time.RFC3339),
		"postId": "p-54", "mint": "Mint1", "symbol": "KNOTS", "title": "KNOTS weekly digest 54"}
	if len(first) != len(want) {
		t.Errorf("keys = %v, want exactly %v", first, want)
	}
	for k, v := range want {
		if first[k] != v {
			t.Errorf("%s = %v, want %v", k, first[k], v)
		}
	}

	// Persisted: a fresh store over the same file sees the same 50.
	st2 := newAutopilotStore(s.config.DataDir, func() time.Time { return apNow })
	if err := st2.load(); err != nil {
		t.Fatal(err)
	}
	if got := st2.events(); len(got) != 50 || got[0].ID != "ev-54" {
		t.Errorf("reload = %d first=%v", len(got), got)
	}

	// Loopback only, like the other setup GETs.
	req := setupRequest(http.MethodGet, "/api/v1/setup/autopilot/events", "")
	req.RemoteAddr = "203.0.113.9:4444"
	if w := do(s, req); w.Code != http.StatusForbidden {
		t.Errorf("remote = %d", w.Code)
	}
}

// --- enable after the slot, nextDigestAt, dash scrub ---------------------

func TestAutopilotDigest_NextAtAndWeekToSkip(t *testing.T) {
	mon9 := config.AutopilotDigest{Enabled: true, Weekday: 1, Hour: 9}
	off := config.AutopilotDigest{Enabled: false, Weekday: 1, Hour: 9}
	fri18 := config.AutopilotDigest{Enabled: true, Weekday: 5, Hour: 18}
	at := func(day, hour int) time.Time { return time.Date(2026, 9, day, hour, 0, 0, 0, time.UTC) }

	next := []struct {
		name     string
		d        config.AutopilotDigest
		now      time.Time
		lastWeek string
		want     time.Time
	}{
		{"off", off, at(15, 12), "", time.Time{}},
		{"before this week's slot", mon9, at(14, 8), "", at(14, 9)},
		{"past the slot, still open: pending now", mon9, at(15, 12), "", at(14, 9)},
		{"done this week: next week", mon9, at(15, 12), "2026-W38", at(21, 9)},
		{"friday slot later this week", fri18, at(15, 12), "", at(18, 18)},
		{"friday slot done: next friday", fri18, at(19, 12), "2026-W38", at(25, 18)},
	}
	for _, tc := range next {
		t.Run("next/"+tc.name, func(t *testing.T) {
			if got := autopilotNextDigestAt(tc.d, tc.now, tc.lastWeek); !got.Equal(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}

	skip := []struct {
		name     string
		old, upd config.AutopilotDigest
		now      time.Time
		lastWeek string
		want     string
	}{
		{"enable after the slot", off, mon9, at(15, 12), "", "2026-W38"},
		{"enable before the slot", off, mon9, at(14, 8), "", ""},
		{"enable later slot", off, fri18, at(15, 12), "", ""},
		{"stays off", off, off, at(15, 12), "", ""},
		{"no change", mon9, mon9, at(15, 12), "", ""},
		{"move slot into the past", fri18, mon9, at(16, 12), "", "2026-W38"},
		{"move slot into the future", mon9, fri18, at(16, 12), "", ""},
		{"already done", off, mon9, at(15, 12), "2026-W38", ""},
		{"disable", mon9, off, at(15, 12), "", ""},
	}
	for _, tc := range skip {
		t.Run("skip/"+tc.name, func(t *testing.T) {
			if got := autopilotDigestWeekToSkip(tc.old, tc.upd, tc.now, tc.lastWeek); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSetupAutopilot_EnableDigestAfterSlotWaitsForNextWeek(t *testing.T) {
	trk, fb := newFakeBoard(t)
	fb.set(func(f *fakeBoard) { withRoom(f, "agent") })
	s := newAutopilotTestServer(t, trk.URL, policyWith(config.AutopilotOff))
	// Tuesday 12:00, Monday 09:00 already passed: enabling marks this week done.
	got := autopilotData(t, do(s, setupRequest(http.MethodPost, "/api/v1/setup/autopilot", `{"digest":{"enabled":true}}`)))
	if got.Status.NextDigestAt != "2026-09-21T09:00:00Z" {
		t.Errorf("nextDigestAt = %q", got.Status.NextDigestAt)
	}
	if s.autopilotSt().store.digestLastWeek() != "2026-W38" {
		t.Errorf("week not marked at enable time: %q", s.autopilotSt().store.digestLastWeek())
	}
	if res := s.runAutopilotOnce(context.Background()); res.Digest.Skipped != "not_due" || fb.postCount() != 0 {
		t.Errorf("first tick after enabling posted: %+v", res.Digest)
	}
	// GET carries the same value; next Monday it posts.
	if got := autopilotData(t, do(s, setupRequest(http.MethodGet, "/api/v1/setup/autopilot", ""))); got.Status.NextDigestAt != "2026-09-21T09:00:00Z" {
		t.Errorf("get nextDigestAt = %q", got.Status.NextDigestAt)
	}
	st := s.autopilotSt()
	st.now = func() time.Time { return time.Date(2026, 9, 21, 9, 5, 0, 0, time.UTC) }
	if res := s.runAutopilotOnce(context.Background()); res.Digest.Skipped != "" || fb.postCount() != 1 {
		t.Errorf("next week = %+v posts=%d", res.Digest, fb.postCount())
	}
	if got := autopilotData(t, do(s, setupRequest(http.MethodGet, "/api/v1/setup/autopilot", ""))); got.Status.NextDigestAt != "2026-09-28T09:00:00Z" {
		t.Errorf("after post nextDigestAt = %q", got.Status.NextDigestAt)
	}

	// Enabling before the slot keeps this week: Friday 18:00 on a Tuesday.
	s2 := newAutopilotTestServer(t, trk.URL, policyWith(config.AutopilotOff))
	got = autopilotData(t, do(s2, setupRequest(http.MethodPost, "/api/v1/setup/autopilot", `{"digest":{"enabled":true,"weekday":5,"hour":18}}`)))
	if got.Status.NextDigestAt != "2026-09-18T18:00:00Z" || s2.autopilotSt().store.digestLastWeek() != "" {
		t.Errorf("enable before slot: next=%q week=%q", got.Status.NextDigestAt, s2.autopilotSt().store.digestLastWeek())
	}
	// Off: no nextDigestAt.
	got = autopilotData(t, do(s2, setupRequest(http.MethodPost, "/api/v1/setup/autopilot", `{"digest":{"enabled":false}}`)))
	if got.Status.NextDigestAt != "" {
		t.Errorf("off nextDigestAt = %q", got.Status.NextDigestAt)
	}
}

func TestAutopilotScrubDashes(t *testing.T) {
	em, en := string(rune(0x2014)), string(rune(0x2013))
	in := "Quiet week " + em + " two posts, one reply" + en + "and no bounties. Range 1" + en + "3."
	got := autopilotScrubDashes(in)
	if strings.ContainsAny(got, em+en) {
		t.Errorf("dashes left: %q", got)
	}
	if got != "Quiet week, two posts, one reply, and no bounties. Range 1, 3." {
		t.Errorf("scrubbed = %q", got)
	}
	if autopilotScrubDashes("plain - hyphen") != "plain - hyphen" {
		t.Error("hyphens must stay")
	}
}
