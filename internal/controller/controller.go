// Package controller provides an HTTP service that starts and stops the StonkAgents daemon
// via OS-native APIs (Windows SCM, macOS launchctl). Used so the frontend can "Disconnect"
// without the OS auto-restarting the daemon.
//
// Feature: F-025 (Auto-Update System)
// Story: US-025-01 (Version Identity)
// Purpose: Controller HTTP server with embedded Ed25519 public key for manifest verification
package controller

import (
	"context"
	"crypto/ed25519"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/stonkagents/agent/internal/installenv"
	"github.com/stonkagents/agent/internal/setup"
	"github.com/stonkagents/agent/internal/update"
	"golang.org/x/time/rate"
)

//go:embed update_pubkey.pub
var updatePubkeyRaw string

// updatePublicKey is the Ed25519 public key used to verify release manifest signatures.
// Loaded at init time via go:embed from update_pubkey.pub. Unexported to prevent mutation.
var updatePublicKey ed25519.PublicKey

func init() {
	var err error
	updatePublicKey, err = update.LoadPublicKey(updatePubkeyRaw)
	if err != nil {
		panic("controller: invalid embedded update public key: " + err.Error())
	}
}

// GetUpdatePublicKey returns a copy of the embedded Ed25519 public key.
// Returns a copy to prevent callers from mutating the original.
func GetUpdatePublicKey() ed25519.PublicKey {
	cpy := make(ed25519.PublicKey, len(updatePublicKey))
	copy(cpy, updatePublicKey)
	return cpy
}

const (
	// The listen address is per environment (installenv); see NewServer.
	rateLimitSec = 5
)

// Response types for API
type statusResponse struct {
	Daemon  string `json:"daemon"`  // "running" | "stopped"
	Healthy bool   `json:"healthy"` // true if daemon HTTP /health returns 200
	Version string `json:"version"` // build version from ldflags
}

type successResponse struct {
	Status  string `json:"status"` // "started" | "stopped"
	Message string `json:"message,omitempty"`
}

type errorPayload struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type errorResponse struct {
	Error errorPayload `json:"error"`
}

// Server is the controller HTTP server.
type Server struct {
	httpServer *http.Server
	version    string
	lastAction time.Time
	mu         sync.Mutex
	updater    *Updater // F-025: update lifecycle state machine

	// RUN-1: privileged setup fixes (firewall rule, service start type).
	setupLimiter  *rate.Limiter
	daemonExePath func() string                    // setup.DefaultDaemonExePath; injectable for tests
	fixFirewall   func(exePath string) setup.Check // setup.FixFirewall; injectable for tests
	fixAutostart  func() setup.Check               // setup.FixAutostart; injectable for tests

	// The background command tools job (setup_commandtools.go).
	commandTools commandToolsDeps
}

// NewServer creates a controller server with the given version string.
// Returns error if required dependencies fail to initialize (e.g., public key loading in Phase A).
// Call Run to start listening.
func NewServer(version string) (*Server, error) {
	logger := slog.Default()
	// folder to .stonkagents (2.5.0) here too, so whichever service comes up
	// first does it before anything opens a file in the folder.
	if err := installenv.MigrateLegacyDataDirs(func(format string, args ...any) {
		logger.Info(fmt.Sprintf(format, args...))
	}); err != nil {
		logger.Warn("data folder migration failed; keeping the legacy folder for now", "error", err)
	}
	mux := http.NewServeMux()
	updater := NewUpdater(UpdaterConfig{
		CurrentVersion: version,
		PubKey:         GetUpdatePublicKey(),
		Notifier:       newPlatformNotifier(logger),
	})
	wireUpdaterInstall(updater)
	s := &Server{
		version:       version,
		updater:       updater,
		setupLimiter:  newSetupLimiter(),
		daemonExePath: setup.DefaultDaemonExePath,
		fixFirewall:   setup.FixFirewall,
		fixAutostart:  setup.FixAutostart,
		commandTools:  defaultCommandToolsDeps(),
	}
	mux.HandleFunc("POST /start", s.handleStart)
	mux.HandleFunc("POST /stop", s.handleStop)
	mux.HandleFunc("GET /status", s.handleStatus)
	// F-025: Update lifecycle endpoints
	mux.HandleFunc("POST /update/notify", s.handleUpdateNotify)
	mux.HandleFunc("GET /update/status", s.handleUpdateStatus)
	mux.HandleFunc("POST /update/start", s.handleUpdateStart)
	mux.HandleFunc("POST /update/cancel", s.handleUpdateCancel)
	// RUN-1: privileged setup fixes, proxied from the daemon's /api/v1/setup/{firewall,autostart}
	mux.HandleFunc("POST /setup/firewall", s.handleSetupFirewall)
	mux.HandleFunc("POST /setup/autostart", s.handleSetupAutostart)
	// The background command tools job: its status file, and a retry (starts the scheduled task).
	mux.HandleFunc("GET /setup/command-tools", s.handleCommandToolsStatus)
	mux.HandleFunc("POST /setup/command-tools/retry", s.handleCommandToolsRetry)
	// TD-020: Wrap mux with CORS so browser preflight from portal (:3000) passes.
	s.httpServer = &http.Server{Addr: installenv.Current().ControllerAddr(), Handler: corsMiddleware(mux)}
	return s, nil
}

