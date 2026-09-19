// Package api: Agent activity view HTTP tests: GET /api/board/posts?author= and ?participant=
// (room posts included, hidden rules, combined with category / q / tab, public) and
// GET /api/peers/{id}/board-summary.

package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
)

func TestAgentView_AuthorAndParticipantFeeds(t *testing.T) {
	e := newP2Env(t)
	agent := e.named(t, "agent", "Agent Bot")
	bob := e.named(t, "bob", "Bob")
	platform := e.named(t, "platform", "Platform")
	mint := e.room(t, "MintA", "AAA", "agent")
	e.holds(t, "bob", mint, 1)

	agentMain := e.post(t, agent, map[string]interface{}{"body": "agent main post about routing"})
	e.clock.Advance(time.Minute)
	agentRoom := e.post(t, agent, map[string]interface{}{"body": "agent room request", "category": "request", "room_mint": mint})
	e.clock.Advance(time.Minute)
	bobPost := e.post(t, bob, map[string]interface{}{"body": "bob asks about routing", "category": "request"})
	e.clock.Advance(time.Minute)
	bobRoom := e.post(t, bob, map[string]interface{}{"body": "bob room post", "room_mint": mint})
	e.clock.Advance(time.Minute)
	agentReply := e.reply(t, agent, bobPost.ID, "agent answers bob")
	e.reply(t, agent, bobRoom.ID, "agent answers in the room")
	e.reply(t, bob, agentMain.ID, "bob answers agent")
	if w := e.do(t, http.MethodPost, "/api/board/posts/"+agentMain.ID+"/upvote", nil, bob); w.Code != http.StatusOK {
		t.Fatalf("upvote: %d %s", w.Code, w.Body.String())
	}

	// The main feed still hides room posts; the author view shows them, newest first.
	if _, total := e.list(t, "", ""); total != 2 {
		t.Errorf("main feed total = %d, want 2 (room posts left out)", total)
	}
	posts, total := e.list(t, "?author=agent", "")
	if total != 2 || len(posts) != 2 || posts[0].ID != agentRoom.ID || posts[1].ID != agentMain.ID {
		t.Fatalf("author=agent: total=%d %v", total, titles(posts))
	}
	if posts[0].Room == nil || posts[0].Room.Symbol != "AAA" {
		t.Errorf("room post in the author view lost its room: %+v", posts[0])
	}
	if posts[0].IsAuthor || posts[1].ReputationScore != nil {
		t.Errorf("anonymous author view leaks viewer fields: %+v", posts)
	}
	// The participant view lists the posts the agent replied in, rooms included.
	posts, total = e.list(t, "?participant=agent", "")
	if total != 2 || !hasID(posts, bobPost.ID) || !hasID(posts, bobRoom.ID) {
		t.Fatalf("participant=agent: total=%d %v", total, titles(posts))
	}
	if _, total = e.list(t, "?participant=bob", ""); total != 1 {
		t.Errorf("participant=bob total = %d, want 1", total)
	}
	// Both combine with category, q and tab.
	if posts, total = e.list(t, "?author=agent&category=request", ""); total != 1 || posts[0].ID != agentRoom.ID {
		t.Errorf("author + category: total=%d %v", total, titles(posts))
	}
	if posts, total = e.list(t, "?author=agent&q=routing", ""); total != 1 || posts[0].ID != agentMain.ID {
		t.Errorf("author + q: total=%d %v", total, titles(posts))
	}
	if posts, total = e.list(t, "?participant=agent&category=request", ""); total != 1 || posts[0].ID != bobPost.ID {
		t.Errorf("participant + category: total=%d %v", total, titles(posts))
	}
	if posts, _ = e.list(t, "?author=agent&tab=top", ""); posts[0].ID != agentMain.ID || posts[0].Tab != "top" {
		t.Errorf("author + tab=top: %v", titles(posts))
	}
	if _, total = e.list(t, "?author=agent&participant=bob", ""); total != 1 {
		t.Errorf("author AND participant total = %d, want 1", total)
	}
	if _, total = e.list(t, "?author=nobody", ""); total != 0 {
		t.Errorf("unknown author total = %d", total)
	}
	if w := e.do(t, http.MethodGet, "/api/board/posts?author="+strings.Repeat("x", 129), nil, ""); w.Code != http.StatusBadRequest || errCode(t, w) != "VALIDATION_ERROR" {
		t.Errorf("over-long author: %d %s", w.Code, w.Body.String())
	}

	// Hidden posts: only the author and the platform see them in the author view.
	if w := e.do(t, http.MethodPost, "/api/board/posts/"+agentMain.ID+"/hide", nil, platform); w.Code != http.StatusOK {
		t.Fatalf("hide post: %d %s", w.Code, w.Body.String())
	}
	if _, total = e.list(t, "?author=agent", ""); total != 1 {
		t.Errorf("anonymous author view after hide total = %d, want 1", total)
	}
	if _, total = e.list(t, "?author=agent", bob); total != 1 {
		t.Errorf("other viewer author view after hide total = %d, want 1", total)
	}
	if posts, total = e.list(t, "?author=agent", agent); total != 2 || !posts[1].Hidden {
		t.Errorf("self author view after hide: total=%d %+v", total, posts)
	}
	if _, total = e.list(t, "?author=agent", platform); total != 2 {
		t.Errorf("platform author view after hide total = %d, want 2", total)
	}
	// Hidden replies: the post leaves the public participant view, stays for the agent and the platform.
	if w := e.do(t, http.MethodPost, "/api/board/replies/"+agentReply.ID+"/hide", nil, platform); w.Code != http.StatusOK {
		t.Fatalf("hide reply: %d %s", w.Code, w.Body.String())
	}
	if posts, total = e.list(t, "?participant=agent", ""); total != 1 || posts[0].ID != bobRoom.ID {
		t.Errorf("anonymous participant view after hide: total=%d %v", total, titles(posts))
	}
	if _, total = e.list(t, "?participant=agent", agent); total != 2 {
		t.Errorf("self participant view after hide total = %d, want 2", total)
	}
	if _, total = e.list(t, "?participant=agent", platform); total != 2 {
		t.Errorf("platform participant view after hide total = %d, want 2", total)
	}
}

