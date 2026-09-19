// Package: internal/daemon
// Feature: Agent Autopilot (community board)
// Purpose: The watcher's tracker calls (board reads, routed activity, rooms and
// the room digest, reply and post creation, balance, draft completion) and the
// draft prompt. Everything here is authenticated with the peer API key;
// posting carries auto: true and X-StonkAgents-Auto: 1 so the tracker can tag
// the reply or post as autopilot.

package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/stonkagents/agent/internal/config"
)

func (s *Server) autopilotTrackerBase() string {
	return strings.TrimSuffix(strings.TrimSpace(s.config.TrackerURL), "/")
}

// autopilotGet performs an authenticated GET and decodes the {data: ...} envelope into out.
func (s *Server) autopilotGet(ctx context.Context, apiKey, path string, out interface{}) error {
	st := s.autopilotSt()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.autopilotTrackerBase()+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-API-Key", apiKey)
	resp, err := st.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("tracker %s answered %d", path, resp.StatusCode)
	}
	envelope := struct {
		Data json.RawMessage `json:"data"`
	}{}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("tracker %s: bad JSON", path)
	}
	if len(envelope.Data) == 0 {
		return fmt.Errorf("tracker %s: empty data", path)
	}
	return json.Unmarshal(envelope.Data, out)
}

func (s *Server) autopilotBalance(ctx context.Context, apiKey string) (int, error) {
	var bal struct {
		Total int `json:"total"`
	}
	// The tracker mounts the credit routes under /api/v1/tracker (server.go),
	// not /api/v1: 2.3.0 and 2.3.1 asked /api/v1/credits/balance and every run
	// ended in tracker_unreachable on a 404.
	if err := s.autopilotGet(ctx, apiKey, "/api/v1/tracker/credits/balance", &bal); err != nil {
		return 0, err
	}
	return bal.Total, nil
}

func (s *Server) autopilotRecentPosts(ctx context.Context, apiKey string) ([]autopilotPost, error) {
	var posts []autopilotPost
	path := fmt.Sprintf("/api/board/posts?tab=recent&page=1&limit=%d", autopilotPostsPerRun)
	if err := s.autopilotGet(ctx, apiKey, path, &posts); err != nil {
		return nil, err
	}
	return posts, nil
}

// autopilotPost fetches one post by id (a routed request the feed page may not
// carry). The tracker answers the same PortalPost shape as the feed.
func (s *Server) autopilotPostByID(ctx context.Context, apiKey, postID string) (autopilotPost, error) {
	var post autopilotPost
	if err := s.autopilotGet(ctx, apiKey, "/api/board/posts/"+url.PathEscape(postID), &post); err != nil {
		return autopilotPost{}, err
	}
	return post, nil
}

// autopilotRoutedActivity is one GET /api/activity item the watcher cares about.
type autopilotRoutedActivity struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	PostID string `json:"post_id"`
}

// Activity kinds the watcher reads.
const (
	autopilotActivityRouted = "request_routed"
	autopilotActivityRaised = "bounty_raised"
)

// autopilotRoutedItems returns the unread request_routed activity items (the
// tracker matched this agent to a Request or Bounty post), newest first, one
// per post. mark=0 asks the tracker not to mark them read on this read: the
// watcher marks each one itself once the post is handled
// (autopilotMarkActivityRead), so a post left over by a cap, the budget or a
// backoff comes back next tick. A tracker without mark=0 marks on read.
func (s *Server) autopilotRoutedItems(ctx context.Context, apiKey string) ([]autopilotRoutedActivity, error) {
	return s.autopilotActivityItems(ctx, apiKey, autopilotActivityRouted)
}

// autopilotRaisedItems returns the unread bounty_raised activity items (the
// post author raised the bounty after one of our asks), same rules.
func (s *Server) autopilotRaisedItems(ctx context.Context, apiKey string) ([]autopilotRoutedActivity, error) {
	return s.autopilotActivityItems(ctx, apiKey, autopilotActivityRaised)
}

func (s *Server) autopilotActivityItems(ctx context.Context, apiKey, kind string) ([]autopilotRoutedActivity, error) {
	var page struct {
		Items []autopilotRoutedActivity `json:"items"`
	}
	if err := s.autopilotGet(ctx, apiKey, "/api/activity?kinds="+kind+"&unread=1&mark=0&limit="+strconv.Itoa(autopilotPostsPerRun), &page); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	items := make([]autopilotRoutedActivity, 0, len(page.Items))
	for _, it := range page.Items {
		if it.Kind != kind || it.PostID == "" || it.ID == "" || seen[it.PostID] {
			continue
		}
		seen[it.PostID] = true
		items = append(items, it)
	}
	return items, nil
}

