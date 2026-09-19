// Package: main
// Feature: StonkAgents portal feedback
// Purpose: Wires the feedback repository, the chat webhook forwarder, Turnstile verification
//          and the admin key.
//
// Env read here:
//
//	FEEDBACK_WEBHOOK_URL       optional; when set every submission is forwarded (best-effort).
//	                           Unset = submissions still 200 and sit in Postgres; one WARN at boot.
//	FEEDBACK_WEBHOOK_KIND      "discord" (default) | "telegram"
//	FEEDBACK_TELEGRAM_CHAT_ID  required with FEEDBACK_WEBHOOK_KIND=telegram
//	TURNSTILE_SECRET_KEY       optional; when set POST /api/v1/feedback verifies X-Turnstile-Token
//	                           against Cloudflare siteverify (403 TURNSTILE_FAILED / 503 TURNSTILE_UNAVAILABLE).
//	                           Unset = the header is ignored. Never logged.
//	TURNSTILE_VERIFY_URL       optional siteverify override (tests / proxies)
//	ADMIN_API_KEY              optional; GET /api/v1/admin/feedback (header X-Admin-Key) answers
//	                           503 ADMIN_DISABLED until it is set

package main

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stonkagents/agent/tracker/internal/api"
	"github.com/stonkagents/agent/tracker/internal/clock"
	"github.com/stonkagents/agent/tracker/internal/repository"
	"github.com/stonkagents/agent/tracker/internal/services"
)

func bootstrapFeedback(pgPool *pgxpool.Pool, clk clock.Clock) *api.FeedbackHandler {
	deps := api.FeedbackHandlerDeps{
		Repo:     repository.NewPostgresFeedbackRepository(pgPool),
		AdminKey: strings.TrimSpace(os.Getenv("ADMIN_API_KEY")),
		Clock:    clk,
		Logger:   slog.Default(),
	}
	fwd := services.NewFeedbackForwarder(services.FeedbackForwarderConfig{
		WebhookURL:     os.Getenv("FEEDBACK_WEBHOOK_URL"),
		Kind:           os.Getenv("FEEDBACK_WEBHOOK_KIND"),
		TelegramChatID: os.Getenv("FEEDBACK_TELEGRAM_CHAT_ID"),
	}, slog.Default())
	if fwd != nil {
		deps.Forwarder = fwd // nil *FeedbackForwarder must not become a non-nil interface
		fmt.Printf("Feedback: webhook forwarding enabled (%s)\n", fwd.Kind())
	} else {
		// Logged once here; bootstrapInterest shares the same env and stays quiet about it.
		slog.Warn("[feedback] no webhook configured; feedback and interest submissions are stored in Postgres only "+
			"(read them via GET /api/v1/admin/feedback and /api/v1/admin/interest with ADMIN_API_KEY)",
			"hint", "set FEEDBACK_WEBHOOK_URL (telegram also needs FEEDBACK_TELEGRAM_CHAT_ID)")
	}
	if ts := services.NewTurnstileVerifier(os.Getenv("TURNSTILE_SECRET_KEY"), strings.TrimSpace(os.Getenv("TURNSTILE_VERIFY_URL")), slog.Default()); ts != nil {
		deps.Turnstile = ts // same nil-interface rule as the forwarder
		fmt.Println("Feedback: Turnstile verification enabled (X-Turnstile-Token checked on POST /api/v1/feedback)")
	} else {
		fmt.Println("Feedback: TURNSTILE_SECRET_KEY not set, X-Turnstile-Token is ignored")
	}
	if deps.AdminKey != "" {
		fmt.Println("Feedback: admin read enabled (GET /api/v1/admin/feedback)")
	} else {
		fmt.Println("Feedback: ADMIN_API_KEY not set, GET /api/v1/admin/feedback answers 503 ADMIN_DISABLED")
	}
	return api.NewFeedbackHandler(deps)
}
