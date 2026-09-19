// Package: main
// Feature: StonkAgents roadmap interest
// Purpose: Wires the interest repository, the shared chat webhook forwarder and the admin key.
//
// Env read here (shared with feedback):
//
//	FEEDBACK_WEBHOOK_URL       optional; when set every submission is forwarded (best-effort)
//	FEEDBACK_WEBHOOK_KIND      "discord" (default) | "telegram"
//	FEEDBACK_TELEGRAM_CHAT_ID  required with FEEDBACK_WEBHOOK_KIND=telegram
//	ADMIN_API_KEY              optional; GET /api/v1/admin/interest[/summary] (header X-Admin-Key)
//	                           answers 503 ADMIN_DISABLED until it is set

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

func bootstrapInterest(pgPool *pgxpool.Pool, clk clock.Clock) *api.InterestHandler {
	deps := api.InterestHandlerDeps{
		Repo:     repository.NewPostgresInterestRepository(pgPool),
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
		fmt.Printf("Interest: webhook forwarding enabled (%s)\n", fwd.Kind())
	} else {
		// The missing-webhook WARN is logged once by bootstrapFeedback (same env).
		fmt.Println("Interest: webhook forwarding disabled (store only)")
	}
	if deps.AdminKey != "" {
		fmt.Println("Interest: admin read enabled (GET /api/v1/admin/interest, /summary)")
	} else {
		fmt.Println("Interest: ADMIN_API_KEY not set, GET /api/v1/admin/interest[/summary] answers 503 ADMIN_DISABLED")
	}
	return api.NewInterestHandler(deps)
}
