// Package api: Community board phase 2 HTTP tests: room posting and replies (room_mint,
// ROOM_NOT_HOLDER, ROOM_UNKNOWN_MINT), room feeds and counts, the rooms list and single room
// (can_post), the room digest, the activity kinds/unread filters (reading with unread=1 marks
// read), routed_count and request_routed rows, and autopilot_categories on the heartbeat.

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/presence"
	"github.com/stonkagents/agent/tracker/internal/repository"
	"github.com/stonkagents/agent/tracker/internal/services"
)

// stubRoomMetrics serves cached holder counts per mint.
type stubRoomMetrics struct{ holders map[string]int }

func (s *stubRoomMetrics) BatchGetCachedMetrics(_ context.Context, mints []string) map[string]*models.TokenMetrics {
	out := map[string]*models.TokenMetrics{}
	for _, m := range mints {
		if n, ok := s.holders[m]; ok {
			h := n
			out[m] = &models.TokenMetrics{Holders: &h}
		}
	}
	return out
}

// p2Env is p1Env with the phase 2 deps wired into the forum service.
type p2Env struct {
	*p1Env
	holders   *services.StubTokenHolderChecker
	autopilot *repository.MemoryPeerAutopilotRepository
	presence  *presence.MemoryPresenceStore
	metrics   *stubRoomMetrics
}

func newP2Env(t *testing.T) *p2Env {
	t.Helper()
	e := newP1Env(t)
	env := &p2Env{
		p1Env:     e,
		holders:   &services.StubTokenHolderChecker{Balances: map[string]uint64{}},
		autopilot: repository.NewMemoryPeerAutopilotRepository(),
		presence:  presence.NewMemoryPresenceStore(e.clock),
		metrics:   &stubRoomMetrics{holders: map[string]int{}},
	}
	t.Cleanup(env.presence.Close)
	// The legacy daemon forum routes serve the same (rebuilt) forum service.
	e.srv.forum.svc = e.forum
	e.forum.SetRoomDeps(services.RoomDeps{
		Holders: env.holders, Metrics: env.metrics, Snapshots: repository.NewMemoryRoomHolderSnapshotRepository(),
		Autopilot: env.autopilot, Presence: env.presence,
	})
	return env
}

// room records a bound launch and returns its mint.
func (e *p2Env) room(t *testing.T, mint, symbol, agent string) string {
	t.Helper()
	l := &models.TokenLaunch{Mint: mint, Symbol: symbol, Name: symbol + " Token", ImageURL: "https://cdn.example/" + mint + ".png",
		CreatorWallet: "wallet-" + agent, LaunchSignature: "sig-" + mint, PeerID: agent, CreatedAt: e.clock.Now()}
	if agent != "" {
		l.Status = models.LaunchStatusBound
	}
	if err := e.launches.Create(context.Background(), l); err != nil {
		t.Fatalf("launch: %v", err)
	}
	return mint
}

// holds links a wallet to the peer holding amount of mint.
func (e *p2Env) holds(t *testing.T, peerID, mint string, amount uint64) {
	t.Helper()
	e.wallet(t, peerID, "wallet-"+peerID)
	e.holders.Balances["wallet-"+peerID+":"+mint] = amount
}

func (e *p2Env) seen(t *testing.T, peerID string) string {
	t.Helper()
	if err := e.peers.Upsert(context.Background(), &models.Peer{PeerID: peerID, DisplayName: peerID, FirstSeen: e.clock.Now().Add(-48 * time.Hour), LastSeen: e.clock.Now()}); err != nil {
		t.Fatalf("peer: %v", err)
	}
	return e.key(t, peerID)
}

