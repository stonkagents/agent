// Package api: title and body resolution on the two create-post routes (dev seeding
// hardening): an explicit title wins, content aliases body, an empty body is a 400, the
// digest's repeated first line is dropped, and the P2P rank is exposed as p2p_tier next to
// authorTier on posts and replies.
package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/stonkagents/agent/tracker/internal/reputation"
)

func TestResolvePostTitleAndBody(t *testing.T) {
	long := strings.Repeat("x", 250)
	cases := []struct {
		name       string
		title      string
		candidates []string
		wantTitle  string
		wantBody   string
		wantErr    error
	}{
		{name: "explicit title wins over the first line", title: " My title ", candidates: []string{"first line\nsecond"}, wantTitle: "My title", wantBody: "first line\nsecond"},
		{name: "content is an alias of body", title: "", candidates: []string{"", "from content"}, wantTitle: "from content", wantBody: "from content"},
		{name: "body wins over content", title: "", candidates: []string{"from body", "from content"}, wantTitle: "from body", wantBody: "from body"},
		{name: "empty body", title: "T", candidates: []string{"  ", "\n"}, wantErr: ErrPostBodyEmpty},
		{name: "derived title is the first line", title: "", candidates: []string{"Question here\n\nDetails"}, wantTitle: "Question here", wantBody: "Question here\n\nDetails"},
		{name: "derived title capped at 200 runes", title: "", candidates: []string{long + "\nrest"}, wantTitle: long[:200], wantBody: long + "\nrest"},
		{name: "explicit title capped and collapsed to one line", title: "a\nb  c" + long, candidates: []string{"body"}, wantTitle: ("a b c" + long)[:200], wantBody: "body"},
		{name: "digest: title equal to the first line is stripped from the body", title: "AAA weekly digest", candidates: []string{"AAA weekly digest\n\n4 posts, 9 replies"}, wantTitle: "AAA weekly digest", wantBody: "4 posts, 9 replies"},
		{name: "single line body equal to the title is kept", title: "hello", candidates: []string{"hello"}, wantTitle: "hello", wantBody: "hello"},
		{name: "title differing from the first line strips nothing", title: "Other", candidates: []string{"hello\nworld"}, wantTitle: "Other", wantBody: "hello\nworld"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			title, body, err := resolvePostTitleAndBody(tc.title, tc.candidates...)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if title != tc.wantTitle || body != tc.wantBody {
				t.Errorf("got title %q body %q, want %q / %q", title, body, tc.wantTitle, tc.wantBody)
			}
		})
	}
}

func TestPortalCreatePost_TitleAndContentFields(t *testing.T) {
	e := newBoardEnv(t)
	key := e.key(t, "alice")

	// title + content (the seeding client's shape) keeps the title and stores content as body.
	p := e.post(t, key, map[string]interface{}{"title": "Seeded title", "content": "Seeded body\nwith two lines", "category": "general"})
	if p.Title != "Seeded title" || p.Content != "Seeded body\nwith two lines" {
		t.Errorf("title+content: got %q / %q", p.Title, p.Content)
	}
	// Daemon digest shape: the title repeated as the body's first line is dropped.
	p = e.post(t, key, map[string]interface{}{"title": "AAA weekly digest", "body": "AAA weekly digest\n\n4 posts", "category": "general"})
	if p.Title != "AAA weekly digest" || p.Content != "4 posts" {
		t.Errorf("digest: got %q / %q", p.Title, p.Content)
	}
	// No title: derived from the first line, as before.
	p = e.post(t, key, map[string]interface{}{"body": "First line\nrest", "category": "general"})
	if p.Title != "First line" || p.Content != "First line\nrest" {
		t.Errorf("derived: got %q / %q", p.Title, p.Content)
	}
	// Empty body is a 400 naming the problem, whatever the title.
	w := e.do(t, http.MethodPost, "/api/board/posts", map[string]interface{}{"title": "Only a title", "category": "general"}, key)
	if w.Code != http.StatusBadRequest || errorCode(t, w) != "VALIDATION_ERROR" || !strings.Contains(w.Body.String(), "post body is empty") {
		t.Errorf("empty body: %d %s", w.Code, w.Body.String())
	}
}

func TestForumV1CreatePost_TitleAndBodyAliases(t *testing.T) {
	e := newBoardEnv(t)
	key := e.key(t, "alice")
	create := func(body map[string]interface{}) (PostDTO, int, string) {
		w := e.do(t, http.MethodPost, "/api/v1/tracker/forum/posts", body, key)
		var dto PostDTO
		_ = json.Unmarshal(w.Body.Bytes(), &dto)
		return dto, w.Code, w.Body.String()
	}
	if dto, code, raw := create(map[string]interface{}{"title": "T", "body": "T\n\nthe body"}); code != http.StatusCreated || dto.Title != "T" || dto.Description != "the body" {
		t.Errorf("title+body: %d %s", code, raw)
	}
	if dto, code, raw := create(map[string]interface{}{"content": "Only content\nmore"}); code != http.StatusCreated || dto.Title != "Only content" || dto.Description != "Only content\nmore" {
		t.Errorf("content only: %d %s", code, raw)
	}
	if dto, code, raw := create(map[string]interface{}{"title": "T", "description": "desc"}); code != http.StatusCreated || dto.Title != "T" || dto.Description != "desc" {
		t.Errorf("legacy shape: %d %s", code, raw)
	}
	if _, code, raw := create(map[string]interface{}{"title": "T"}); code != http.StatusBadRequest || !strings.Contains(raw, "post body is empty") {
		t.Errorf("empty body: %d %s", code, raw)
	}
}

func TestPortalPostAndReply_P2PTierNextToAuthorTier(t *testing.T) {
	e := newP1Env(t)
	alice := e.key(t, "alice")
	bob := e.key(t, "bob")
	// bob is silver on the P2P rank; alice has no record (new).
	if err := e.rep.Upsert(t.Context(), &reputation.ReputationRecord{PeerID: "bob", CompositeScore: reputation.RankSilverThreshold, UpdatedAt: e.clock.Now()}); err != nil {
		t.Fatalf("rep: %v", err)
	}
	p := e.post(t, alice, map[string]interface{}{"body": "question", "category": "general"})
	if p.AuthorTier != reputation.RankNew || p.P2PTier != p.AuthorTier || p.ReputationTier != "new" {
		t.Errorf("post tiers: authorTier=%q p2p_tier=%q reputation_tier=%q", p.AuthorTier, p.P2PTier, p.ReputationTier)
	}
	e.reply(t, bob, p.ID, "answer")
	replies := e.thread(t, p.ID, "")
	if len(replies) != 1 || replies[0].AuthorTier != reputation.RankSilver || replies[0].P2PTier != reputation.RankSilver || replies[0].ReputationTier != "new" {
		t.Fatalf("reply tiers: %+v", replies)
	}
	// Both names are on the wire.
	w := e.do(t, http.MethodGet, "/api/board/posts/"+p.ID+"/replies", nil, "")
	var raw struct {
		Data []map[string]json.RawMessage `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &raw)
	if len(raw.Data) != 1 || string(raw.Data[0]["authorTier"]) != `"silver"` || string(raw.Data[0]["p2p_tier"]) != `"silver"` {
		t.Errorf("reply wire shape: %s", w.Body.String())
	}
}
