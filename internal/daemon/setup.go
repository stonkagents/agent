// Package: internal/daemon
// Feature: RUN-1 (Permissions step) / PERF-2
// Purpose: Setup surface consumed by the portal's Permissions step.
//
//	GET  /api/v1/setup/status  → 200 {checks: SetupCheck[]}
//	POST /api/v1/setup/{id}    → 200 {check: SetupCheck} | 202 (restart required)
//	                             4xx/5xx {error: {code, message}}
//
// Loopback only, like /api/v1/installer/peer-key. Privileged fixes (firewall,
// autostart) are proxied to the controller service on :7840; unprivileged
// fixes (storage, bandwidth, origin) are written to config.yaml and applied
// live where the running process supports it.
//
// CSRF: the user's browser is itself loopback, so the loopback check alone
// does not stop a hostile page from POSTing here with mode:"no-cors". Every
// mutating POST therefore requires Content-Type: application/json and the
// custom header X-StonkAgents-Setup: 1 (SetupHeader). A custom header makes
// the browser send a CORS preflight, and CORSMiddleware only answers the
// preflight for allowlisted origins — an unlisted page never gets to send
// the real request. The origin fix additionally admits only the product's
// own domains (isProductOrigin) so it cannot be used to widen that allowlist.

package daemon

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/stonkagents/agent/internal/config"
	"github.com/stonkagents/agent/internal/installenv"
	"github.com/stonkagents/agent/internal/setup"
	"golang.org/x/time/rate"
)

// SetupHeader is the custom request header every POST /api/v1/setup/* must
// carry (value "1"). Its presence forces a CORS preflight; see the package
// comment. The controller's /setup/* expects the same header from the proxy.
const SetupHeader = setup.MutationHeader

// setupFixTimeout bounds a proxied privileged fix. It must stay below the
// daemon's http.Server WriteTimeout (ServerWriteTimeout, 18s) or the response
// is cut off before the error can be relayed.
const setupFixTimeout = 15 * time.Second

// setupStatusTimeout bounds GET /api/v1/setup/status as a whole. The checks
// run concurrently and a check still pending at the deadline is reported
// failed, so the CLI (10s client timeout) never mistakes a slow netsh/sc for
// an unreachable daemon.
const setupStatusTimeout = 8 * time.Second

// setupFixLimiter allows a light burst of fixes, then one per second.
func newSetupFixLimiter() *rate.Limiter { return rate.NewLimiter(rate.Every(time.Second), 5) }

// setupState is the setup-surface state hung off Server.
type setupState struct {
	mu             sync.RWMutex
	limiter        *rate.Limiter
	pendingDataDir string // data_dir written to config.yaml, effective after restart

	// tracker registration outcome (RegisterWithTracker / doHeartbeat)
	regAttempted bool
	regOK        bool
	regErr       string
	regAt        time.Time

	exePath func() (string, error) // os.Executable, injectable for tests
}

func (s *Server) setupSt() *setupState {
	s.setupOnce.Do(func() {
		s.setup = &setupState{limiter: newSetupFixLimiter(), exePath: os.Executable}
	})
	return s.setup
}

// recordTrackerRegistration stores the outcome of the latest registration or
// heartbeat so the tracker check can report it.
func (s *Server) recordTrackerRegistration(err error) {
	st := s.setupSt()
	st.mu.Lock()
	defer st.mu.Unlock()
	st.regAttempted = true
	st.regAt = time.Now()
	st.regOK = err == nil
	st.regErr = ""
	if err != nil {
		st.regErr = err.Error()
	}
}

