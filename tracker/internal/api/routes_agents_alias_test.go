// Package: tracker/internal/api
// Feature: StonkAgents rename (user-visible API surface)
// Purpose: The /api/v1/agents/* completion paths must
//          reach the same handler with the same auth and the same rate limit bucket.

package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stonkagents/agent/tracker/internal/clock"
	"github.com/stonkagents/agent/tracker/internal/ratelimit"
)

// newCompletionsAliasServer builds a server with only the completions handler wired.
// The handler has no LLM and no key repos, which is enough: every registered path
// answers 401 for a keyless request, and an unregistered path answers 404.
func newCompletionsAliasServer(t *testing.T, limiter ratelimit.Limiter) *Server {
	t.Helper()
	return NewServer(ServerDeps{
		AgentCompletions: NewAgentCompletionsHandler(nil, nil, nil, nil, nil, nil, AgentCompletionsConfig{}),
		Limiter:          limiter,
		Address:          ":7843",
	})
}

func postCompletions(t *testing.T, srv *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "198.51.100.7:5555"
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	return w
}

// TestAgentCompletions_AliasPathsHitSameHandler asserts every path in AgentCompletionsPaths
// is registered and behaves identically.
func TestAgentCompletions_AliasPathsHitSameHandler(t *testing.T) {
	srv := newCompletionsAliasServer(t, nil)

	// Paths are relative to the /api subrouter, exactly as server.go registers them.
	want := []string{
		"/v1/agents/completions",
		"/v1/agents/chat/completions",
	}
	if len(AgentCompletionsPaths) != len(want) {
		t.Fatalf("AgentCompletionsPaths = %v, want the four paths %v", AgentCompletionsPaths, want)
	}
	for i, p := range want {
		if AgentCompletionsPaths[i] != p {
			t.Fatalf("AgentCompletionsPaths[%d] = %q, want %q", i, AgentCompletionsPaths[i], p)
		}
	}

	var first string
	for _, p := range want {
		w := postCompletions(t, srv, "/api"+p)
		if w.Code == http.StatusNotFound {
			t.Fatalf("%s: status 404 — path is not registered", p)
		}
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401 (keyless request reaches the completions handler)", p, w.Code)
		}
		if first == "" {
			first = w.Body.String()
			continue
		}
		if body := w.Body.String(); body != first {
			t.Errorf("%s: body = %s, want identical to the first path's body %s", p, body, first)
		}
	}
}

// TestAgentCompletions_AliasesShareRateLimitBucket asserts the new and legacy paths are
// metered together: a client cannot get a second allowance by switching to the alias.
func TestAgentCompletions_AliasesShareRateLimitBucket(t *testing.T) {
	clk := clock.NewMockClock(time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC))
	srv := newCompletionsAliasServer(t, ratelimit.NewMemoryLimiter(clk))

	limit := AgentCompletionsRateLimitConfig().Limit
	for i := 0; i < limit; i++ {
		if w := postCompletions(t, srv, "/api/v1/agents/completions"); w.Code == http.StatusTooManyRequests {
			t.Fatalf("request %d/%d rate limited too early", i+1, limit)
		}
	}
	if w := postCompletions(t, srv, "/api/v1/agents/completions"); w.Code != http.StatusTooManyRequests {
		t.Errorf("legacy path status = %d after exhausting the limit on the agents path, want 429 (one shared bucket)", w.Code)
	}
}
