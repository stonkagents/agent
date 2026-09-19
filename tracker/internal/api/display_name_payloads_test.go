// Package: tracker/internal/api
// Purpose: The owner-set display name appears next to every public peer reference
//          (forum and board authors, leaderboard rows, peer detail, tokens) and is
//          omitted when the peer has none; the heartbeat can clear it.

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/clock"
	"github.com/stonkagents/agent/tracker/internal/geo"
	"github.com/stonkagents/agent/tracker/internal/leaderboard"
	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/presence"
	"github.com/stonkagents/agent/tracker/internal/repository"
	"github.com/stonkagents/agent/tracker/internal/services"
)

const (
	namedPeer   = "12D3KooWNamedPeer111111111111111111111111111111111"
	unnamedPeer = "12D3KooWUnnamedPeer1111111111111111111111111111111"
	namedKey    = "key-named"
	unnamedKey  = "key-unnamed"
)

// displayNameEnv wires the public handlers over memory repositories with two peers:
// one named "Atlas", one without a display name.
type displayNameEnv struct {
	srv      *Server
	peers    *repository.MemoryPeerRepository
	tokens   *repository.MemoryTokenRepository
	presence *presence.MemoryPresenceStore
}

func newDisplayNameEnv(t *testing.T) *displayNameEnv {
	t.Helper()
	clk := clock.NewMockClock(time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC))
	peerRepo := repository.NewMemoryPeerRepository()
	assetRepo := repository.NewMemoryAssetRepository()
	forumRepo := repository.NewMemoryForumRepository()
	apiKeyRepo := repository.NewMemoryPeerAPIKeyRepository()
	tokenRepo := repository.NewMemoryTokenRepository()
	store := presence.NewMemoryPresenceStore(clk)
	t.Cleanup(store.Close)

	now := clk.Now()
	for _, p := range []*models.Peer{
		{PeerID: namedPeer, DisplayName: "Atlas", MaskedPeerID: "12D3...1111", TotalUploadBytes: 500, TotalDownloadBytes: 400, FirstSeen: now, LastSeen: now},
		{PeerID: unnamedPeer, MaskedPeerID: "12D3...2222", TotalUploadBytes: 300, TotalDownloadBytes: 200, FirstSeen: now, LastSeen: now},
	} {
		if err := peerRepo.Create(context.Background(), p); err != nil {
			t.Fatal(err)
		}
	}
	apiKeyRepo.Store(namedPeer, namedKey)
	apiKeyRepo.Store(unnamedPeer, unnamedKey)

	peerSvc := services.NewPeerService(peerRepo, store, apiKeyRepo)
	assetSvc := services.NewTestAssetServiceWithSemanticSearch(assetRepo, store, repository.NewMemoryAvailabilityRepository())
	forumSvc := services.NewForumService(forumRepo, nil, nil)
	forumHandler := NewForumHandler(forumSvc, apiKeyRepo)
	forumHandler.SetPeerRepo(peerRepo)
	tokenHandler := NewTokenHandler(tokenRepo)
	tokenHandler.SetPeerRepo(peerRepo)
	peerHandler := NewPeerHandler(peerSvc)
	peerHandler.SetAPIKeyRepo(apiKeyRepo)
	peerHandler.SetPresenceStore(store)
	portalHandler := NewPortalHandler(assetSvc, assetRepo, peerSvc, peerRepo, forumSvc, repository.NewMemoryPeerTrustBlockRepository(),
		apiKeyRepo, repository.NewMemoryGuestKeyRepository(), repository.NewMemoryReputationRepository(), &geo.StubResolver{}, nil, nil, nil)

	srv := NewServer(ServerDeps{
		PeerHandler:        peerHandler,
		AssetHandler:       NewAssetHandlerWithPeers(assetSvc, peerSvc),
		ForumHandler:       forumHandler,
		PortalHandler:      portalHandler,
		TokenHandler:       tokenHandler,
		LeaderboardHandler: NewLeaderboardHandler(leaderboard.NewPostgresStore(peerRepo, assetRepo)),
		APIKeyRepo:         apiKeyRepo,
		Address:            ":7842",
	})
	return &displayNameEnv{srv: srv, peers: peerRepo, tokens: tokenRepo, presence: store}
}

