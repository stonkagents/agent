// Package api: server-side input hardening of the board write routes (hardening pass): body
// and title caps, tags, the bounty currency, HTML stripping and unicode safety, on the
// portal and the legacy daemon routes alike.
package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stonkagents/agent/tracker/internal/models"
)

func errorCodeAndMessage(t *testing.T, raw []byte) (string, string) {
	t.Helper()
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("error body %s: %v", raw, err)
	}
	return body.Error.Code, body.Error.Message
}

func TestBoardInput_BodyAndTitleCaps(t *testing.T) {
	e := newBoardEnv(t)
	alice := e.key(t, "alice")
	long := strings.Repeat("x", models.MaxBoardBodyRunes+1)

	for _, c := range []struct {
		name, path string
		body       map[string]interface{}
	}{
		{"portal post", "/api/board/posts", map[string]interface{}{"title": "t", "body": long, "category": "general"}},
		{"legacy post", "/api/v1/tracker/forum/posts", map[string]interface{}{"title": "t", "description": long}},
	} {
		w := e.do(t, http.MethodPost, c.path, c.body, alice)
		code, msg := errorCodeAndMessage(t, w.Body.Bytes())
		if w.Code != http.StatusBadRequest || code != "VALIDATION_ERROR" || msg != "body must be at most 10000 characters" {
			t.Errorf("%s over the cap = %d %s %q", c.name, w.Code, code, msg)
		}
	}
	post := e.post(t, alice, map[string]interface{}{"body": "a thread", "category": "general"})
	for _, c := range []struct{ name, path string }{
		{"portal reply", "/api/board/posts/" + post.ID + "/replies"},
		{"legacy reply", "/api/v1/tracker/forum/posts/" + post.ID + "/replies"},
	} {
		w := e.do(t, http.MethodPost, c.path, map[string]interface{}{"body": long}, alice)
		code, msg := errorCodeAndMessage(t, w.Body.Bytes())
		if w.Code != http.StatusBadRequest || code != "VALIDATION_ERROR" || msg != "body must be at most 10000 characters" {
			t.Errorf("%s over the cap = %d %s %q", c.name, w.Code, code, msg)
		}
	}
	// Exactly the cap is fine, in runes (multibyte characters count once).
	exact := strings.Repeat("\u65e5", models.MaxBoardBodyRunes)
	if w := e.do(t, http.MethodPost, "/api/board/posts/"+post.ID+"/replies", map[string]interface{}{"body": exact}, alice); w.Code != http.StatusOK {
		t.Errorf("reply at the cap = %d %s", w.Code, w.Body.String())
	}
	// An explicit title is one line of at most MaxBoardTitleRunes.
	created := e.post(t, alice, map[string]interface{}{"title": strings.Repeat("t", 300) + "\nsecond line", "body": "body text", "category": "general"})
	if len([]rune(created.Title)) != models.MaxBoardTitleRunes || strings.Contains(created.Title, "\n") {
		t.Errorf("title = %d runes %q", len([]rune(created.Title)), created.Title)
	}
}

func TestBoardInput_TagsBountyCurrencyAndLabel(t *testing.T) {
	e := newBoardEnv(t)
	alice := e.key(t, "alice")
	tags := make([]string, models.MaxBoardTags+1)
	for i := range tags {
		tags[i] = "tag" + strings.Repeat("x", i)
	}
	w := e.do(t, http.MethodPost, "/api/board/posts", map[string]interface{}{"body": "tagged", "category": "general", "tags": tags}, alice)
	if code, msg := errorCodeAndMessage(t, w.Body.Bytes()); w.Code != http.StatusBadRequest || code != "VALIDATION_ERROR" || msg != "tags must be at most 10 entries" {
		t.Errorf("too many tags = %d %s %q", w.Code, code, msg)
	}
	w = e.do(t, http.MethodPost, "/api/board/posts", map[string]interface{}{"body": "tagged", "category": "general", "tags": []string{strings.Repeat("t", models.MaxBoardTagRunes+1)}}, alice)
	if code, msg := errorCodeAndMessage(t, w.Body.Bytes()); w.Code != http.StatusBadRequest || code != "VALIDATION_ERROR" || msg != "tag must be at most 32 characters" {
		t.Errorf("long tag = %d %s %q", w.Code, code, msg)
	}
	// Empty, duplicate and tag-like tags are cleaned rather than refused.
	created := e.post(t, alice, map[string]interface{}{"body": "tagged", "category": "general", "tags": []string{" go ", "GO", "", "<b>rust</b>", "go"}})
	if strings.Join(created.Tags, ",") != "go,rust" {
		t.Errorf("tags = %v", created.Tags)
	}
	// Credits are the only bounty currency.
	w = e.do(t, http.MethodPost, "/api/board/posts", map[string]interface{}{"body": "paid", "category": "bounty", "bounty": map[string]interface{}{"amount": 10, "currency": "SOL"}}, alice)
	if code, _ := errorCodeAndMessage(t, w.Body.Bytes()); w.Code != http.StatusBadRequest || code != "VALIDATION_ERROR" {
		t.Errorf("bounty in SOL = %d %s", w.Code, code)
	}
	if p := e.post(t, alice, map[string]interface{}{"body": "paid", "category": "bounty", "bounty": map[string]interface{}{"amount": 10, "currency": "Credits"}}); p.Bounty == nil || p.Bounty.Currency != "credits" {
		t.Errorf("bounty currency = %+v", p.Bounty)
	}
	// The legacy token offer label is one short line without markup.
	p := e.post(t, alice, map[string]interface{}{"body": "offer", "category": "token-offer", "tokenOffer": map[string]interface{}{"amount": 5, "token": "<script>x</script>$" + strings.Repeat("B", 40)}})
	if p.TokenOffer == nil || p.TokenOffer.Token != "$"+strings.Repeat("B", models.MaxTokenOfferLabelRunes-1) {
		t.Errorf("token offer label = %+v", p.TokenOffer)
	}
}

func TestBoardInput_MarkupStrippedUnicodeKept(t *testing.T) {
	e := newBoardEnv(t)
	alice, bob := e.key(t, "alice"), e.key(t, "bob")
	body := "<script>alert(1)</script>Rocket \U0001F680 to \u65e5\u672c: if a < b {}<img src=x onerror=alert(1)>\u202E done"
	p := e.post(t, alice, map[string]interface{}{"body": body, "category": "general"})
	want := "Rocket \U0001F680 to \u65e5\u672c: if a < b {} done"
	if p.Content != want {
		t.Errorf("body = %q, want %q", p.Content, want)
	}
	if p.Title != want {
		t.Errorf("derived title = %q, want %q", p.Title, want)
	}
	w := e.do(t, http.MethodPost, "/api/board/posts/"+p.ID+"/replies", map[string]interface{}{"body": "<b>hi</b>\u200B there <a href=x>x</a>"}, bob)
	if w.Code != http.StatusOK {
		t.Fatalf("reply: %d %s", w.Code, w.Body.String())
	}
	var reply struct {
		Data PortalReply `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Data.Content != "hi there x" {
		t.Errorf("reply body = %q", reply.Data.Content)
	}
	// A body that is only markup is an empty body.
	w = e.do(t, http.MethodPost, "/api/board/posts/"+p.ID+"/replies", map[string]interface{}{"body": "<script>x</script><br/>"}, bob)
	if w.Code != http.StatusBadRequest {
		t.Errorf("markup-only reply = %d", w.Code)
	}
}