// Run starts the HTTP server and blocks until ctx is cancelled or the server fails.
func (s *Server) Run(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		_ = s.httpServer.Shutdown(context.Background())
	}()
	return s.httpServer.ListenAndServe()
}

func (s *Server) rateLimit() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if now.Sub(s.lastAction) < rateLimitSec*time.Second {
		return false
	}
	s.lastAction = now
	return true
}

func (s *Server) recordAction() {
	s.mu.Lock()
	s.lastAction = time.Now()
	s.mu.Unlock()
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) handleStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: errorPayload{Code: "METHOD_NOT_ALLOWED", Message: "POST required"}})
		return
	}
	if !s.rateLimit() {
		writeJSON(w, http.StatusTooManyRequests, errorResponse{Error: errorPayload{Code: "RATE_LIMITED", Message: "start/stop called too soon; wait 5 seconds"}})
		return
	}
	if err := StartDaemon(); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: errorPayload{Code: "START_FAILED", Message: err.Error()}})
		return
	}
	s.recordAction()
	writeJSON(w, http.StatusOK, successResponse{Status: "started", Message: "daemon service started"})
}

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: errorPayload{Code: "METHOD_NOT_ALLOWED", Message: "POST required"}})
		return
	}
	if !s.rateLimit() {
		writeJSON(w, http.StatusTooManyRequests, errorResponse{Error: errorPayload{Code: "RATE_LIMITED", Message: "start/stop called too soon; wait 5 seconds"}})
		return
	}
	if err := StopDaemon(); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: errorPayload{Code: "STOP_FAILED", Message: err.Error()}})
		return
	}
	s.recordAction()
	writeJSON(w, http.StatusOK, successResponse{Status: "stopped", Message: "daemon service stopped"})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: errorPayload{Code: "METHOD_NOT_ALLOWED", Message: "GET required"}})
		return
	}
	daemonState := "stopped"
	if IsDaemonRunning() {
		daemonState = "running"
	}
	healthy := IsDaemonHealthy()
	writeJSON(w, http.StatusOK, statusResponse{Daemon: daemonState, Healthy: healthy, Version: s.version})
}

// === F-025: Update lifecycle HTTP handlers ===

type updateNotifyRequest struct {
	LatestVersion string `json:"latest_version"`
	ReleaseNotes  string `json:"release_notes"`
}

func (s *Server) handleUpdateNotify(w http.ResponseWriter, r *http.Request) {
	var req updateNotifyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{
			Error: errorPayload{Code: "INVALID_REQUEST", Message: "invalid JSON body"},
		})
		return
	}
	s.updater.Notify(req.LatestVersion, req.ReleaseNotes)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleUpdateStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.updater.Status())
}

func (s *Server) handleUpdateStart(w http.ResponseWriter, _ *http.Request) {
	if err := s.updater.Start(); err != nil {
		code := "INVALID_STATE"
		if errors.Is(err, ErrManualInstall) {
			code = "MANUAL_INSTALL"
		}
		writeJSON(w, http.StatusConflict, errorResponse{
			Error: errorPayload{Code: code, Message: err.Error()},
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleUpdateCancel(w http.ResponseWriter, _ *http.Request) {
	if err := s.updater.Cancel(); err != nil {
		writeJSON(w, http.StatusConflict, errorResponse{
			Error: errorPayload{Code: "INVALID_STATE", Message: err.Error()},
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
