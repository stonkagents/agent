package services

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
)

func sampleFeedback() *models.Feedback {
	return &models.Feedback{
		ID: 7, Kind: models.FeedbackKindBug, Message: "Launch button does nothing", Path: "/launch",
		Contact: "@tev", ContactVia: "telegram", WalletAddress: "Wa11et111111111111111111111111111111111111",
		CreatedAt: time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC),
	}
}

func TestNewFeedbackForwarder_DisabledWithoutURLOrChatID(t *testing.T) {
	if f := NewFeedbackForwarder(FeedbackForwarderConfig{}, nil); f != nil {
		t.Errorf("no URL: want nil forwarder, got %+v", f)
	}
	if f := NewFeedbackForwarder(FeedbackForwarderConfig{WebhookURL: "https://x", Kind: "telegram"}, nil); f != nil {
		t.Errorf("telegram without chat id: want nil forwarder, got %+v", f)
	}
	if f := NewFeedbackForwarder(FeedbackForwarderConfig{WebhookURL: "https://x"}, nil); f == nil || f.Kind() != FeedbackWebhookDiscord {
		t.Errorf("URL only: want a discord forwarder, got %+v", f)
	}
}

func TestFeedbackForwarder_DiscordShape(t *testing.T) {
	var got map[string]interface{}
	var contentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contentType = r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	f := NewFeedbackForwarder(FeedbackForwarderConfig{WebhookURL: srv.URL}, nil)
	if err := f.Forward(context.Background(), sampleFeedback()); err != nil {
		t.Fatalf("Forward: %v", err)
	}
	if contentType != "application/json" {
		t.Errorf("Content-Type = %q", contentType)
	}
	if got["content"] != "New feedback (bug) #7" {
		t.Errorf("content = %v", got["content"])
	}
	embeds, _ := got["embeds"].([]interface{})
	if len(embeds) != 1 {
		t.Fatalf("embeds = %v", got["embeds"])
	}
	embed := embeds[0].(map[string]interface{})
	if embed["description"] != "Launch button does nothing" || embed["title"] != "[bug] feedback #7" || embed["timestamp"] != "2026-09-13T10:00:00Z" {
		t.Errorf("embed = %v", embed)
	}
	fields := embed["fields"].([]interface{})
	if len(fields) != 3 {
		t.Fatalf("fields = %v", fields)
	}
	if f0 := fields[0].(map[string]interface{}); f0["name"] != "Path" || f0["value"] != "/launch" {
		t.Errorf("field[0] = %v", f0)
	}
	if f1 := fields[1].(map[string]interface{}); f1["value"] != "telegram: @tev" {
		t.Errorf("field[1] = %v", f1)
	}
}

func TestFeedbackForwarder_TelegramShape(t *testing.T) {
	var got map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	f := NewFeedbackForwarder(FeedbackForwarderConfig{WebhookURL: srv.URL, Kind: "Telegram", TelegramChatID: "-100123"}, nil)
	if f == nil || f.Kind() != FeedbackWebhookTelegram {
		t.Fatalf("forwarder = %+v", f)
	}
	if err := f.Forward(context.Background(), sampleFeedback()); err != nil {
		t.Fatalf("Forward: %v", err)
	}
	if got["chat_id"] != "-100123" || got["disable_web_page_preview"] != true {
		t.Errorf("payload = %v", got)
	}
	text, _ := got["text"].(string)
	want := "New feedback (bug) #7\n\nLaunch button does nothing\n\nPath: /launch\nContact: telegram: @tev\nWallet: Wa11et111111111111111111111111111111111111"
	if text != want {
		t.Errorf("text = %q\nwant %q", text, want)
	}
	if _, has := got["embeds"]; has {
		t.Errorf("telegram payload must not carry discord embeds")
	}
}

func TestFeedbackForwarder_ErrorsAreReturnedNotPanics(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad webhook", http.StatusBadRequest)
	}))
	defer srv.Close()
	f := NewFeedbackForwarder(FeedbackForwarderConfig{WebhookURL: srv.URL}, nil)
	if err := f.Forward(context.Background(), sampleFeedback()); !errors.Is(err, ErrFeedbackWebhookRejected) {
		t.Errorf("400 -> err = %v, want ErrFeedbackWebhookRejected", err)
	}
	srv.Close()
	if err := f.Forward(context.Background(), sampleFeedback()); err == nil {
		t.Errorf("closed server -> want a transport error")
	}
}