func (e *displayNameEnv) do(t *testing.T, method, path string, body interface{}, apiKey string) map[string]interface{} {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("X-API-Key", apiKey)
	}
	w := httptest.NewRecorder()
	e.srv.Router().ServeHTTP(w, req)
	if w.Code < 200 || w.Code > 299 {
		t.Fatalf("%s %s: status %d body %s", method, path, w.Code, w.Body.String())
	}
	var out map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("%s %s: decode %s: %v", method, path, w.Body.String(), err)
	}
	return out
}

// items returns the "data" array of a list response.
func items(t *testing.T, resp map[string]interface{}) []map[string]interface{} {
	t.Helper()
	raw, ok := resp["data"].([]interface{})
	if !ok {
		t.Fatalf("data is not a list: %v", resp)
	}
	out := make([]map[string]interface{}, 0, len(raw))
	for _, r := range raw {
		out = append(out, r.(map[string]interface{}))
	}
	return out
}

// byField indexes list items by the value of one field.
func byField(rows []map[string]interface{}, field string) map[string]map[string]interface{} {
	out := make(map[string]map[string]interface{}, len(rows))
	for _, r := range rows {
		if v, ok := r[field].(string); ok {
			out[v] = r
		}
	}
	return out
}

// expectName asserts the display-name field is present with want, or absent when want is "".
func expectName(t *testing.T, where string, row map[string]interface{}, field, want string) {
	t.Helper()
	got, present := row[field]
	if want == "" {
		if present {
			t.Errorf("%s: %s = %v, want omitted", where, field, got)
		}
		return
	}
	if got != want {
		t.Errorf("%s: %s = %v, want %q", where, field, got, want)
	}
}

func TestDisplayName_ForumPostsAndReplies(t *testing.T) {
	env := newDisplayNameEnv(t)
	created := env.do(t, http.MethodPost, "/api/v1/tracker/forum/posts", map[string]string{"title": "Hello", "description": "from Atlas"}, namedKey)
	expectName(t, "create post", created, "author_display_name", "Atlas")
	postID := created["id"].(string)
	env.do(t, http.MethodPost, "/api/v1/tracker/forum/posts", map[string]string{"title": "Anon", "description": "no name"}, unnamedKey)

	posts := byField(items(t, env.do(t, http.MethodGet, "/api/v1/tracker/forum/posts", nil, "")), "author_peer_id")
	expectName(t, "list posts named", posts[namedPeer], "author_display_name", "Atlas")
	expectName(t, "list posts unnamed", posts[unnamedPeer], "author_display_name", "")

	single := env.do(t, http.MethodGet, "/api/v1/tracker/forum/posts/"+postID, nil, "")
	expectName(t, "get post", single, "author_display_name", "Atlas")

	reply := env.do(t, http.MethodPost, "/api/v1/tracker/forum/posts/"+postID+"/replies", map[string]string{"body": "hi"}, unnamedKey)
	expectName(t, "create reply unnamed", reply, "author_display_name", "")
	env.do(t, http.MethodPost, "/api/v1/tracker/forum/posts/"+postID+"/replies", map[string]string{"body": "hey"}, namedKey)
	replies := byField(items(t, env.do(t, http.MethodGet, "/api/v1/tracker/forum/posts/"+postID+"/replies", nil, "")), "author_peer_id")
	expectName(t, "list replies named", replies[namedPeer], "author_display_name", "Atlas")
	expectName(t, "list replies unnamed", replies[unnamedPeer], "author_display_name", "")
}

