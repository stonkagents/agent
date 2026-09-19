// Package: tracker/internal/services
// Feature: StonkAgents portal feedback
// Purpose: Best-effort forwarding of each feedback submission to a chat webhook.
//          Discord webhook JSON by default; Telegram sendMessage JSON when configured.

package services

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/stonkagents/agent/tracker/internal/models"
)

// Feedback webhook kinds (FEEDBACK_WEBHOOK_KIND).
const (
	FeedbackWebhookDiscord  = "discord"
	FeedbackWebhookTelegram = "telegram"

	// DefaultFeedbackForwardTimeout bounds one webhook call.
	DefaultFeedbackForwardTimeout = 5 * time.Second
	// feedbackForwardBodyLimit caps how much of an error body is read for the log line.
	feedbackForwardBodyLimit = 512
)

// ErrFeedbackWebhookRejected is returned when the webhook answers a non-2xx status.
var ErrFeedbackWebhookRejected = errors.New("feedback webhook rejected the payload")

// FeedbackForwarderConfig configures the forwarder from env:
//
//	FEEDBACK_WEBHOOK_URL       target URL; empty disables forwarding
//	FEEDBACK_WEBHOOK_KIND      "discord" (default) | "telegram"
//	FEEDBACK_TELEGRAM_CHAT_ID  chat_id for the Telegram sendMessage body
type FeedbackForwarderConfig struct {
	WebhookURL     string
	Kind           string
	TelegramChatID string
	Timeout        time.Duration
}

// FeedbackForwarder posts feedback submissions to a chat webhook.
type FeedbackForwarder struct {
	cfg    FeedbackForwarderConfig
	client *http.Client
	logger *slog.Logger
}