func TestBuildFeedbackWebhookPayload_OmitsEmptyFields(t *testing.T) {
	fb := &models.Feedback{ID: 1, Kind: "idea", Message: "m"}
	p := BuildFeedbackWebhookPayload(FeedbackWebhookDiscord, "", fb)
	fields := p["embeds"].([]map[string]interface{})[0]["fields"].([]map[string]interface{})
	if len(fields) != 0 {
		t.Errorf("fields = %v, want none", fields)
	}
	if text := BuildFeedbackWebhookPayload(FeedbackWebhookTelegram, "c", fb)["text"]; text != "New feedback (idea) #1\n\nm" {
		t.Errorf("text = %q", text)
	}
}

func sampleInterest() *models.AgentInterest {
	return &models.AgentInterest{
		ID: 9, Capabilities: []string{"trade", "alerts"}, Description: "Snipe launches for me", Priority: "pay",
		Path: "/", Contact: "@tev", ContactVia: "telegram", WalletAddress: "WaLLetForRoadmapInterestTestsOnlyAAAAAAAAAAA",
		CreatedAt: time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC),
	}
}

func TestBuildInterestWebhookPayload_DistinctTitleAndShapes(t *testing.T) {
	it := sampleInterest()
	p := BuildInterestWebhookPayload(FeedbackWebhookDiscord, "", it)
	if p["content"] != "New roadmap interest #9 (pay)" {
		t.Errorf("content = %v", p["content"])
	}
	embed := p["embeds"].([]map[string]interface{})[0]
	if embed["title"] != "[roadmap] interest #9" || embed["description"] != "Snipe launches for me" || embed["timestamp"] != "2026-09-13T10:00:00Z" {
		t.Errorf("embed = %v", embed)
	}
	fields := embed["fields"].([]map[string]interface{})
	if len(fields) != 5 || fields[0]["name"] != "Capabilities" || fields[0]["value"] != "Trade for me (sniper / copy trades), Send me alerts" ||
		fields[1]["value"] != "pay" || fields[2]["value"] != "/" || fields[3]["value"] != "telegram: @tev" {
		t.Errorf("fields = %v", fields)
	}

	bare := &models.AgentInterest{ID: 2, Capabilities: []string{"learn"}, Priority: "nice"}
	p = BuildInterestWebhookPayload(FeedbackWebhookDiscord, "", bare)
	embed = p["embeds"].([]map[string]interface{})[0]
	if embed["description"] != "(no description)" {
		t.Errorf("empty description must read as such, got %v", embed["description"])
	}
	if fields := embed["fields"].([]map[string]interface{}); len(fields) != 2 {
		t.Errorf("bare fields = %v, want Capabilities and Priority only", fields)
	}

	tg := BuildInterestWebhookPayload(FeedbackWebhookTelegram, "-100123", it)
	want := "New roadmap interest #9 (pay)\n\nCapabilities: Trade for me (sniper / copy trades), Send me alerts\n\nSnipe launches for me\n\nPath: /\nContact: telegram: @tev\nWallet: WaLLetForRoadmapInterestTestsOnlyAAAAAAAAAAA"
	if tg["chat_id"] != "-100123" || tg["text"] != want {
		t.Errorf("telegram = %v\nwant text %q", tg, want)
	}
	if _, has := tg["embeds"]; has {
		t.Errorf("telegram payload must not carry discord embeds")
	}
}

func TestFeedbackForwarder_ForwardInterestPosts(t *testing.T) {
	var got map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	f := NewFeedbackForwarder(FeedbackForwarderConfig{WebhookURL: srv.URL}, nil)
	if err := f.ForwardInterest(context.Background(), sampleInterest()); err != nil {
		t.Fatalf("ForwardInterest: %v", err)
	}
	if got["content"] != "New roadmap interest #9 (pay)" {
		t.Errorf("content = %v", got["content"])
	}
}