// corsPolicy returns the live CORS allowlist (created on first use so tests
// that never call Start still get one).
func (s *Server) corsPolicy() *CORSConfig {
	s.corsOnce.Do(func() {
		if s.corsConfig == nil {
			s.corsConfig = DefaultCORSConfig()
			if s.config != nil {
				s.corsConfig.AddOrigins(s.config.CORSAllowedOrigins)
			}
		}
	})
	return s.corsConfig
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

// daemonExePath is the path the firewall rule is scoped to.
func (s *Server) daemonExePath() string {
	exe, err := s.setupSt().exePath()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return exe
}

// --- checks -------------------------------------------------------------

// setupChecks evaluates every check concurrently and returns them in
// CheckIDs order. The slow ones (controller probe, netsh, sc) do not add up:
// the whole set is bounded by setupStatusTimeout and a check still running
// at the deadline is reported failed (its goroutine finishes on its own).
func (s *Server) setupChecks(r *http.Request) []setup.Check {
	return s.setupChecksWithin(r, setupStatusTimeout)
}

func (s *Server) setupChecksWithin(r *http.Request, budget time.Duration) []setup.Check {
	type result struct {
		i     int
		check setup.Check
	}
	results := make(chan result, len(setup.CheckIDs))
	for i, id := range setup.CheckIDs {
		go func(i int, id string) {
			results <- result{i: i, check: s.setupCheck(id, r)}
		}(i, id)
	}
	checks := make([]setup.Check, len(setup.CheckIDs))
	done := make([]bool, len(setup.CheckIDs))
	deadline := time.After(budget)
	for pending := len(setup.CheckIDs); pending > 0; {
		select {
		case res := <-results:
			checks[res.i], done[res.i] = res.check, true
			pending--
		case <-deadline:
			for i, id := range setup.CheckIDs {
				if !done[i] {
					checks[i] = setup.Failed(id, fmt.Sprintf("check did not finish within %s", budget), nil)
				}
			}
			return checks
		}
	}
	return checks
}

func (s *Server) setupCheck(id string, r *http.Request) setup.Check {
	switch id {
	case setup.IDService:
		return s.checkService()
	case setup.IDController:
		return setup.ControllerCheck(s.controllerURLOrDefault())
	case setup.IDFirewall:
		return setup.Firewall(s.daemonExePath())
	case setup.IDP2P:
		return s.checkP2P()
	case setup.IDTracker:
		return s.checkTracker()
	case setup.IDStorage:
		return s.checkStorage()
	case setup.IDBandwidth:
		return s.checkBandwidth()
	case setup.IDAutostart:
		return setup.Autostart()
	case setup.IDOrigin:
		return s.checkOrigin(r)
	}
	return setup.Failed(id, "unknown check", nil)
}

func (s *Server) controllerURLOrDefault() string {
	if s.controllerURL != "" {
		return s.controllerURL
	}
	return defaultControllerURL()
}

func (s *Server) checkService() setup.Check {
	version := "dev"
	if s.config != nil && s.config.Version != "" {
		version = s.config.Version
	}
	return setup.OK(setup.IDService, map[string]any{
		"pid":           os.Getpid(),
		"version":       version,
		"uptimeSeconds": int64(time.Since(s.startTime).Seconds()),
	})
}

func (s *Server) checkP2P() setup.Check {
	if s.p2pHost == nil {
		return setup.Failed(setup.IDP2P, "P2P host is not started", nil)
	}
	health := s.p2pHealthIndicators()
	addrs := s.p2pHost.Addrs()
	listening := len(addrs) > 0
	detail := map[string]any{
		"natStatus":      health.NATStatus,
		"dhtReady":       health.DHTReady,
		"relayConnected": health.RelayConnected,
		"listening":      listening,
		"listenAddrs":    len(addrs),
	}
	switch {
	case !listening:
		return setup.Failed(setup.IDP2P, "P2P host is not listening on any address", detail)
	case !health.DHTReady && !health.RelayConnected:
		return setup.Failed(setup.IDP2P, "neither the DHT nor a relay is available; peers cannot reach this agent", detail)
	}
	return setup.OK(setup.IDP2P, detail)
}

func (s *Server) checkTracker() setup.Check {
	detail := map[string]any{}
	if s.config != nil {
		detail["trackerUrl"] = s.config.TrackerURL
	}
	if s.getTrackerAPIKey() == "" {
		return setup.Missing(setup.IDTracker, "not registered with the tracker yet", detail)
	}
	st := s.setupSt()
	st.mu.RLock()
	attempted, ok, errText, at := st.regAttempted, st.regOK, st.regErr, st.regAt
	st.mu.RUnlock()
	detail["apiKeyPresent"] = true
	if !at.IsZero() {
		detail["lastRegistrationAt"] = at.UTC().Format(time.RFC3339)
	}
	switch {
	case !attempted:
		return setup.Missing(setup.IDTracker, "tracker registration pending", detail)
	case !ok:
		return setup.Failed(setup.IDTracker, "last tracker registration failed: "+errText, detail)
	}
	return setup.OK(setup.IDTracker, detail)
}

func (s *Server) checkStorage() setup.Check {
	path := ""
	if s.config != nil {
		path = s.config.DataDir
	}
	check := setup.StorageCheck(path)
	st := s.setupSt()
	st.mu.RLock()
	pending := st.pendingDataDir
	st.mu.RUnlock()
	if pending != "" && pending != path {
		if check.Detail == nil {
			check.Detail = map[string]any{}
		}
		check.Detail["pendingPath"] = pending
		check.Detail["restartRequired"] = true
	}
	return check
}

// bandwidthCaps returns the configured caps under the config lock.
func (s *Server) bandwidthCaps() (up, down float64) {
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	if s.config != nil {
		up, down = s.config.UploadCapMbps, s.config.DownloadCapMbps
	}
	return up, down
}

func (s *Server) checkBandwidth() setup.Check {
	return setup.BandwidthCheck(s.bandwidthCaps())
}

func (s *Server) checkOrigin(r *http.Request) setup.Check {
	origin := ""
	if r != nil {
		origin = strings.TrimSpace(r.Header.Get("Origin"))
	}
	if origin == "" {
		return setup.OK(setup.IDOrigin, map[string]any{"origin": nil})
	}
	detail := map[string]any{"origin": origin}
	if s.corsPolicy().IsOriginAllowed(origin) {
		return setup.OK(setup.IDOrigin, detail)
	}
	return setup.Missing(setup.IDOrigin, origin+" is not allowed to call the agent", detail)
}

// --- handlers -----------------------------------------------------------

// handleSetupStatus handles GET /api/v1/setup/status.
func (s *Server) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	if !isLoopbackRequest(r) {
		s.sendErrorResponse(w, http.StatusForbidden, "FORBIDDEN", "Only localhost requests allowed", nil)
		return
	}
	s.sendJSONResponse(w, http.StatusOK, setup.Response{Checks: s.setupChecks(r)})
}

