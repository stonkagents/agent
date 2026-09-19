// Package: internal/daemon
// Feature: Agent identity (portal Identity tab)
// Purpose: Owner-set display name on the setup surface.
//
//	GET  /api/v1/setup/identity                 200 {data: {peerId, displayName}}
//	POST /api/v1/setup/identity {displayName}   200 {data: {peerId, displayName, trackerSynced, trackerError?}}
//	                                            4xx {error: {code, message}}
//
// Same loopback, CORS and mutation-header rules as the other setup routes (see
// setup.go). The name is written to config.yaml (display_name), applied to the
// running config, and pushed to the tracker at once so the public pages show it
// within seconds instead of at the next heartbeat. The tracker sanitizes again
// on its side (tracker/internal/models SanitizeDisplayName) with the same cap.
//
// Every agent has a name: on startup (StartP2P) a missing display_name becomes the
// placeholder config.DefaultDisplayName(peerID), and clearing through POST resets
// to that placeholder. The tracker treats a placeholder as replaceable (it names
// the agent after its token at launch claim) and never lets one overwrite a name
// it already holds.

package daemon

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/stonkagents/agent/internal/config"
)

// maxDisplayNameRunes caps the display name in characters (runes), matching the
// tracker's limit so a saved name is never truncated on the way.
const maxDisplayNameRunes = 50

// identityPushWait bounds how long POST /api/v1/setup/identity waits for the
// tracker to acknowledge the new name before answering. The push keeps running
// in the background past it; the response then says trackerSynced: false.
const identityPushWait = 5 * time.Second

// identityDTO is the data payload of both identity routes.
type identityDTO struct {
	PeerID      string `json:"peerId"`
	DisplayName string `json:"displayName"`
	// PublicKey is the agent's Ed25519 public key as stored in config (base64);
	// empty until the identity exists. Never the private key.
	PublicKey string `json:"publicKey,omitempty"`
	// TrackerSynced (POST only) reports whether the tracker acknowledged the
	// new name before the response was sent.
	TrackerSynced *bool `json:"trackerSynced,omitempty"`
	// TrackerError (POST only) explains a false TrackerSynced; the name is
	// saved regardless and travels with the next registration or heartbeat.
	TrackerError string `json:"trackerError,omitempty"`
}

type identityResponse struct {
	Data identityDTO `json:"data"`
}

// publicKey returns the configured public key under the config lock.
func (s *Server) publicKey() string {
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	if s.config == nil {
		return ""
	}
	return s.config.PublicKey
}

// displayName returns the configured display name under the config lock.
func (s *Server) displayName() string {
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	if s.config == nil {
		return ""
	}
	return s.config.DisplayName
}

// setDisplayName updates the in-memory config under the config lock.
func (s *Server) setDisplayName(name string) {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	if s.config != nil {
		s.config.DisplayName = name
	}
}

// ensureDefaultDisplayName gives the agent a placeholder name when config.yaml has
// none (first start after install, or the key was reused on a fresh config). The
// name is derived from the peer id (config.DefaultDisplayName), persisted, and
// applied live so the first registration already carries it. Called from StartP2P
// once the libp2p id is known; safe to call again (no-op when a name exists).
func (s *Server) ensureDefaultDisplayName() {
	if s.displayName() != "" {
		return
	}
	name := config.DefaultDisplayName(s.identityPeerID())
	if err := config.UpdateFile(func(doc map[string]any) error {
		if existing, _ := doc["display_name"].(string); existing != "" {
			name = existing // written since we loaded (installer, owner); keep it
			return nil
		}
		doc["display_name"] = name
		return nil
	}); err != nil && s.logger != nil {
		s.logger.Warn("Daemon", "ensureDefaultDisplayName", fmt.Sprintf("Could not persist default display name: %v", err), nil)
	}
	s.setDisplayName(name)
	if s.logger != nil {
		s.logger.Info("Daemon", "ensureDefaultDisplayName", "Assigned default display name", map[string]interface{}{"display_name": name})
	}
}

// adoptTrackerDisplayName takes over the name the tracker holds for this peer
// when the local one is still the generated placeholder. Names set tracker-side
// (a token claim naming the agent <symbol>_agent, an operator backfill) would
// otherwise show publicly while Settings > Identity kept the placeholder. A name
// the owner typed locally is never replaced; a tracker placeholder is ignored.
func (s *Server) adoptTrackerDisplayName(trackerName string) {
	trackerName = strings.TrimSpace(trackerName)
	if trackerName == "" || config.IsPlaceholderDisplayName(trackerName) {
		return
	}
	local := s.displayName()
	if local == trackerName {
		return
	}
	// A real local name is kept, except the old token default <symbol>_agent, which
	// follows the tracker's renamed <SYMBOL>_Agent form.
	if local != "" && !config.IsPlaceholderDisplayName(local) && !isLegacyTokenDefaultRename(local, trackerName) {
		return
	}
	name, err := normalizeDisplayName(trackerName)
	if err != nil || name == "" {
		return
	}
	if err := config.UpdateFile(func(doc map[string]any) error {
		doc["display_name"] = name
		return nil
	}); err != nil && s.logger != nil {
		s.logger.Warn("Daemon", "adoptTrackerDisplayName", fmt.Sprintf("Could not persist display name: %v", err), nil)
	}
	s.setDisplayName(name)
	if s.logger != nil {
		s.logger.Info("Daemon", "adoptTrackerDisplayName", "Adopted the tracker's display name", map[string]interface{}{"display_name": name})
	}
}

