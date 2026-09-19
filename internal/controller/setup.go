// Package: internal/controller
// Feature: RUN-1 (Permissions step)
// Purpose: Privileged setup fixes. The controller runs as a Windows service
// (LocalSystem) so it can add the program-scoped firewall rule and set the
// services' start type; the daemon proxies POST /api/v1/setup/{firewall,autostart}
// here.
//
//	POST /setup/firewall   → 200 {check}
//	POST /setup/autostart  → 200 {check}
//	                         400 {error} invalid body, 403 not loopback / missing
//	                         headers, 409 daemon binary missing, 429 rate limited
//
// SECURITY: the firewall handler runs netsh as LocalSystem. It never takes a
// program path from the request — the rule is scoped to the daemon binary
// next to this executable (setup.DefaultDaemonExePath), which must exist —
// so a caller cannot punch a hole for an arbitrary program. Requests must
// come from loopback and carry Content-Type: application/json plus
// X-StonkAgents-Setup: 1 (setup.HasMutationHeaders): the custom header
// forces a CORS preflight, which corsMiddleware does not grant for this
// header, so a browser page cannot reach these handlers at all.

package controller

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/stonkagents/agent/internal/setup"
	"golang.org/x/time/rate"
)

// newSetupLimiter allows a light burst of privileged fixes, then one per second.
func newSetupLimiter() *rate.Limiter { return rate.NewLimiter(rate.Every(time.Second), 5) }

// decodeSetupRequest validates the (optional, ignored) JSON body: an empty
// body or a JSON object is accepted, anything else is a 400. No field of the
// body is used.
func (s *Server) decodeSetupRequest(r *http.Request) bool {
	body, err := io.ReadAll(io.LimitReader(r.Body, 16<<10))
	if err != nil {
		return false
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return true
	}
	var ignored map[string]json.RawMessage
	return json.Unmarshal(body, &ignored) == nil
}

// isLoopbackRequest reports whether r came from 127.0.0.1/::1.
func isLoopbackRequest(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// setupGate applies the checks shared by both setup handlers (loopback,
// mutation headers, body shape, rate limit) and reports whether the handler
// may proceed; on false the error has been written.
func (s *Server) setupGate(w http.ResponseWriter, r *http.Request) bool {
	if !isLoopbackRequest(r) {
		writeJSON(w, http.StatusForbidden, errorResponse{Error: errorPayload{Code: "FORBIDDEN", Message: "only localhost requests allowed"}})
		return false
	}
	if !setup.HasMutationHeaders(r) {
		writeJSON(w, http.StatusForbidden, errorResponse{Error: errorPayload{Code: "FORBIDDEN",
			Message: "setup changes require Content-Type: application/json and " + setup.MutationHeader + ": " + setup.MutationHeaderValue}})
		return false
	}
	if !s.decodeSetupRequest(r) {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: errorPayload{Code: "INVALID_REQUEST", Message: "invalid JSON body"}})
		return false
	}
	if !s.setupLimiter.Allow() {
		writeJSON(w, http.StatusTooManyRequests, errorResponse{Error: errorPayload{Code: "RATE_LIMITED", Message: "too many setup changes; wait a second and retry"}})
		return false
	}
	return true
}

// handleSetupFirewall adds the "StonkAgents Agent" inbound allow rule for the
// installed daemon executable (idempotent) and returns the resulting check.
func (s *Server) handleSetupFirewall(w http.ResponseWriter, r *http.Request) {
	if !s.setupGate(w, r) {
		return
	}
	exe := s.daemonExePath()
	if info, err := os.Stat(exe); exe == "" || err != nil || info.IsDir() {
		writeJSON(w, http.StatusConflict, errorResponse{Error: errorPayload{Code: "DAEMON_EXE_MISSING",
			Message: "daemon executable not found next to the controller: " + exe}})
		return
	}
	writeJSON(w, http.StatusOK, setup.FixResponse{Check: s.fixFirewall(exe)})
}

// handleSetupAutostart sets StonkAgentsDaemon and StonkAgentsController to
// start automatically and returns the resulting check.
func (s *Server) handleSetupAutostart(w http.ResponseWriter, r *http.Request) {
	if !s.setupGate(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, setup.FixResponse{Check: s.fixAutostart()})
}
