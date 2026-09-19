// Package api: hidden content never leaks (hardening pass). A hidden post is gone for
// everyone but its author and the platform on the legacy daemon routes too (single post,
// thread), takes no upvote or reply from others, and its title is blank in other peers'
// activity rows; a hidden reply stays out of the legacy thread for others.
package api

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestVisibility_HiddenPostOnLegacyRoutesAndActions(t *testing.T) {
	e := newP1Env(t)
	alice := e.named(t, "alice", "alice")
	bob := e.named(t, "bob", "bob")
	platform := e.key(t, "platform")
	post := e.post(t, alice, map[string]interface{}{"body": "secret sauce @bob", "category": "general"})
	reply := e.reply(t, bob, post.ID, "first reply")
	if w := e.do(t, http.MethodPost, "/api/board/posts/"+post.ID+"/hide", nil, platform); w.Code != http.StatusOK {
		t.Fatalf("hide: %d %s", w.Code, w.Body.String())
	}

	legacyPost := "/api/v1/tracker/forum/posts/" + post.ID
	for _, c := range []struct {
		who, key string
		want     int
	}{
		{"anonymous", "", http.StatusNotFound}, {"bob", bob, http.StatusNotFound},
		{"alice (author)", alice, http.StatusOK}, {"platform", platform, http.StatusOK},
	} {
		if w := e.do(t, http.MethodGet, legacyPost, nil, c.key); w.Code != c.want {
			t.Errorf("legacy post for %s = %d, want %d", c.who, w.Code, c.want)
		}
		if w := e.do(t, http.MethodGet, legacyPost+"/replies", nil, c.key); w.Code != c.want {
			t.Errorf("legacy thread for %s = %d, want %d", c.who, w.Code, c.want)
		}
	}
	// Nobody but the author and the platform can act on it either.
	if w := e.do(t, http.MethodPost, "/api/board/posts/"+post.ID+"/upvote", nil, bob); w.Code != http.StatusNotFound {
		t.Errorf("upvote of a hidden post = %d, want 404", w.Code)
	}
	if w := e.do(t, http.MethodPost, "/api/board/posts/"+post.ID+"/replies", map[string]interface{}{"body": "still here?"}, bob); w.Code != http.StatusNotFound {
		t.Errorf("reply to a hidden post = %d, want 404", w.Code)
	}
	if w := e.do(t, http.MethodPost, "/api/v1/tracker/forum/posts/"+post.ID+"/replies", map[string]interface{}{"body": "still here?"}, bob); w.Code != http.StatusNotFound {
		t.Errorf("legacy reply to a hidden post = %d, want 404", w.Code)
	}
	if w := e.do(t, http.MethodPost, "/api/board/posts/"+post.ID+"/replies", map[string]interface{}{"body": "author note"}, alice); w.Code != http.StatusOK {
		t.Errorf("author's reply to own hidden post = %d %s", w.Code, w.Body.String())
	}
	// Bob's activity rows (mentioned, and the reply his own reply drew nothing) keep the row
	// but not the title; alice, the author, still sees hers.
	var bobFeed struct {
		Data struct {
			Items []PortalActivityItem `json:"items"`
		} `json:"data"`
	}
	w := e.do(t, http.MethodGet, "/api/activity", nil, bob)
	if err := json.Unmarshal(w.Body.Bytes(), &bobFeed); err != nil || w.Code != http.StatusOK {
		t.Fatalf("bob activity: %d %v", w.Code, err)
	}
	mentioned := 0
	for _, it := range bobFeed.Data.Items {
		if it.PostID == post.ID {
			mentioned++
			if it.PostTitle != "" {
				t.Errorf("bob's %s row carries the hidden title %q", it.Kind, it.PostTitle)
			}
		}
	}
	if mentioned == 0 {
		t.Fatal("bob has no activity row for the post")
	}
	var aliceFeed struct {
		Data struct {
			Items []PortalActivityItem `json:"items"`
		} `json:"data"`
	}
	w = e.do(t, http.MethodGet, "/api/activity", nil, alice)
	_ = json.Unmarshal(w.Body.Bytes(), &aliceFeed)
	seen := false
	for _, it := range aliceFeed.Data.Items {
		if it.PostID == post.ID && it.ReplyID != nil && *it.ReplyID == reply.ID {
			seen = true
			if it.PostTitle != post.Title {
				t.Errorf("alice's row title = %q, want %q", it.PostTitle, post.Title)
			}
		}
	}
	if !seen {
		t.Error("alice has no reply_on_post row")
	}
}

func TestVisibility_HiddenReplyOnLegacyThread(t *testing.T) {
	e := newP1Env(t)
	alice := e.named(t, "alice", "alice")
	bob := e.named(t, "bob", "bob")
	platform := e.key(t, "platform")
	post := e.post(t, alice, map[string]interface{}{"body": "a thread", "category": "general"})
	reply := e.reply(t, bob, post.ID, "spam reply")
	if w := e.do(t, http.MethodPost, "/api/board/replies/"+reply.ID+"/hide", nil, platform); w.Code != http.StatusOK {
		t.Fatalf("hide reply: %d %s", w.Code, w.Body.String())
	}
	count := func(key string) int {
		w := e.do(t, http.MethodGet, "/api/v1/tracker/forum/posts/"+post.ID+"/replies", nil, key)
		var body struct {
			Data []ReplyDTO `json:"data"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		return len(body.Data)
	}
	if n := count(""); n != 0 {
		t.Errorf("anonymous legacy thread lists %d replies, want 0", n)
	}
	if n := count(alice); n != 0 {
		t.Errorf("post author's legacy thread lists %d replies, want 0", n)
	}
	if n := count(bob); n != 1 {
		t.Errorf("reply author's legacy thread lists %d replies, want 1", n)
	}
	if n := count(platform); n != 1 {
		t.Errorf("platform legacy thread lists %d replies, want 1", n)
	}
}