// isLegacyTokenDefaultRename reports whether local is the old lowercase token
// default (<symbol>_agent) and tracker is the same name in the current
// <SYMBOL>_Agent form.
func isLegacyTokenDefaultRename(local, tracker string) bool {
	return legacyTokenDefault.MatchString(local) && tokenDefault.MatchString(tracker) && strings.EqualFold(local, tracker)
}

var (
	legacyTokenDefault = regexp.MustCompile("^[a-z0-9]+_agent$")
	tokenDefault       = regexp.MustCompile("^[A-Z0-9]+_Agent$")
)

// identityPeerID is the libp2p peer id shown next to the name: the live host's
// when P2P is up, else the configured one.
func (s *Server) identityPeerID() string {
	if id := s.GetPeerID(); id != "" {
		return id
	}
	if s.config != nil {
		return s.config.PeerID
	}
	return ""
}

// normalizeDisplayName trims, strips angle brackets and enforces the limits:
// no control characters, at most maxDisplayNameRunes characters. Empty is valid
// and means "no name".
func normalizeDisplayName(raw string) (string, error) {
	if !utf8.ValidString(raw) {
		return "", fmt.Errorf("displayName must be valid UTF-8")
	}
	name := strings.NewReplacer("<", "", ">", "").Replace(raw)
	name = strings.TrimSpace(name)
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("displayName must not contain control characters")
		}
	}
	if utf8.RuneCountInString(name) > maxDisplayNameRunes {
		return "", fmt.Errorf("displayName must be at most %d characters", maxDisplayNameRunes)
	}
	return name, nil
}

// handleSetupIdentityGet handles GET /api/v1/setup/identity.
func (s *Server) handleSetupIdentityGet(w http.ResponseWriter, r *http.Request) {
	if !isLoopbackRequest(r) {
		s.sendErrorResponse(w, http.StatusForbidden, "FORBIDDEN", "Only localhost requests allowed", nil)
		return
	}
	s.sendJSONResponse(w, http.StatusOK, identityResponse{Data: identityDTO{
		PeerID:      s.identityPeerID(),
		DisplayName: s.displayName(),
		PublicKey:   s.publicKey(),
	}})
}

// handleSetupIdentitySet handles POST /api/v1/setup/identity {displayName}.
func (s *Server) handleSetupIdentitySet(w http.ResponseWriter, r *http.Request) {
	if !isLoopbackRequest(r) {
		s.sendErrorResponse(w, http.StatusForbidden, "FORBIDDEN", "Only localhost requests allowed", nil)
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
	var body struct {
		DisplayName *string `json:"displayName"`
	}
	if err := decodeOptionalJSON(r, &body); err != nil {
		var se *setupError
		if errors.As(err, &se) {
			s.sendErrorResponse(w, se.status, se.code, se.Error(), nil)
			return
		}
		s.sendErrorResponse(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error(), nil)
		return
	}
	if body.DisplayName == nil {
		s.sendErrorResponse(w, http.StatusBadRequest, "INVALID_REQUEST", "displayName is required (empty string clears the name)", nil)
		return
	}
	name, err := normalizeDisplayName(*body.DisplayName)
	if err != nil {
		s.sendErrorResponse(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error(), nil)
		return
	}
	// Every agent has a name: clearing goes back to the generated placeholder.
	if name == "" {
		name = config.DefaultDisplayName(s.identityPeerID())
	}

	if err := config.UpdateFile(func(doc map[string]any) error {
		doc["display_name"] = name
		return nil
	}); err != nil {
		s.sendErrorResponse(w, http.StatusInternalServerError, "FIX_FAILED", err.Error(), nil)
		return
	}
	s.setDisplayName(name)

	synced, pushErr := s.pushDisplayNameBounded(name, identityPushWait)
	dto := identityDTO{PeerID: s.identityPeerID(), DisplayName: name, PublicKey: s.publicKey(), TrackerSynced: &synced}
	if pushErr != nil {
		dto.TrackerError = pushErr.Error()
	}
	s.sendJSONResponse(w, http.StatusOK, identityResponse{Data: dto})
}

// pushDisplayNameBounded runs pushDisplayNameToTracker in the background and
// waits at most wait for its outcome. A push still running at the deadline is
// reported as not synced but keeps going (the tracker gets the name anyway).
func (s *Server) pushDisplayNameBounded(name string, wait time.Duration) (bool, error) {
	if s.isStopping() {
		return false, fmt.Errorf("daemon is shutting down")
	}
	done := make(chan error, 1)
	s.goBackground(func() { done <- s.pushDisplayNameToTracker(name) })
	select {
	case err := <-done:
		if err != nil {
			if s.logger != nil {
				s.logger.Warn("Daemon", "setup/identity", fmt.Sprintf("Display name push failed: %v", err), nil)
			}
			return false, err
		}
		return true, nil
	case <-time.After(wait):
		return false, fmt.Errorf("tracker did not confirm within %s; the name is still being sent", wait)
	}
}

// pushDisplayNameToTracker sends the current name to the tracker now. With an
// API key it is one authenticated heartbeat carrying display_name (empty clears);
// without one the daemon re-registers, which carries the name too.
func (s *Server) pushDisplayNameToTracker(name string) error {
	if s.trackerClient == nil {
		return fmt.Errorf("tracker client not initialized")
	}
	apiKey := s.getTrackerAPIKey()
	if apiKey == "" {
		if s.p2pHost == nil {
			return fmt.Errorf("not registered with the tracker yet; the name is sent with the next registration")
		}
		return s.RegisterWithTracker()
	}
	version := ""
	if s.config != nil {
		version = s.config.Version
	}
	if err := s.trackerClient.SetDisplayName(apiKey, version, name); err != nil {
		return err
	}
	s.recordTrackerRegistration(nil)
	return nil
}
