// Package: tracker/internal/api
// Purpose: A claimed launch carries the bound agent's owner-set display name on every
//          launch view (single, list, by-wallet); unclaimed or unnamed peers omit it.

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/repository"
	"github.com/stonkagents/agent/tracker/internal/services"
)

func TestLaunchViews_PeerDisplayName(t *testing.T) {
	env := newLaunchTestEnv(t, false)
	peers := repository.NewMemoryPeerRepository()
	_ = peers.Create(context.Background(), &models.Peer{PeerID: "peer-named", DisplayName: "Atlas"})
	_ = peers.Create(context.Background(), &models.Peer{PeerID: "peer-plain"})
	launchSvc := services.NewLaunchService(services.LaunchServiceDeps{
		Launches: env.launches, Revenue: env.revenue, Tokens: env.tokens, Accounts: env.accounts,
		Wallets: env.wallets, Credits: env.credits, Verifier: env.verifier, Clock: env.clock,
		Peers: peers, TreasuryAddress: lTreasury, MinFeeLamports: 10_000_000,
	})
	repRepo := repository.NewMemoryBoardReputationRepository()
	_ = repRepo.Upsert(context.Background(), &models.PeerReputation{PeerID: "peer-named", Tier: "trusted", Score: 150})
	launchSvc.SetReputationService(services.NewReputationService(repRepo, repository.NewMemoryForumRepository(), nil, env.clock))
	env.srv = NewServer(ServerDeps{LaunchHandler: NewLaunchHandler(launchSvc), TokenHandler: NewTokenHandler(env.tokens), APIKeyRepo: env.apiKeys, Address: ":7842"})

	// Launch A claimed by the owner-named peer (kept), launch B by an unnamed one (gets <symbol>_agent).
	if w := env.do(t, http.MethodPost, "/api/launch/record", recordBody(), ""); w.Code != http.StatusCreated {
		t.Fatalf("record A: %d %s", w.Code, w.Body.String())
	}
	b := secondRecordBody()
	b["creatorWallet"] = lOther
	if w := env.do(t, http.MethodPost, "/api/launch/record", b, ""); w.Code != http.StatusCreated {
		t.Fatalf("record B: %d %s", w.Code, w.Body.String())
	}
	env.linkPeer(t, "peer-named", "key-named", lCreator)
	env.linkPeer(t, "peer-plain", "key-plain", lOther)
	if w := env.do(t, http.MethodPost, "/api/launch/claim", map[string]string{"mint": lMint}, "key-named"); w.Code != http.StatusOK {
		t.Fatalf("claim A: %d %s", w.Code, w.Body.String())
	}
	if w := env.do(t, http.MethodPost, "/api/launch/claim", map[string]string{"mint": lMint2}, "key-plain"); w.Code != http.StatusOK {
		t.Fatalf("claim B: %d %s", w.Code, w.Body.String())
	}

	check := func(where string, v map[string]interface{}, want string) {
		t.Helper()
		snake, camel := v["peer_display_name"], v["peerDisplayName"]
		if want == "" {
			if snake != nil || camel != nil {
				t.Errorf("%s: display name = %v / %v, want omitted", where, snake, camel)
			}
			return
		}
		if snake != want || camel != want {
			t.Errorf("%s: peer_display_name = %v, peerDisplayName = %v, want %q", where, snake, camel, want)
		}
	}

	w := env.do(t, http.MethodGet, "/api/launch/"+lMint, nil, "")
	if w.Code != http.StatusOK {
		t.Fatalf("get A: %d %s", w.Code, w.Body.String())
	}
	named := decodeData(t, w)
	check("GET /api/launch/{mint} named", named, "Atlas")
	if named["peer_reputation_tier"] != "trusted" || named["peerReputationTier"] != "trusted" {
		t.Errorf("named launch reputation tier = %v / %v, want trusted", named["peer_reputation_tier"], named["peerReputationTier"])
	}
	w = env.do(t, http.MethodGet, "/api/launch/"+lMint2, nil, "")
	plain := decodeData(t, w)
	check("GET /api/launch/{mint} token-named", plain, "STNK2_Agent")
	if plain["peer_reputation_tier"] != "new" {
		t.Errorf("unranked launch reputation tier = %v, want new", plain["peer_reputation_tier"])
	}

	w = env.do(t, http.MethodGet, "/api/launches?limit=20", nil, "")
	if w.Code != http.StatusOK {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	var list struct {
		Data []map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || len(list.Data) != 2 {
		t.Fatalf("list decode: %v (%d items)", err, len(list.Data))
	}
	for _, item := range list.Data {
		switch item["mint"] {
		case lMint:
			check("GET /api/launches named", item, "Atlas")
		case lMint2:
			check("GET /api/launches token-named", item, "STNK2_Agent")
		}
	}

	w = env.do(t, http.MethodGet, "/api/launch/by-wallet?wallet="+lCreator, nil, "")
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || len(list.Data) != 1 {
		t.Fatalf("by-wallet decode: %v (%d items)", err, len(list.Data))
	}
	check("GET /api/launch/by-wallet", list.Data[0], "Atlas")
}

// TestLaunchClaim_NamesAgentFromToken: binding a launch names the agent "<symbol>_agent"
// when the peer has no name or only the daemon placeholder; an owner-set name is kept.
// The claim response and the launch views carry the resulting name.
func TestLaunchClaim_NamesAgentFromToken(t *testing.T) {
	env := newLaunchTestEnv(t, false)
	peers := repository.NewMemoryPeerRepository()
	ctx := context.Background()
	_ = peers.Create(ctx, &models.Peer{PeerID: "peer-placeholder", DisplayName: "swarm_operator_42"})
	_ = peers.Create(ctx, &models.Peer{PeerID: "peer-owner", DisplayName: "Atlas"})
	launchSvc := services.NewLaunchService(services.LaunchServiceDeps{
		Launches: env.launches, Revenue: env.revenue, Tokens: env.tokens, Accounts: env.accounts,
		Wallets: env.wallets, Credits: env.credits, Verifier: env.verifier, Clock: env.clock,
		Peers: peers, TreasuryAddress: lTreasury, MinFeeLamports: 10_000_000,
	})
	env.srv = NewServer(ServerDeps{LaunchHandler: NewLaunchHandler(launchSvc), TokenHandler: NewTokenHandler(env.tokens), APIKeyRepo: env.apiKeys, Address: ":7842"})

	// Launch A (symbol from recordBody) claimed by the placeholder peer; B (STNK2) by the owner-named peer.
	a := recordBody()
	if w := env.do(t, http.MethodPost, "/api/launch/record", a, ""); w.Code != http.StatusCreated {
		t.Fatalf("record A: %d %s", w.Code, w.Body.String())
	}
	b := secondRecordBody()
	b["creatorWallet"] = lOther
	if w := env.do(t, http.MethodPost, "/api/launch/record", b, ""); w.Code != http.StatusCreated {
		t.Fatalf("record B: %d %s", w.Code, w.Body.String())
	}
	env.linkPeer(t, "peer-placeholder", "key-placeholder", lCreator)
	env.linkPeer(t, "peer-owner", "key-owner", lOther)

	wantA := models.AgentDisplayNameForSymbol(a["symbol"].(string))
	w := env.do(t, http.MethodPost, "/api/launch/claim", map[string]string{"mint": lMint}, "key-placeholder")
	if w.Code != http.StatusOK {
		t.Fatalf("claim A: %d %s", w.Code, w.Body.String())
	}
	if got := decodeData(t, w)["peer_display_name"]; got != wantA {
		t.Errorf("claim A peer_display_name = %v, want %q", got, wantA)
	}
	if p, _ := peers.FindByID(ctx, "peer-placeholder"); p.DisplayName != wantA {
		t.Errorf("placeholder peer renamed to %q, want %q", p.DisplayName, wantA)
	}
	// Idempotent re-claim reports the same name.
	w = env.do(t, http.MethodPost, "/api/launch/claim", map[string]string{"mint": lMint}, "key-placeholder")
	if w.Code != http.StatusOK || decodeData(t, w)["peer_display_name"] != wantA {
		t.Errorf("re-claim = %d %s", w.Code, w.Body.String())
	}

	w = env.do(t, http.MethodPost, "/api/launch/claim", map[string]string{"mint": lMint2}, "key-owner")
	if w.Code != http.StatusOK {
		t.Fatalf("claim B: %d %s", w.Code, w.Body.String())
	}
	if got := decodeData(t, w)["peer_display_name"]; got != "Atlas" {
		t.Errorf("claim B peer_display_name = %v, want owner name kept", got)
	}
	if p, _ := peers.FindByID(ctx, "peer-owner"); p.DisplayName != "Atlas" {
		t.Errorf("owner name overwritten: %q", p.DisplayName)
	}

	// The launch view shows the token-derived name; a later placeholder heartbeat would not undo it (see peer service tests).
	w = env.do(t, http.MethodGet, "/api/launch/"+lMint, nil, "")
	if got := decodeData(t, w)["peer_display_name"]; got != wantA {
		t.Errorf("launch view peer_display_name = %v, want %q", got, wantA)
	}
}