func TestDisplayName_BoardPostsAndReplies(t *testing.T) {
	env := newDisplayNameEnv(t)
	created := env.do(t, http.MethodPost, "/api/board/posts", map[string]interface{}{"body": "Atlas here", "category": "general"}, namedKey)["data"].(map[string]interface{})
	expectName(t, "board create", created, "authorDisplayName", "Atlas")
	postID := created["id"].(string)
	env.do(t, http.MethodPost, "/api/board/posts", map[string]interface{}{"body": "anon here", "category": "general"}, unnamedKey)

	posts := byField(items(t, env.do(t, http.MethodGet, "/api/board/posts", nil, "")), "author")
	expectName(t, "board list named", posts[namedPeer], "authorDisplayName", "Atlas")
	expectName(t, "board list unnamed", posts[unnamedPeer], "authorDisplayName", "")
	if posts[namedPeer]["author"] != namedPeer {
		t.Errorf("author must stay the peer id, got %v", posts[namedPeer]["author"])
	}

	single := env.do(t, http.MethodGet, "/api/board/posts/"+postID, nil, "")["data"].(map[string]interface{})
	expectName(t, "board get", single, "authorDisplayName", "Atlas")

	reply := env.do(t, http.MethodPost, "/api/board/posts/"+postID+"/replies", map[string]string{"body": "yo"}, namedKey)["data"].(map[string]interface{})
	expectName(t, "board create reply", reply, "authorDisplayName", "Atlas")
	env.do(t, http.MethodPost, "/api/board/posts/"+postID+"/replies", map[string]string{"body": "sup"}, unnamedKey)
	replies := byField(items(t, env.do(t, http.MethodGet, "/api/board/posts/"+postID+"/replies", nil, "")), "author")
	expectName(t, "board replies named", replies[namedPeer], "authorDisplayName", "Atlas")
	expectName(t, "board replies unnamed", replies[unnamedPeer], "authorDisplayName", "")
}

func TestDisplayName_Leaderboards(t *testing.T) {
	env := newDisplayNameEnv(t)
	for _, path := range []string{"/api/v1/tracker/leaderboard/seeders", "/api/v1/tracker/leaderboard/leechers"} {
		rows := byField(items(t, env.do(t, http.MethodGet, path, nil, "")), "masked_peer_id")
		expectName(t, path+" named", rows["12D3...1111"], "display_name", "Atlas")
		expectName(t, path+" unnamed", rows["12D3...2222"], "display_name", "")
	}
}