// handleSetupFix handles POST /api/v1/setup/{id}.
func (s *Server) handleSetupFix(w http.ResponseWriter, r *http.Request) {
	if !isLoopbackRequest(r) {
		s.sendErrorResponse(w, http.StatusForbidden, "FORBIDDEN", "Only localhost requests allowed", nil)
		return
	}
	id := r.PathValue("id")
	if !setup.IsKnownID(id) {
		s.sendErrorResponse(w, http.StatusNotFound, "UNKNOWN_CHECK", "unknown setup check: "+id, nil)
		return
	}
	if !setup.FixableIDs[id] {
		s.sendErrorResponse(w, http.StatusMethodNotAllowed, "NOT_FIXABLE", id+" is reported only and has no fix", nil)
		return
	}
	if !hasSetupMutationHeaders(r) {
		s.sendErrorResponse(w, http.StatusForbidden, "FORBIDDEN",
			"setup changes require Content-Type: application/json and "+SetupHeader+": 1", nil)
		return
	}
	if !s.setupSt().limiter.Allow() {
		s.sendErrorResponse(w, http.StatusTooManyRequests, "RATE_LIMITED", "too many setup changes; wait a second and retry", nil)
		return
	}
	if setup.PrivilegedIDs[id] {
		s.proxySetupFixToController(w, r, id)
		return
	}

	var (
		check  setup.Check
		status = http.StatusOK
		err    error
	)
	switch id {
	case setup.IDStorage:
		check, status, err = s.fixStorage(r)
	case setup.IDBandwidth:
		check, err = s.fixBandwidth(r)
	case setup.IDOrigin:
		check, err = s.fixOrigin(r)
	}
	if err != nil {
		var se *setupError
		if errors.As(err, &se) {
			s.sendErrorResponse(w, se.status, se.code, se.Error(), nil)
			return
		}
		s.sendErrorResponse(w, http.StatusInternalServerError, "FIX_FAILED", err.Error(), nil)
		return
	}
	s.sendJSONResponse(w, status, setup.FixResponse{Check: check})
}

// setupError carries an HTTP status and error code for a rejected fix.
type setupError struct {
	status int
	code   string
	msg    string
}

func (e *setupError) Error() string { return e.msg }

func badRequest(msg string) error {
	return &setupError{status: http.StatusBadRequest, code: "INVALID_REQUEST", msg: msg}
}

// hasSetupMutationHeaders reports whether r carries the CSRF-defeating
// headers: a JSON content type (a form or text/plain post from a hostile page
// does not qualify) and SetupHeader: 1 (forces a preflight). Shared with the
// controller (setup.HasMutationHeaders) so both hops enforce the same rule.
func hasSetupMutationHeaders(r *http.Request) bool { return setup.HasMutationHeaders(r) }

// decodeOptionalJSON decodes a JSON body into v; an empty body is not an error
// (the portal's fix button posts no body).
func decodeOptionalJSON(r *http.Request, v any) error {
	if r.Body == nil {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		return badRequest("cannot read body")
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, v); err != nil {
		return badRequest("invalid JSON body")
	}
	return nil
}