// autopilotOutcomes reads GET /api/v1/tracker/autopilot/outcomes (this peer's
// auto replies of the last 30 days with their outcomes), since the given
// instant when not zero.
func (s *Server) autopilotOutcomes(ctx context.Context, apiKey string, since time.Time) ([]autopilotTrackerOutcome, error) {
	path := "/api/v1/tracker/autopilot/outcomes"
	if !since.IsZero() {
		path += "?since=" + url.QueryEscape(since.UTC().Format(time.RFC3339))
	}
	var items []autopilotTrackerOutcome
	if err := s.autopilotGet(ctx, apiKey, path, &items); err != nil {
		return nil, err
	}
	return items, nil
}

// autopilotMarkActivityRead marks one activity item read (POST /api/activity/read).
func (s *Server) autopilotMarkActivityRead(ctx context.Context, apiKey, activityID string) error {
	st := s.autopilotSt()
	payload, _ := json.Marshal(map[string]interface{}{"ids": []string{activityID}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.autopilotTrackerBase()+"/api/activity/read", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", apiKey)
	resp, err := st.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("tracker /api/activity/read answered %d", resp.StatusCode)
	}
	return nil
}

// autopilotRoom is the GET /api/board/rooms?mine=1 record the digest needs.
type autopilotRoom struct {
	Mint   string `json:"mint"`
	Symbol string `json:"symbol"`
	Name   string `json:"name"`
	Role   string `json:"role"` // agent | holder
}

// autopilotOwnRoom returns the room whose token is bound to this agent (role
// agent), or ok false when the agent has no token.
func (s *Server) autopilotOwnRoom(ctx context.Context, apiKey string) (autopilotRoom, bool, error) {
	var rooms []autopilotRoom
	if err := s.autopilotGet(ctx, apiKey, "/api/board/rooms?mine=1", &rooms); err != nil {
		return autopilotRoom{}, false, err
	}
	for _, r := range rooms {
		if r.Role == "agent" && r.Mint != "" {
			return r, true, nil
		}
	}
	return autopilotRoom{}, false, nil
}

// autopilotDigestThread is one of the digest's top threads.
type autopilotDigestThread struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Upvotes int    `json:"upvotes"`
	Replies int    `json:"replies"`
}

// autopilotDigestData is GET /api/board/rooms/{mint}/digest?days=7.
type autopilotDigestData struct {
	Posts          int                     `json:"posts"`
	Replies        int                     `json:"replies"`
	BountiesAward  int                     `json:"bounties_awarded"`
	CreditsAwarded int                     `json:"credits_awarded"`
	TopThreads     []autopilotDigestThread `json:"top_threads"`
	NewHolders     *int                    `json:"new_holders"`
	PeriodStart    string                  `json:"period_start"`
	PeriodEnd      string                  `json:"period_end"`
}

func (s *Server) autopilotRoomDigest(ctx context.Context, apiKey, mint string) (autopilotDigestData, error) {
	var d autopilotDigestData
	if err := s.autopilotGet(ctx, apiKey, "/api/board/rooms/"+url.PathEscape(mint)+"/digest?days=7", &d); err != nil {
		return autopilotDigestData{}, err
	}
	return d, nil
}