func TestP2_RoomPostAndReplyRules(t *testing.T) {
	e := newP2Env(t)
	agent := e.named(t, "agent", "Agent")
	holder := e.named(t, "holder", "Holder")
	nobody := e.named(t, "nobody", "Nobody")
	mint := e.room(t, "MintA", "AAA", "agent")
	e.holds(t, "holder", mint, 3)

	w := e.do(t, http.MethodPost, "/api/board/posts", map[string]interface{}{"body": "hello", "room_mint": "Nope"}, agent)
	if w.Code != http.StatusBadRequest || errCode(t, w) != "ROOM_UNKNOWN_MINT" {
		t.Errorf("unknown mint: %d %s", w.Code, w.Body.String())
	}
	w = e.do(t, http.MethodPost, "/api/board/posts", map[string]interface{}{"body": "hello", "room_mint": mint}, nobody)
	if w.Code != http.StatusForbidden || errCode(t, w) != "ROOM_NOT_HOLDER" {
		t.Errorf("non holder: %d %s", w.Code, w.Body.String())
	}
	post := e.post(t, agent, map[string]interface{}{"body": "Welcome holders", "room_mint": mint})
	if post.Room == nil || post.Room.Mint != mint || post.Room.Symbol != "AAA" || post.RoomPinned || post.RoutedCount != 0 {
		t.Errorf("room post DTO = %+v", post)
	}
	w = e.do(t, http.MethodPost, "/api/board/posts/"+post.ID+"/replies", map[string]interface{}{"body": "hi"}, nobody)
	if w.Code != http.StatusForbidden || errCode(t, w) != "ROOM_NOT_HOLDER" {
		t.Errorf("reply by non holder: %d %s", w.Code, w.Body.String())
	}
	e.reply(t, holder, post.ID, "gm")
	// The legacy daemon route maps the room error the same way.
	w = e.do(t, http.MethodPost, "/api/v1/tracker/forum/posts/"+post.ID+"/replies", map[string]interface{}{"body": "hi", "auto": true}, nobody)
	if w.Code != http.StatusForbidden || errCode(t, w) != "ROOM_NOT_HOLDER" {
		t.Errorf("legacy reply by non holder: %d %s", w.Code, w.Body.String())
	}
	// A main feed post has room null.
	plain := e.post(t, agent, map[string]interface{}{"body": "main feed"})
	if plain.Room != nil {
		t.Errorf("main post room = %+v", plain.Room)
	}
	var raw map[string]json.RawMessage
	p1Data(t, e.do(t, http.MethodGet, "/api/board/posts/"+plain.ID, nil, "").Body.Bytes(), &raw)
	if string(raw["room"]) != "null" || string(raw["room_pinned"]) != "false" || string(raw["routed_count"]) != "0" {
		t.Errorf("main post raw fields: room=%s room_pinned=%s routed_count=%s", raw["room"], raw["room_pinned"], raw["routed_count"])
	}
}

func TestP2_RoomFeedsAndCounts(t *testing.T) {
	e := newP2Env(t)
	agent := e.named(t, "agent", "Agent")
	mint := e.room(t, "MintA", "AAA", "agent")
	main := e.post(t, agent, map[string]interface{}{"body": "main"})
	inRoom := e.post(t, agent, map[string]interface{}{"body": "room request", "category": "request", "room_mint": mint})

	posts, total := e.list(t, "", "")
	if total != 1 || posts[0].ID != main.ID {
		t.Errorf("main feed = %d posts", total)
	}
	posts, total = e.list(t, "?room="+mint, "")
	if total != 1 || posts[0].ID != inRoom.ID || posts[0].Room == nil || posts[0].Room.Symbol != "AAA" {
		t.Errorf("room feed = %d posts %+v", total, posts)
	}
	if _, total = e.list(t, "?mine=posts", agent); total != 2 {
		t.Errorf("mine=posts = %d (room posts included)", total)
	}
	var counts PortalBoardCounts
	p1Data(t, e.do(t, http.MethodGet, "/api/board/counts", nil, "").Body.Bytes(), &counts)
	if counts.All != 1 || counts.Request != 0 {
		t.Errorf("main counts = %+v", counts)
	}
	p1Data(t, e.do(t, http.MethodGet, "/api/board/counts?room="+mint, nil, "").Body.Bytes(), &counts)
	if counts.All != 1 || counts.Request != 1 {
		t.Errorf("room counts = %+v", counts)
	}
}