// fixStorage handles POST /api/v1/setup/storage {path?}. Without a path it
// repairs the current data directory (creating it) and, when that still
// fails, picks the first candidate that passes. A supplied path must lie
// under the current data directory's parent, the user's profile, ProgramData
// or an offered candidate (setup.StorageRoots) — never an arbitrary absolute
// or UNC path. A changed data_dir is written to config.yaml and answered with
// 202 restartRequired: the chunk store and databases are opened once at
// startup and cannot be re-pointed live.
func (s *Server) fixStorage(r *http.Request) (setup.Check, int, error) {
	var body struct {
		Path string `json:"path"`
	}
	if err := decodeOptionalJSON(r, &body); err != nil {
		return setup.Check{}, 0, err
	}
	current := ""
	if s.config != nil {
		current = s.config.DataDir
	}

	target := strings.TrimSpace(body.Path)
	var check setup.Check
	if target != "" {
		validated, err := setup.ValidateStoragePath(target, setup.StorageRoots(current))
		if err != nil {
			return setup.Check{}, 0, badRequest(err.Error())
		}
		c, err := setup.PrepareStorage(validated)
		if err != nil {
			return setup.Check{}, 0, badRequest(err.Error())
		}
		check, target = c, c.Detail["path"].(string)
	} else {
		var lastErr error
		for _, cand := range setup.StorageCandidates(current) {
			c, err := setup.PrepareStorage(cand)
			if err == nil {
				check, target = c, c.Detail["path"].(string)
				break
			}
			lastErr = err
		}
		if target == "" {
			if lastErr == nil {
				lastErr = fmt.Errorf("no candidate directory available")
			}
			return setup.Check{}, 0, &setupError{status: http.StatusConflict, code: "NO_STORAGE", msg: lastErr.Error()}
		}
	}

	if samePathClean(target, current) {
		return check, http.StatusOK, nil // repaired in place; nothing to persist
	}
	if err := config.UpdateFile(func(doc map[string]any) error {
		doc["data_dir"] = target
		return nil
	}); err != nil {
		return setup.Check{}, 0, err
	}
	st := s.setupSt()
	st.mu.Lock()
	st.pendingDataDir = target
	st.mu.Unlock()
	check.Message = "restart to apply"
	check.Detail["restartRequired"] = true
	check.Detail["currentPath"] = current
	return check, http.StatusAccepted, nil
}

func samePathClean(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

// fixBandwidth handles POST /api/v1/setup/bandwidth {uploadMbps?, downloadMbps?}.
// Missing values keep the configured caps or fall back to the defaults; each
// cap must lie within [setup.MinCapMbps, setup.MaxCapMbps]. Written to
// config.yaml and applied live to the upload and download throttles.
func (s *Server) fixBandwidth(r *http.Request) (setup.Check, error) {
	var body struct {
		UploadMbps   *float64 `json:"uploadMbps"`
		DownloadMbps *float64 `json:"downloadMbps"`
	}
	if err := decodeOptionalJSON(r, &body); err != nil {
		return setup.Check{}, err
	}
	up, down := setup.DefaultUploadMbps, setup.DefaultDownloadMbps
	curUp, curDown := s.bandwidthCaps()
	if curUp > 0 {
		up = curUp
	}
	if curDown > 0 {
		down = curDown
	}
	if body.UploadMbps != nil {
		up = *body.UploadMbps
	}
	if body.DownloadMbps != nil {
		down = *body.DownloadMbps
	}
	if err := setup.ValidateCapMbps(up); err != nil {
		return setup.Check{}, badRequest("uploadMbps " + err.Error())
	}
	if err := setup.ValidateCapMbps(down); err != nil {
		return setup.Check{}, badRequest("downloadMbps " + err.Error())
	}
	if err := config.UpdateFile(func(doc map[string]any) error {
		doc["upload_cap_mbps"] = up
		doc["download_cap_mbps"] = down
		return nil
	}); err != nil {
		return setup.Check{}, err
	}
	s.applyBandwidthCaps(up, down)
	return s.checkBandwidth(), nil
}

// applyBandwidthCaps updates config (under configMu) and the live throttles:
// the download manager paces received chunks, the upload manager paces chunks
// served to peers (chunkProviderAdapter.GetChunk).
func (s *Server) applyBandwidthCaps(uploadMbps, downloadMbps float64) {
	s.configMu.Lock()
	if s.config != nil {
		s.config.UploadCapMbps = uploadMbps
		s.config.DownloadCapMbps = downloadMbps
	}
	s.configMu.Unlock()
	if s.uploadManager != nil {
		s.uploadManager.SetBandwidthLimit(setup.MbpsToBytesPerSecond(uploadMbps))
	}
	if s.downloadManager != nil {
		s.downloadManager.SetBandwidthLimit(setup.MbpsToBytesPerSecond(downloadMbps))
	}
}

// fixOrigin handles POST /api/v1/setup/origin: the request's Origin header is
// appended to cors_allowed_origins in config.yaml and to the live allowlist.
// Only the product's own origins (isProductOrigin) are ever admitted — the
// endpoint exists so a new portal host (preview, staging) can enrol itself,
// not so that any page can grant itself CORS access.
func (s *Server) fixOrigin(r *http.Request) (setup.Check, error) {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return setup.Check{}, badRequest("Origin header required")
	}
	if err := validateOrigin(origin); err != nil {
		return setup.Check{}, badRequest(err.Error())
	}
	if !isProductOrigin(origin) {
		return setup.Check{}, &setupError{status: http.StatusForbidden, code: "ORIGIN_NOT_ALLOWED",
			msg: origin + " is not a StonkAgents origin and cannot be allowlisted"}
	}
	if !s.corsPolicy().IsOriginAllowed(origin) {
		if err := config.UpdateFile(func(doc map[string]any) error {
			config.AppendUniqueString(doc, "cors_allowed_origins", origin)
			return nil
		}); err != nil {
			return setup.Check{}, err
		}
		s.corsPolicy().AddOrigin(origin)
		s.configMu.Lock()
		if s.config != nil {
			s.config.CORSAllowedOrigins = append(s.config.CORSAllowedOrigins, origin)
		}
		s.configMu.Unlock()
	}
	return s.checkOrigin(r), nil
}