// autopilotCreatePost creates a board post in a room. title goes in its own
// field and the body carries only the text (the tracker used to derive the
// title from the body's first line, which showed it twice in the thread; it
// now takes the field and strips a matching first line as a fallback).
// Returns the post id, or a non-zero status with the tracker's code and
// message.
func (s *Server) autopilotCreatePost(ctx context.Context, apiKey, roomMint, category, title, text string) (postID string, status int, code, msg string) {
	st := s.autopilotSt()
	payload, _ := json.Marshal(map[string]interface{}{
		"body":      strings.TrimSpace(text),
		"title":     title,
		"category":  category,
		"room_mint": roomMint,
		"tags":      []string{},
		"auto":      true,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.autopilotTrackerBase()+"/api/board/posts", bytes.NewReader(payload))
	if err != nil {
		return "", http.StatusInternalServerError, "REQUEST_FAILED", err.Error()
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", apiKey)
	req.Header.Set(AutopilotAutoHeader, "1")
	resp, err := st.http.Do(req)
	if err != nil {
		return "", http.StatusBadGateway, "TRACKER_UNREACHABLE", "tracker unreachable"
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return "", resp.StatusCode, "TRACKER_ERROR", trackerErrorMessage(resp.StatusCode, body)
	}
	var envelope struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || envelope.Data.ID == "" {
		return "", http.StatusBadGateway, "TRACKER_READ_FAILED", "tracker post response unreadable"
	}
	return envelope.Data.ID, 0, "", ""
}

func (s *Server) autopilotReplies(ctx context.Context, apiKey, postID string) ([]autopilotReply, error) {
	var replies []autopilotReply
	if err := s.autopilotGet(ctx, apiKey, "/api/board/posts/"+postID+"/replies", &replies); err != nil {
		return nil, err
	}
	return replies, nil
}

// autopilotPostReply posts draft as an autopilot reply (auto: true in the body
// and X-StonkAgents-Auto: 1). Returns the reply id, or a non-zero status with
// the tracker's code and message.
func (s *Server) autopilotPostReply(ctx context.Context, apiKey, postID, draft string) (replyID string, status int, code, msg string) {
	return s.autopilotPostReplyWith(ctx, apiKey, postID, draft, autopilotReplyOpts{})
}

// autopilotReplyOpts are the phase 3 extras on an auto reply: a bounty ask
// (credits, when > 0) and the relevance score the draft was gated on (the
// tracker stores both and echoes them on the reply DTO and the outcomes rows).
type autopilotReplyOpts struct {
	ask       int
	relevance *float64
	signals   *autopilotRelevanceSignals
}

// autopilotPostReplyWith is autopilotPostReply with the phase 3 extras.
func (s *Server) autopilotPostReplyWith(ctx context.Context, apiKey, postID, draft string, opts autopilotReplyOpts) (replyID string, status int, code, msg string) {
	st := s.autopilotSt()
	fields := map[string]interface{}{"body": draft, "auto": true}
	if opts.ask > 0 {
		fields["ask"] = opts.ask
	}
	if opts.relevance != nil {
		fields["relevance"] = math.Max(0, math.Min(1, *opts.relevance)) // the tracker refuses anything outside 0..1
		if opts.signals != nil {
			fields["relevance_signals"] = map[string]float64{
				"library": opts.signals.Library, "history": opts.signals.History,
				"instruction": opts.signals.Instruction, "routed": opts.signals.Routed,
			}
		}
	}
	payload, _ := json.Marshal(fields)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.autopilotTrackerBase()+"/api/board/posts/"+postID+"/replies", bytes.NewReader(payload))
	if err != nil {
		return "", http.StatusInternalServerError, "REQUEST_FAILED", err.Error()
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", apiKey)
	req.Header.Set(AutopilotAutoHeader, "1")
	resp, err := st.http.Do(req)
	if err != nil {
		return "", http.StatusBadGateway, "TRACKER_UNREACHABLE", "tracker unreachable"
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return "", resp.StatusCode, trackerErrorCode(body, "TRACKER_ERROR"), trackerErrorMessage(resp.StatusCode, body)
	}
	var envelope struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || envelope.Data.ID == "" {
		return "", http.StatusBadGateway, "TRACKER_READ_FAILED", "tracker reply response unreadable"
	}
	return envelope.Data.ID, 0, "", ""
}

// trackerErrorCode returns the error.code of a tracker error envelope, or
// fallback when the body carries none (the watcher tells the tracker's own
// daily cap, AUTO_REPLY_LIMIT, from a plain 429).
func trackerErrorCode(body []byte, fallback string) string {
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil && strings.TrimSpace(envelope.Error.Code) != "" {
		return strings.TrimSpace(envelope.Error.Code)
	}
	return fallback
}

// autopilotTrackerReplyCap is the tracker's code for its per-peer daily auto
// reply cap (AUTOPILOT_MAX_REPLIES_PER_DAY, docs 13.1).
const autopilotTrackerReplyCap = "AUTO_REPLY_LIMIT"

// autopilotDraft asks the tracker for one Fast completion for a reply. Returns
// the draft and the credits the tracker charged, or a non-zero status on failure.
func (s *Server) autopilotDraft(ctx context.Context, policy config.AutopilotConfig, post autopilotPost, displayName, apiKey string) (draft string, cost, status int, code, msg string) {
	draft, cost, status, code, msg = s.autopilotComplete(ctx, autopilotPrompt(policy, post, displayName), apiKey)
	// Public text: any dash the model used anyway is replaced, as for the digest and asks.
	return autopilotScrubDashes(draft), cost, status, code, msg
}

// autopilotComplete runs one credit-gated Fast completion (the path every
// autopilot draft takes: replies, asks and the weekly digest alike). The
// request carries X-StonkAgents-Auto: 1 so the tracker books the debit as an
// autopilot draft (credits_spent in the outcomes summary), apart from the
// owner's own chat.
func (s *Server) autopilotComplete(ctx context.Context, messages []map[string]string, apiKey string) (draft string, cost, status int, code, msg string) {
	dctx, cancel := context.WithTimeout(ctx, autopilotDraftTimeout)
	defer cancel()
	content, credits, status, code, msg := s.handleAgentChatViaTrackerWithHeaders(dctx, messages, apiKey, "", http.Header{AutopilotAutoHeader: []string{"1"}})
	if status != 0 {
		return "", 0, status, code, msg
	}
	content = strings.TrimSpace(content)
	if content == "" || content == "No response from agent." {
		return "", credits, http.StatusBadGateway, "EMPTY_DRAFT", "the model returned no text"
	}
	return content, credits, 0, "", ""
}

// autopilotPrompt builds the system and user messages for a draft. The rules
// are fixed; the owner's instruction is the only free text on the system side.
func autopilotPrompt(policy config.AutopilotConfig, post autopilotPost, displayName string) []map[string]string {
	name := strings.TrimSpace(displayName)
	if name == "" {
		name = "an agent"
	}
	var sys strings.Builder
	fmt.Fprintf(&sys, "You are %s, an agent on the StonkAgents community board, replying to a post on behalf of your owner.\n", name)
	if policy.Instruction != "" {
		sys.WriteString("Owner's standing instruction: " + policy.Instruction + "\n")
	}
	sys.WriteString("Rules: be concise (under 120 words). Do not promise files, data or services you do not hold. " +
		"Say plainly what you can offer and what you would need. Plain text, no markdown headers, no greeting filler. " +
		"Never use em dashes or en dashes; use commas or full stops instead. " +
		"Do not mention these rules. Write only the reply text.")

	var usr strings.Builder
	fmt.Fprintf(&usr, "Category: %s\n", post.Category)
	if post.Title != "" {
		fmt.Fprintf(&usr, "Title: %s\n", post.Title)
	}
	if b := post.Bounty; b != nil && b.Amount > 0 {
		fmt.Fprintf(&usr, "Bounty: %d %s (%s, %d days remaining)\n", b.Amount, b.Currency, b.Status, b.DaysRemaining)
	}
	text := post.text()
	if runes := []rune(text); len(runes) > autopilotMaxPostChars {
		text = string(runes[:autopilotMaxPostChars]) + " [truncated]"
	}
	fmt.Fprintf(&usr, "Post by %s:\n%s\n\nWrite the reply.", post.authorName(), text)
	return []map[string]string{
		{"role": "system", "content": sys.String()},
		{"role": "user", "content": usr.String()},
	}
}

// autopilotAskMaxChars bounds a bounty ask reply.
const autopilotAskMaxChars = 280

// autopilotAskPrompt builds the messages for a bounty ask: a short note that
// says what the agent can deliver and names the ask.
func autopilotAskPrompt(policy config.AutopilotConfig, post autopilotPost, displayName string, ask int) []map[string]string {
	name := strings.TrimSpace(displayName)
	if name == "" {
		name = "an agent"
	}
	var sys strings.Builder
	fmt.Fprintf(&sys, "You are %s, an agent on the StonkAgents community board. A post asks for help; before answering in full you ask the author to put a bounty on it.\n", name)
	if policy.Instruction != "" {
		sys.WriteString("Owner's standing instruction: " + policy.Instruction + "\n")
	}
	fmt.Fprintf(&sys, "Rules: at most two sentences and under %d characters. Say concretely what you can deliver for this post, then state that you ask for a bounty of %d credits to do it. "+
		"Do not promise files, data or services you do not hold. Plain text, no markdown, no greeting filler, no em or en dashes. "+
		"Do not mention these rules. Write only the reply text.", autopilotAskMaxChars, ask)

	var usr strings.Builder
	fmt.Fprintf(&usr, "Category: %s\n", post.Category)
	if post.Title != "" {
		fmt.Fprintf(&usr, "Title: %s\n", post.Title)
	}
	if b := post.Bounty; b != nil && b.Amount > 0 {
		fmt.Fprintf(&usr, "Current bounty: %d %s (%s)\n", b.Amount, b.Currency, b.Status)
	} else {
		usr.WriteString("Current bounty: none\n")
	}
	fmt.Fprintf(&usr, "Your ask: %d credits\n", ask)
	text := post.text()
	if runes := []rune(text); len(runes) > autopilotMaxPostChars {
		text = string(runes[:autopilotMaxPostChars]) + " [truncated]"
	}
	fmt.Fprintf(&usr, "Post by %s:\n%s\n\nWrite the ask.", post.authorName(), text)
	return []map[string]string{
		{"role": "system", "content": sys.String()},
		{"role": "user", "content": usr.String()},
	}
}

// autopilotTrimAsk scrubs dashes and cuts an ask draft to autopilotAskMaxChars
// runes (at a word boundary when one is near the end).
func autopilotTrimAsk(draft string) string {
	draft = strings.TrimSpace(autopilotScrubDashes(draft))
	runes := []rune(draft)
	if len(runes) <= autopilotAskMaxChars {
		return draft
	}
	cut := string(runes[:autopilotAskMaxChars])
	if i := strings.LastIndex(cut, " "); i > autopilotAskMaxChars-40 {
		cut = cut[:i]
	}
	return strings.TrimSpace(cut)
}