func TestDisplayName_PeerDetailAndTokens(t *testing.T) {
	env := newDisplayNameEnv(t)
	expectName(t, "peer detail named", env.do(t, http.MethodGet, "/api/v1/tracker/peers/"+namedPeer, nil, ""), "display_name", "Atlas")
	expectName(t, "peer detail unnamed", env.do(t, http.MethodGet, "/api/v1/tracker/peers/"+unnamedPeer, nil, ""), "display_name", "")

	for peer, mint := range map[string]string{
		namedPeer:   "7xKXtg2CW87d97TXJSDpbD5jBkheTqA83TZRuJosgAsU",
		unnamedPeer: "9wFFyRfZBsuAha4YcuxcXLKwMxJR43S7fPfQLusDBzvT",
	} {
		if err := env.tokens.Create(context.Background(), &models.PeerToken{PeerID: peer, TokenContractAddress: mint, TokenTicker: "T", TokenName: "Token", LaunchedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	one := env.do(t, http.MethodGet, "/api/peers/"+namedPeer+"/token", nil, "")["data"].(map[string]interface{})
	expectName(t, "peer token named", one, "display_name", "Atlas")
	if one["peer_id"] != namedPeer || one["token_ticker"] != "T" {
		t.Errorf("token fields lost: %v", one)
	}
	expectName(t, "peer token unnamed", env.do(t, http.MethodGet, "/api/peers/"+unnamedPeer+"/token", nil, "")["data"].(map[string]interface{}), "display_name", "")

	list := byField(items(t, env.do(t, http.MethodGet, "/api/tokens", nil, "")), "peer_id")
	expectName(t, "tokens list named", list[namedPeer], "display_name", "Atlas")
	expectName(t, "tokens list unnamed", list[unnamedPeer], "display_name", "")
}

func TestDisplayName_HeartbeatRules(t *testing.T) {
	env := newDisplayNameEnv(t)
	ctx := context.Background()
	get := func() string {
		p, err := env.peers.FindByID(ctx, unnamedPeer)
		if err != nil {
			t.Fatal(err)
		}
		return p.DisplayName
	}
	hb := func(body map[string]interface{}) {
		env.do(t, http.MethodPost, "/api/v1/tracker/heartbeat", body, unnamedKey)
	}

	// Absent field: unchanged (still empty).
	hb(map[string]interface{}{"client_version": "2.2.2"})
	if got := get(); got != "" {
		t.Fatalf("after heartbeat without name = %q", got)
	}
	// A daemon placeholder fills an empty slot.
	hb(map[string]interface{}{"display_name": "swarm_operator_42"})
	if got := get(); got != "swarm_operator_42" {
		t.Fatalf("placeholder on empty = %q", got)
	}
	// Tracker-side name (launch claim) is not overwritten by a later placeholder or an absent field.
	if err := env.peers.UpdateDisplayName(ctx, unnamedPeer, "lalala_agent"); err != nil {
		t.Fatal(err)
	}
	hb(map[string]interface{}{"display_name": "quiet_courier_07"})
	hb(map[string]interface{}{"multiaddrs": []string{"/ip4/127.0.0.1/tcp/4001"}})
	if got := get(); got != "lalala_agent" {
		t.Fatalf("tracker name overwritten: %q", got)
	}
	// An owner-set name (sanitized) replaces it.
	hb(map[string]interface{}{"display_name": "  <b>Nova</b>\t "})
	if got := get(); got != "bNova/b" {
		t.Fatalf("owner name = %q, want sanitized", got)
	}
	expectName(t, "peer detail", env.do(t, http.MethodGet, "/api/v1/tracker/peers/"+unnamedPeer, nil, ""), "display_name", "bNova/b")
	// An explicit empty string clears; the placeholder then fills the empty slot (daemon reset flow).
	hb(map[string]interface{}{"display_name": ""})
	if got := get(); got != "" {
		t.Fatalf("explicit clear left %q", got)
	}
	expectName(t, "peer detail after clear", env.do(t, http.MethodGet, "/api/v1/tracker/peers/"+unnamedPeer, nil, ""), "display_name", "")
	hb(map[string]interface{}{"display_name": "neon_archivist_88"})
	if got := get(); got != "neon_archivist_88" {
		t.Fatalf("placeholder after clear = %q", got)
	}
}

func TestSanitizeDisplayName_RunesAndControlChars(t *testing.T) {
	if got := sanitizeDisplayName("a\x00b\x1fc\x7fd"); got != "abcd" {
		t.Errorf("control chars: %q", got)
	}
	// 60 two-byte runes: cut to 50 runes, never mid-character.
	long := ""
	for i := 0; i < 60; i++ {
		long += "é"
	}
	got := sanitizeDisplayName(long)
	if n := len([]rune(got)); n != 50 || got != long[:100] {
		t.Errorf("rune cap: %d runes %q", n, got)
	}
}

func TestDisplayNamesFor_DedupesAndDegrades(t *testing.T) {
	repo := repository.NewMemoryPeerRepository()
	_ = repo.Create(context.Background(), &models.Peer{PeerID: "p1", DisplayName: "One"})
	_ = repo.Create(context.Background(), &models.Peer{PeerID: "p2"})
	got := displayNamesFor(context.Background(), repo, []string{"p1", "", "p1", "p2", "missing"})
	if len(got) != 1 || got["p1"] != "One" {
		t.Errorf("names = %v", got)
	}
	if got := displayNamesFor(context.Background(), nil, []string{"p1"}); len(got) != 0 {
		t.Errorf("nil repo = %v", got)
	}
}