// validateOrigin accepts scheme://host[:port] with an http(s) scheme only.
func validateOrigin(origin string) error {
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("origin must be http(s)://host[:port]")
	}
	if u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return fmt.Errorf("origin must not contain a path, query, fragment or credentials")
	}
	return nil
}

// productDomains are the registrable domains whose https hosts (apex and any
// subdomain) may be allowlisted by the origin fix: the same domains the
// default allowlist (PortalOrigins) is built from.
var productDomains = installenv.ProductDomains

// isProductOrigin reports whether origin (already validated) is an https
// origin on a product domain, or a plain-http loopback dev origin
// (http://localhost[:port] / http://127.0.0.1[:port]).
func isProductOrigin(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if port != "" {
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			return false
		}
	}
	switch u.Scheme {
	case "http":
		return host == "localhost" || host == "127.0.0.1"
	case "https":
		if port != "" && port != "443" {
			return false
		}
		for _, d := range productDomains {
			if host == d || strings.HasSuffix(host, "."+d) {
				return true
			}
		}
	}
	return false
}

// proxySetupFixToController forwards a privileged fix to the controller's
// POST /setup/{id} and relays its answer ({check} or {error}) unchanged. The
// controller derives the daemon path itself; no body is sent.
func (s *Server) proxySetupFixToController(w http.ResponseWriter, r *http.Request, id string) {
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, s.controllerURLOrDefault()+"/setup/"+id, bytes.NewReader([]byte("{}")))
	if err != nil {
		s.sendErrorResponse(w, http.StatusInternalServerError, "PROXY_FAILED", err.Error(), nil)
		return
	}
	setup.SetMutationHeaders(req.Header)
	client := &http.Client{Timeout: setupFixTimeout}
	resp, err := client.Do(req)
	if err != nil {
		s.sendErrorResponse(w, http.StatusBadGateway, "CONTROLLER_UNREACHABLE",
			"controller not reachable on "+s.controllerURLOrDefault()+"; is the StonkAgents Controller service running?", nil)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
}

// registerSetupRoutes wires the setup surface on mux.
func (s *Server) registerSetupRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/setup/status", s.handleSetupStatus)
	mux.HandleFunc("GET /api/v1/setup/identity", s.handleSetupIdentityGet)
	mux.HandleFunc("GET /api/v1/setup/gateway", s.handleSetupGatewayGet)
	mux.HandleFunc("POST /api/v1/setup/identity", s.handleSetupIdentitySet) // literal beats {id} below
	s.registerAutopilotRoutes(mux)                                          // /api/v1/setup/autopilot[/suggestions...]
	mux.HandleFunc("POST /api/v1/setup/{id}", s.handleSetupFix)
}

// buildSetupMux returns a mux with only the setup routes (tests).
func (s *Server) buildSetupMux() http.Handler {
	mux := http.NewServeMux()
	s.registerSetupRoutes(mux)
	return CORSMiddleware(s.corsPolicy())(mux)
}