func TestP2_RoomsListAndGet(t *testing.T) {
	e := newP2Env(t)
	agent := e.named(t, "agent", "Agent Bot")
	holder := e.named(t, "holder", "Holder")
	mintA := e.room(t, "MintA", "AAA", "agent")
	mintB := e.room(t, "MintB", "BBB", "other")
	e.holds(t, "holder", mintB, 1)
	e.metrics.holders[mintA] = 7
	e.post(t, agent, map[string]interface{}{"body": "in A", "room_mint": mintA})

	w := e.do(t, http.MethodGet, "/api/board/rooms?mine=1", nil, "")
	if w.Code != http.StatusUnauthorized {
		t.Errorf("rooms without key: %d", w.Code)
	}
	w = e.do(t, http.MethodGet, "/api/board/rooms", nil, agent)
	if w.Code != http.StatusBadRequest {
		t.Errorf("rooms without mine=1: %d", w.Code)
	}
	var rooms []PortalRoom
	p1Data(t, e.do(t, http.MethodGet, "/api/board/rooms?mine=1", nil, agent).Body.Bytes(), &rooms)
	if len(rooms) != 1 || rooms[0].Mint != mintA || rooms[0].Role != "agent" || rooms[0].Symbol != "AAA" || rooms[0].Name != "AAA Token" ||
		rooms[0].AgentPeerID != "agent" || rooms[0].AgentDisplayName != "Agent Bot" || rooms[0].Posts7d != 1 || rooms[0].MembersEstimate == nil || *rooms[0].MembersEstimate != 7 ||
		rooms[0].LastPostAt == nil || rooms[0].ImageURL == "" || rooms[0].CanPost != nil {
		t.Errorf("agent rooms = %+v", rooms)
	}
	w = e.do(t, http.MethodGet, "/api/board/rooms?mine=1", nil, holder)
	p1Data(t, w.Body.Bytes(), &rooms)
	if len(rooms) != 1 || rooms[0].Mint != mintB || rooms[0].Role != "holder" || rooms[0].Posts7d != 0 || rooms[0].LastPostAt != nil || rooms[0].MembersEstimate != nil {
		t.Errorf("holder rooms = %+v", rooms)
	}
	if !strings.Contains(w.Body.String(), `"members_estimate":null`) {
		t.Errorf("members_estimate should be null without cached metrics: %s", w.Body.String())
	}

	var room PortalRoom
	p1Data(t, e.do(t, http.MethodGet, "/api/board/rooms/"+mintA, nil, "").Body.Bytes(), &room)
	if room.Mint != mintA || room.CanPost == nil || *room.CanPost || room.Role != "" {
		t.Errorf("anonymous room = %+v", room)
	}
	p1Data(t, e.do(t, http.MethodGet, "/api/board/rooms/"+mintA, nil, agent).Body.Bytes(), &room)
	if room.CanPost == nil || !*room.CanPost || room.Role != "agent" {
		t.Errorf("agent room = %+v", room)
	}
	p1Data(t, e.do(t, http.MethodGet, "/api/board/rooms/"+mintB, nil, holder).Body.Bytes(), &room)
	if room.CanPost == nil || !*room.CanPost || room.Role != "holder" {
		t.Errorf("holder room = %+v", room)
	}
	if w := e.do(t, http.MethodGet, "/api/board/rooms/Nope", nil, ""); w.Code != http.StatusNotFound {
		t.Errorf("unknown room: %d", w.Code)
	}
}

func TestP2_RoutingActivityAndUnreadQueue(t *testing.T) {
	e := newP2Env(t)
	alice := e.seen(t, "alice")
	bot := e.seen(t, "bot")
	e.seen(t, "idle")
	_ = e.autopilot.Set(context.Background(), "bot", []string{"request"}, e.clock.Now())
	_ = e.presence.Heartbeat(context.Background(), "bot", time.Hour)

	post := e.post(t, alice, map[string]interface{}{"body": "Need help with X", "category": "request"})
	if post.RoutedCount != 1 {
		t.Fatalf("routed_count = %d", post.RoutedCount)
	}
	if w := e.do(t, http.MethodGet, "/api/activity?kinds=bogus", nil, bot); w.Code != http.StatusBadRequest {
		t.Errorf("bad kinds: %d", w.Code)
	}
	// limit above 100 is clamped; a non-number or below 1 is refused.
	if w := e.do(t, http.MethodGet, "/api/activity?limit=500", nil, bot); w.Code != http.StatusOK {
		t.Errorf("limit=500 should be clamped, got %d", w.Code)
	}
	for _, bad := range []string{"0", "-1", "ten"} {
		if w := e.do(t, http.MethodGet, "/api/activity?limit="+bad, nil, bot); w.Code != http.StatusBadRequest {
			t.Errorf("limit=%s: %d, want 400", bad, w.Code)
		}
	}
	var feed PortalActivityResponse
	// mark=0 reads the queue without marking; the default marks read: the queue is then empty
	// and the plain feed still has the row.
	p1Data(t, e.do(t, http.MethodGet, "/api/activity?kinds=request_routed&unread=1&mark=0", nil, bot).Body.Bytes(), &feed)
	if len(feed.Items) != 1 || feed.Items[0].Kind != "request_routed" || feed.Items[0].PostID != post.ID || feed.Items[0].PostTitle != "Need help with X" ||
		feed.Items[0].ActorPeerID != "alice" || feed.Items[0].Amount != nil || feed.Items[0].RoomMint != "" || feed.Items[0].ReadAt != nil || feed.Unread != 1 {
		t.Errorf("routed queue with mark=0 = %+v", feed)
	}
	p1Data(t, e.do(t, http.MethodGet, "/api/activity?kinds=request_routed&unread=1&mark=0", nil, bot).Body.Bytes(), &feed)
	if len(feed.Items) != 1 {
		t.Errorf("mark=0 must not consume the queue: %+v", feed.Items)
	}
	p1Data(t, e.do(t, http.MethodGet, "/api/activity?kinds=request_routed&unread=1", nil, bot).Body.Bytes(), &feed)
	if len(feed.Items) != 1 || feed.Unread != 0 {
		t.Errorf("queue with the default mark = %+v", feed)
	}
	p1Data(t, e.do(t, http.MethodGet, "/api/activity?kinds=request_routed&unread=1", nil, bot).Body.Bytes(), &feed)
	if len(feed.Items) != 0 {
		t.Errorf("queue after read = %+v", feed.Items)
	}
	p1Data(t, e.do(t, http.MethodGet, "/api/activity", nil, bot).Body.Bytes(), &feed)
	if len(feed.Items) != 1 || feed.Items[0].ReadAt == nil {
		t.Errorf("plain feed = %+v", feed.Items)
	}
	// A request in a room carries the room mint on the routed item.
	mint := e.room(t, "MintA", "AAA", "alice")
	e.holds(t, "bot", mint, 1)
	roomPost := e.post(t, alice, map[string]interface{}{"body": "Room request", "category": "request", "room_mint": mint})
	if roomPost.RoutedCount != 1 {
		t.Fatalf("room request routed_count = %d", roomPost.RoutedCount)
	}
	p1Data(t, e.do(t, http.MethodGet, "/api/activity?kinds=request_routed&unread=1", nil, bot).Body.Bytes(), &feed)
	if len(feed.Items) != 1 || feed.Items[0].RoomMint != mint {
		t.Errorf("room routed item = %+v", feed.Items)
	}
}