// NewFeedbackForwarder builds a forwarder. Returns nil (disabled) when WebhookURL is empty or
// the Telegram kind has no chat id, so callers can pass it straight into an interface field
// after a nil check.
func NewFeedbackForwarder(cfg FeedbackForwarderConfig, logger *slog.Logger) *FeedbackForwarder {
	cfg.WebhookURL = strings.TrimSpace(cfg.WebhookURL)
	cfg.Kind = strings.ToLower(strings.TrimSpace(cfg.Kind))
	cfg.TelegramChatID = strings.TrimSpace(cfg.TelegramChatID)
	if cfg.Kind == "" {
		cfg.Kind = FeedbackWebhookDiscord
	}
	if cfg.WebhookURL == "" || (cfg.Kind == FeedbackWebhookTelegram && cfg.TelegramChatID == "") {
		return nil
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultFeedbackForwardTimeout
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &FeedbackForwarder{cfg: cfg, client: &http.Client{Timeout: cfg.Timeout}, logger: logger}
}

// Kind returns the payload shape in use.
func (f *FeedbackForwarder) Kind() string { return f.cfg.Kind }

// Forward posts fb to the webhook. The error is for the caller to log; it must never reach
// the user, and the submission is already stored before Forward is called.
func (f *FeedbackForwarder) Forward(ctx context.Context, fb *models.Feedback) error {
	return f.post(ctx, BuildFeedbackWebhookPayload(f.cfg.Kind, f.cfg.TelegramChatID, fb))
}

// ForwardInterest posts a roadmap interest submission to the same webhook under its own title,
// so a chat reader can tell a capability vote from a bug report at a glance.
func (f *FeedbackForwarder) ForwardInterest(ctx context.Context, it *models.AgentInterest) error {
	return f.post(ctx, BuildInterestWebhookPayload(f.cfg.Kind, f.cfg.TelegramChatID, it))
}

// post sends one JSON payload to the webhook.
func (f *FeedbackForwarder) post(ctx context.Context, payload map[string]interface{}) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode feedback webhook payload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.cfg.WebhookURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build feedback webhook request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := f.client.Do(req)
	if err != nil {
		return fmt.Errorf("post feedback webhook: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, feedbackForwardBodyLimit))
		return fmt.Errorf("%w: status %d: %s", ErrFeedbackWebhookRejected, resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	return nil
}

// BuildFeedbackWebhookPayload returns the JSON body for one submission: a Discord webhook
// message (content + one embed) or a Telegram sendMessage body (chat_id + plain text).
func BuildFeedbackWebhookPayload(kind, telegramChatID string, fb *models.Feedback) map[string]interface{} {
	if kind == FeedbackWebhookTelegram {
		return map[string]interface{}{
			"chat_id":                  telegramChatID,
			"text":                     feedbackPlainText(fb),
			"disable_web_page_preview": true,
		}
	}
	fields := []map[string]interface{}{}
	addField := func(name, value string) {
		if value != "" {
			fields = append(fields, map[string]interface{}{"name": name, "value": value, "inline": true})
		}
	}
	addField("Path", fb.Path)
	addField("Contact", feedbackContactLine(fb))
	addField("Wallet", fb.WalletAddress)
	return map[string]interface{}{
		"content": fmt.Sprintf("New feedback (%s) #%d", fb.Kind, fb.ID),
		"embeds": []map[string]interface{}{{
			"title":       fmt.Sprintf("[%s] feedback #%d", fb.Kind, fb.ID),
			"description": fb.Message,
			"fields":      fields,
			"timestamp":   fb.CreatedAt.UTC().Format(time.RFC3339),
		}},
	}
}

func feedbackContactLine(fb *models.Feedback) string {
	if fb.Contact == "" {
		return ""
	}
	if fb.ContactVia != "" {
		return fb.ContactVia + ": " + fb.Contact
	}
	return fb.Contact
}

func feedbackPlainText(fb *models.Feedback) string {
	var b strings.Builder
	fmt.Fprintf(&b, "New feedback (%s) #%d\n\n%s", fb.Kind, fb.ID, fb.Message)
	if fb.Path != "" {
		fmt.Fprintf(&b, "\n\nPath: %s", fb.Path)
	}
	if c := feedbackContactLine(fb); c != "" {
		fmt.Fprintf(&b, "\nContact: %s", c)
	}
	if fb.WalletAddress != "" {
		fmt.Fprintf(&b, "\nWallet: %s", fb.WalletAddress)
	}
	return b.String()
}

// BuildInterestWebhookPayload returns the JSON body for one roadmap interest submission, in the
// same two shapes as feedback but under a distinct title.
func BuildInterestWebhookPayload(kind, telegramChatID string, it *models.AgentInterest) map[string]interface{} {
	if kind == FeedbackWebhookTelegram {
		return map[string]interface{}{
			"chat_id":                  telegramChatID,
			"text":                     interestPlainText(it),
			"disable_web_page_preview": true,
		}
	}
	fields := []map[string]interface{}{}
	addField := func(name, value string) {
		if value != "" {
			fields = append(fields, map[string]interface{}{"name": name, "value": value, "inline": true})
		}
	}
	addField("Capabilities", interestCapabilityLine(it))
	addField("Priority", it.Priority)
	addField("Path", it.Path)
	addField("Contact", interestContactLine(it))
	addField("Wallet", it.WalletAddress)
	description := it.Description
	if description == "" {
		description = "(no description)"
	}
	return map[string]interface{}{
		"content": fmt.Sprintf("New roadmap interest #%d (%s)", it.ID, it.Priority),
		"embeds": []map[string]interface{}{{
			"title":       fmt.Sprintf("[roadmap] interest #%d", it.ID),
			"description": description,
			"fields":      fields,
			"timestamp":   it.CreatedAt.UTC().Format(time.RFC3339),
		}},
	}
}

func interestCapabilityLine(it *models.AgentInterest) string {
	labels := make([]string, 0, len(it.Capabilities))
	for _, c := range it.Capabilities {
		labels = append(labels, models.InterestCapabilityLabel(c))
	}
	return strings.Join(labels, ", ")
}

func interestContactLine(it *models.AgentInterest) string {
	if it.Contact == "" {
		return ""
	}
	if it.ContactVia != "" {
		return it.ContactVia + ": " + it.Contact
	}
	return it.Contact
}

func interestPlainText(it *models.AgentInterest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "New roadmap interest #%d (%s)\n\nCapabilities: %s", it.ID, it.Priority, interestCapabilityLine(it))
	if it.Description != "" {
		fmt.Fprintf(&b, "\n\n%s", it.Description)
	}
	if it.Path != "" {
		fmt.Fprintf(&b, "\n\nPath: %s", it.Path)
	}
	if c := interestContactLine(it); c != "" {
		fmt.Fprintf(&b, "\nContact: %s", c)
	}
	if it.WalletAddress != "" {
		fmt.Fprintf(&b, "\nWallet: %s", it.WalletAddress)
	}
	return b.String()
}