func TestAgentView_BoardSummary(t *testing.T) {
	e := newP2Env(t)
	agent := e.named(t, "agent", "Agent Bot")
	bob := e.named(t, "bob", "Bob")
	platform := e.named(t, "platform", "Platform")
	e.named(t, "quiet", "Quiet One")
	mint := e.room(t, "MintA", "AAA", "agent")
	_ = e.repRepo.Upsert(context.Background(), &models.PeerReputation{PeerID: "agent", Tier: "trusted", Score: 120, BountiesWon: 2, AnswersAccepted: 3, UpvotesReceived: 10, ComputedAt: e.clock.Now()})

	agentMain := e.post(t, agent, map[string]interface{}{"body": "agent main"})
	e.clock.Advance(time.Minute)
	e.post(t, agent, map[string]interface{}{"body": "agent room", "room_mint": mint})
	e.clock.Advance(time.Minute)
	bobPost := e.post(t, bob, map[string]interface{}{"body": "bob post"})
	e.clock.Advance(time.Minute)
	last := e.clock.Now()
	agentReply := e.reply(t, agent, bobPost.ID, "agent answers")
	e.clock.Advance(time.Minute)
	e.reply(t, bob, agentMain.ID, "bob answers")

	var sum PortalBoardSummary
	w := e.do(t, http.MethodGet, "/api/peers/agent/board-summary", nil, "")
	if w.Code != http.StatusOK {
		t.Fatalf("summary: %d %s", w.Code, w.Body.String())
	}
	p1Data(t, w.Body.Bytes(), &sum)
	if sum.PeerID != "agent" || sum.DisplayName != "Agent Bot" || sum.ReputationTier != "trusted" ||
		sum.Posts != 2 || sum.Replies != 1 || sum.AcceptedAnswers != 3 || sum.BountiesWon != 2 {
		t.Errorf("summary = %+v", sum)
	}
	if sum.LastActiveAt == nil || *sum.LastActiveAt != last.UTC().Format(time.RFC3339) {
		t.Errorf("last_active_at = %v, want %s", sum.LastActiveAt, last.UTC().Format(time.RFC3339))
	}
	if strings.Contains(w.Body.String(), `"score"`) {
		t.Errorf("summary leaks the reputation score: %s", w.Body.String())
	}

	// Hidden content leaves the public counts; the newest time follows the visible rows.
	if w := e.do(t, http.MethodPost, "/api/board/replies/"+agentReply.ID+"/hide", nil, platform); w.Code != http.StatusOK {
		t.Fatalf("hide reply: %d %s", w.Code, w.Body.String())
	}
	if w := e.do(t, http.MethodPost, "/api/board/posts/"+agentMain.ID+"/hide", nil, platform); w.Code != http.StatusOK {
		t.Fatalf("hide post: %d %s", w.Code, w.Body.String())
	}
	p1Data(t, e.do(t, http.MethodGet, "/api/peers/agent/board-summary", nil, "").Body.Bytes(), &sum)
	if sum.Posts != 1 || sum.Replies != 0 || sum.LastActiveAt == nil || *sum.LastActiveAt != last.Add(-2*time.Minute).UTC().Format(time.RFC3339) {
		t.Errorf("summary after hide = %+v", sum)
	}

	// A known peer without board activity: zeros, tier new, null last_active_at.
	p1Data(t, e.do(t, http.MethodGet, "/api/peers/quiet/board-summary", nil, "").Body.Bytes(), &sum)
	if sum.DisplayName != "Quiet One" || sum.ReputationTier != "new" || sum.Posts != 0 || sum.Replies != 0 || sum.LastActiveAt != nil {
		t.Errorf("quiet summary = %+v", sum)
	}
	// Unknown peer: 404.
	if w := e.do(t, http.MethodGet, "/api/peers/nobody/board-summary", nil, ""); w.Code != http.StatusNotFound || errCode(t, w) != "NOT_FOUND" {
		t.Errorf("unknown peer: %d %s", w.Code, w.Body.String())
	}
}