func TestP2_AutoPostFlagAndHideAuto(t *testing.T) {
	e := newP2Env(t)
	agent := e.named(t, "agent", "Agent")
	mint := e.room(t, "MintA", "AAA", "agent")
	manual := e.post(t, agent, map[string]interface{}{"body": "Manual", "room_mint": mint})
	digest := e.post(t, agent, map[string]interface{}{"body": "AAA weekly digest\n\nnumbers", "category": "general", "room_mint": mint, "auto": true})
	if !digest.Auto || manual.Auto || digest.Title != "AAA weekly digest" {
		t.Errorf("auto flags: digest=%v manual=%v title=%q", digest.Auto, manual.Auto, digest.Title)
	}
	got, _ := e.getPost(t, digest.ID, "")
	if !got.Auto {
		t.Errorf("auto flag missing on the single post read")
	}
	var raw map[string]json.RawMessage
	p1Data(t, e.do(t, http.MethodGet, "/api/board/posts/"+manual.ID, nil, "").Body.Bytes(), &raw)
	if _, present := raw["auto"]; present {
		t.Errorf("manual post should omit auto: %s", raw["auto"])
	}
	if _, total := e.list(t, "?room="+mint, ""); total != 2 {
		t.Errorf("room feed = %d", total)
	}
	posts, total := e.list(t, "?room="+mint+"&hide_auto=1", "")
	if total != 1 || posts[0].ID != manual.ID {
		t.Errorf("hide_auto feed = %d %+v", total, posts)
	}
}

func TestP2_RoomDigest(t *testing.T) {
	e := newP2Env(t)
	agent := e.named(t, "agent", "Agent")
	holder := e.named(t, "holder", "Holder")
	mint := e.room(t, "MintA", "AAA", "agent")
	e.holds(t, "holder", mint, 1)
	e.post(t, agent, map[string]interface{}{"body": "Room post", "room_mint": mint})

	if w := e.do(t, http.MethodGet, "/api/board/rooms/"+mint+"/digest", nil, ""); w.Code != http.StatusUnauthorized {
		t.Errorf("digest without key: %d", w.Code)
	}
	w := e.do(t, http.MethodGet, "/api/board/rooms/"+mint+"/digest", nil, holder)
	if w.Code != http.StatusForbidden || errCode(t, w) != "ROOM_NOT_AGENT" {
		t.Errorf("digest by holder: %d %s", w.Code, w.Body.String())
	}
	if w := e.do(t, http.MethodGet, "/api/board/rooms/Nope/digest", nil, agent); w.Code != http.StatusNotFound {
		t.Errorf("digest of unknown room: %d", w.Code)
	}
	if w := e.do(t, http.MethodGet, "/api/board/rooms/"+mint+"/digest?days=99", nil, agent); w.Code != http.StatusBadRequest {
		t.Errorf("days out of range: %d", w.Code)
	}
	var raw map[string]json.RawMessage
	p1Data(t, e.do(t, http.MethodGet, "/api/board/rooms/"+mint+"/digest?days=7", nil, agent).Body.Bytes(), &raw)
	var digest PortalRoomDigest
	p1Data(t, e.do(t, http.MethodGet, "/api/board/rooms/"+mint+"/digest?days=7", nil, agent).Body.Bytes(), &digest)
	if digest.Posts != 1 || digest.Replies != 0 || len(digest.TopThreads) != 1 || digest.TopThreads[0].Title != "Room post" || digest.NewHolders != nil {
		t.Errorf("digest = %+v", digest)
	}
	if string(raw["new_holders"]) != "null" || string(raw["bounties_awarded"]) != "0" {
		t.Errorf("digest raw = %s", raw)
	}
	start, _ := time.Parse(time.RFC3339, digest.PeriodStart)
	end, _ := time.Parse(time.RFC3339, digest.PeriodEnd)
	if end.Sub(start) != 7*24*time.Hour || !end.Equal(e.clock.Now()) {
		t.Errorf("period = %s .. %s", digest.PeriodStart, digest.PeriodEnd)
	}
}

func TestP2_HeartbeatAutopilotCategories(t *testing.T) {
	handler, router, apiKeyRepo, _ := setupHeartbeatTest(t)
	apiKeyRepo.Store("peer-1", "key-1")
	repo := repository.NewMemoryPeerAutopilotRepository()
	handler.SetAutopilotRepo(repo)

	send := func(body map[string]interface{}) int {
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/tracker/heartbeat", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-API-Key", "key-1")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w.Code
	}
	if code := send(map[string]interface{}{"autopilot_categories": []string{"Request", " bounty ", "request", "nonsense", "general", "discovery", "token-offer", "general"}}); code != http.StatusOK {
		t.Fatalf("heartbeat: %d", code)
	}
	got, _ := repo.CategoriesByIDs(context.Background(), []string{"peer-1"})
	want := []string{"request", "bounty", "general", "discovery", "token-offer"}
	if len(got["peer-1"]) != len(want) {
		t.Fatalf("stored categories = %v, want %v", got["peer-1"], want)
	}
	for i := range want {
		if got["peer-1"][i] != want[i] {
			t.Errorf("stored categories = %v, want %v", got["peer-1"], want)
		}
	}
	// An empty list is stored as such; an absent field clears the row.
	send(map[string]interface{}{"autopilot_categories": []string{}})
	got, _ = repo.CategoriesByIDs(context.Background(), []string{"peer-1"})
	if cats, ok := got["peer-1"]; !ok || len(cats) != 0 {
		t.Errorf("empty list = %v (present %v)", cats, ok)
	}
	send(map[string]interface{}{"multiaddrs": []string{"/ip4/127.0.0.1/tcp/4001"}})
	got, _ = repo.CategoriesByIDs(context.Background(), []string{"peer-1"})
	if _, ok := got["peer-1"]; ok {
		t.Errorf("absent field should clear the row: %v", got)
	}
	// Later heartbeats without the field issue no further deletes (B14); a peer never seen
	// with the field gets exactly one.
	clears := repo.Clears()
	send(map[string]interface{}{"multiaddrs": []string{"/ip4/127.0.0.1/tcp/4001"}})
	send(map[string]interface{}{})
	if repo.Clears() != clears {
		t.Errorf("heartbeats without the field kept deleting: %d -> %d", clears, repo.Clears())
	}
	apiKeyRepo.Store("peer-old", "key-old")
	for i := 0; i < 3; i++ {
		raw, _ := json.Marshal(map[string]interface{}{"multiaddrs": []string{"/ip4/127.0.0.1/tcp/4001"}})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/tracker/heartbeat", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-API-Key", "key-old")
		router.ServeHTTP(httptest.NewRecorder(), req)
	}
	if repo.Clears() != clears+1 {
		t.Errorf("old daemon: %d deletes over 3 heartbeats, want 1", repo.Clears()-clears)
	}
	// Reporting again after a clear stores the row and a following absent field clears once more.
	send(map[string]interface{}{"autopilot_categories": []string{"general"}})
	send(map[string]interface{}{})
	if got, _ = repo.CategoriesByIDs(context.Background(), []string{"peer-1"}); len(got) != 0 {
		t.Errorf("row not cleared after a re-report: %v", got)
	}
}
