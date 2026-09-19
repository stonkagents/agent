// Package: internal/daemon
// Feature: Agent Autopilot (community board)
// Purpose: Owner-facing autopilot routes on the setup surface.
//
//	GET  /api/v1/setup/autopilot                          200 {data: {policy, status}}
//	POST /api/v1/setup/autopilot {partial policy}         200 {data: {policy, status}}
//	GET  /api/v1/setup/autopilot/suggestions              200 {data: [suggestion]}
//	POST /api/v1/setup/autopilot/suggestions/{id}/approve 200 {data: {replyId}}
//	POST /api/v1/setup/autopilot/suggestions/{id}/dismiss 200 {data: {dismissed: true}}
//	GET  /api/v1/setup/autopilot/events                   200 {data: [event]}
//	                                                      4xx/5xx {error: {code, message}}
//
// Same loopback, CORS, mutation-header and rate-limit rules as the other
// setup routes (see setup.go). A POSTed policy is a partial: absent fields
// keep their value, officeHours: null clears the window. The merged policy is
// validated as a whole, written to config.yaml (autopilot:) and applied live;
// turning autopilot on triggers an immediate watcher run. The events route
// lists what the watcher did on its own (digest_posted) for the portal bell.

package daemon

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/stonkagents/agent/internal/config"
)

// autopilotStatusDTO is the runtime half of GET/POST /api/v1/setup/autopilot.
type autopilotStatusDTO struct {
	Enabled            bool   `json:"enabled"`
	LastRunAt          string `json:"lastRunAt,omitempty"` // RFC3339, absent before the first run
	RepliesToday       int    `json:"repliesToday"`
	CreditsSpentToday  int    `json:"creditsSpentToday"`
	SuggestionsPending int    `json:"suggestionsPending"`
	DowngradedReason   string `json:"downgradedReason,omitempty"`
	// DigestLastPostedAt is when the weekly digest was last posted (RFC3339),
	// absent until the first one.
	DigestLastPostedAt string `json:"digestLastPostedAt,omitempty"`
	// NextDigestAt is the next digest slot (RFC3339 with the host's offset),
	// absent while the digest is off.
	NextDigestAt string `json:"nextDigestAt,omitempty"`
	// Phase 3: scoring counters of the last 30 days, the outcome ledger
	// summary with its 30-day strip, and the self-tuned per-category
	// thresholds (empty object when nothing is tuned) with when they were
	// computed (absent before the first tuning).
	Relevance       autopilotRelevanceCounters         `json:"relevance"`
	Ledger          autopilotLedgerDTO                 `json:"ledger"`
	TunedThresholds map[string]autopilotTunedThreshold `json:"tunedThresholds"`
	TunedAt         string                             `json:"tunedAt,omitempty"`
}

type autopilotDTO struct {
	Policy config.AutopilotConfig `json:"policy"`
	Status autopilotStatusDTO     `json:"status"`
}

type autopilotResponse struct {
	Data autopilotDTO `json:"data"`
}

type autopilotSuggestionsResponse struct {
	Data []autopilotSuggestion `json:"data"`
}

type autopilotEventsResponse struct {
	Data []autopilotEvent `json:"data"`
}

// autopilotPolicyPatch is the POST body: every field optional.
type autopilotPolicyPatch struct {
	Mode                *string         `json:"mode"`
	Categories          *[]string       `json:"categories"`
	DailyCreditCap      *int            `json:"dailyCreditCap"`
	MaxRepliesPerDay    *int            `json:"maxRepliesPerDay"`
	MinBountyMultiple   *float64        `json:"minBountyMultiple"`
	BalanceFloor        *int            `json:"balanceFloor"`
	ThreadCooldownHours *int            `json:"threadCooldownHours"`
	MaxPostAgeHours     *int            `json:"maxPostAgeHours"`
	Instruction         *string         `json:"instruction"`
	OfficeHours         json.RawMessage `json:"officeHours"` // absent = keep, null = clear, object = set
	Digest              json.RawMessage `json:"digest"`      // absent = keep, object = merge (missing keys keep their value)
	// Phase 3.
	RelevanceThreshold           *float64        `json:"relevanceThreshold"`
	RelevanceMode                *string         `json:"relevanceMode"`
	RelevanceThresholdByCategory json.RawMessage `json:"relevanceThresholdByCategory"` // absent = keep, null or {} = clear, object = replace
}

// autopilotDigestPatch is the digest sub-object of the POST body.
type autopilotDigestPatch struct {
	Enabled *bool `json:"enabled"`
	Weekday *int  `json:"weekday"`
	Hour    *int  `json:"hour"`
}

// apply merges the patch into base and returns the result (unvalidated).
func (p autopilotPolicyPatch) apply(base config.AutopilotConfig) (config.AutopilotConfig, error) {
	out := base
	if p.Mode != nil {
		out.Mode = *p.Mode
	}
	if p.Categories != nil {
		out.Categories = append([]string{}, (*p.Categories)...)
	}
	if p.DailyCreditCap != nil {
		out.DailyCreditCap = *p.DailyCreditCap
	}
	if p.MaxRepliesPerDay != nil {
		out.MaxRepliesPerDay = *p.MaxRepliesPerDay
	}
	if p.MinBountyMultiple != nil {
		out.MinBountyMultiple = *p.MinBountyMultiple
	}
	if p.BalanceFloor != nil {
		out.BalanceFloor = *p.BalanceFloor
	}
	if p.ThreadCooldownHours != nil {
		out.ThreadCooldownHours = *p.ThreadCooldownHours
	}
	if p.MaxPostAgeHours != nil {
		out.MaxPostAgeHours = *p.MaxPostAgeHours
	}
	if p.Instruction != nil {
		out.Instruction = *p.Instruction
	}
	if len(p.OfficeHours) > 0 {
		if string(p.OfficeHours) == "null" {
			out.OfficeHours = nil
		} else {
			var oh config.AutopilotOfficeHours
			if err := json.Unmarshal(p.OfficeHours, &oh); err != nil {
				return out, badRequest("officeHours must be an object {start, end, tz?} or null")
			}
			out.OfficeHours = &oh
		}
	} else if base.OfficeHours != nil {
		copied := *base.OfficeHours
		out.OfficeHours = &copied
	}
	if len(p.Digest) > 0 && string(p.Digest) != "null" {
		var dp autopilotDigestPatch
		if err := json.Unmarshal(p.Digest, &dp); err != nil {
			return out, badRequest("digest must be an object {enabled, weekday?, hour?}")
		}
		if dp.Enabled != nil {
			out.Digest.Enabled = *dp.Enabled
		}
		if dp.Weekday != nil {
			out.Digest.Weekday = *dp.Weekday
		}
		if dp.Hour != nil {
			out.Digest.Hour = *dp.Hour
		}
	}
	if p.RelevanceThreshold != nil {
		out.RelevanceThreshold = *p.RelevanceThreshold
	}
	if p.RelevanceMode != nil {
		out.RelevanceMode = *p.RelevanceMode
	}
	if len(p.RelevanceThresholdByCategory) > 0 {
		if string(p.RelevanceThresholdByCategory) == "null" {
			out.RelevanceThresholdByCategory = nil
		} else {
			var pins map[string]float64
			if err := json.Unmarshal(p.RelevanceThresholdByCategory, &pins); err != nil {
				return out, badRequest("relevanceThresholdByCategory must be an object of category to threshold, or null")
			}
			out.RelevanceThresholdByCategory = pins
		}
	} else if base.RelevanceThresholdByCategory != nil {
		copied := make(map[string]float64, len(base.RelevanceThresholdByCategory))
		for k, v := range base.RelevanceThresholdByCategory {
			copied[k] = v
		}
		out.RelevanceThresholdByCategory = copied
	}
	return out, nil
}

// autopilotDTOFor builds the GET/POST payload from the live state.
func (s *Server) autopilotDTOFor() autopilotDTO {
	st := s.autopilotSt()
	replies, credits := st.store.counters()
	pending := len(st.store.pendingSuggestions())
	digestAt := ""
	for _, ev := range st.store.events() {
		if ev.Kind == autopilotEventDigestPosted {
			digestAt = ev.CreatedAt.UTC().Format(time.RFC3339)
			break
		}
	}
	lastWeek := st.store.digestLastWeek()
	now := st.now()
	tuned, tunedAt := st.store.tuned()
	relevance := st.store.relevanceCounters(now)
	ledger := st.ledger.summary(now)
	st.mu.Lock()
	defer st.mu.Unlock()
	status := autopilotStatusDTO{
		Enabled:            st.policy.Enabled(),
		RepliesToday:       replies,
		CreditsSpentToday:  credits,
		SuggestionsPending: pending,
		DowngradedReason:   st.downgradedReason,
		DigestLastPostedAt: digestAt,
		Relevance:          relevance,
		Ledger:             ledger,
		TunedThresholds:    tuned,
	}
	if !tunedAt.IsZero() {
		status.TunedAt = tunedAt.UTC().Format(time.RFC3339)
	}
	if !st.lastRunAt.IsZero() {
		status.LastRunAt = st.lastRunAt.UTC().Format(time.RFC3339)
	}
	if next := autopilotNextDigestAt(st.policy.Digest, st.now(), lastWeek); !next.IsZero() {
		status.NextDigestAt = next.Format(time.RFC3339)
	}
	if st.now().Before(st.backoffUntil) && status.DowngradedReason == "" {
		status.DowngradedReason = "paused until " + st.backoffUntil.UTC().Format(time.RFC3339)
	}
	if st.now().Before(st.trackerCapUntil) {
		status.DowngradedReason = autopilotTrackerCapReason(st.trackerCapUntil)
	}
	policy := st.policy
	if policy.Categories == nil {
		policy.Categories = []string{}
	}
	return autopilotDTO{Policy: policy, Status: status}
}

// autopilotGuard applies the setup-surface access rules; false means a
// response was already written.
func (s *Server) autopilotGuard(w http.ResponseWriter, r *http.Request, mutation bool) bool {
	if !isLoopbackRequest(r) {
		s.sendErrorResponse(w, http.StatusForbidden, "FORBIDDEN", "Only localhost requests allowed", nil)
		return false
	}
	if !mutation {
		return true
	}
	if !hasSetupMutationHeaders(r) {
		s.sendErrorResponse(w, http.StatusForbidden, "FORBIDDEN",
			"setup changes require Content-Type: application/json and "+SetupHeader+": 1", nil)
		return false
	}
	if !s.setupSt().limiter.Allow() {
		s.sendErrorResponse(w, http.StatusTooManyRequests, "RATE_LIMITED", "too many setup changes; wait a second and retry", nil)
		return false
	}
	return true
}

// handleSetupAutopilotGet handles GET /api/v1/setup/autopilot.
func (s *Server) handleSetupAutopilotGet(w http.ResponseWriter, r *http.Request) {
	if !s.autopilotGuard(w, r, false) {
		return
	}
	s.sendJSONResponse(w, http.StatusOK, autopilotResponse{Data: s.autopilotDTOFor()})
}

// handleSetupAutopilotSet handles POST /api/v1/setup/autopilot.
func (s *Server) handleSetupAutopilotSet(w http.ResponseWriter, r *http.Request) {
	if !s.autopilotGuard(w, r, true) {
		return
	}
	var patch autopilotPolicyPatch
	if err := decodeOptionalJSON(r, &patch); err != nil {
		s.sendSetupError(w, err)
		return
	}
	merged, err := patch.apply(s.autopilotPolicy())
	if err != nil {
		s.sendSetupError(w, err)
		return
	}
	if err := merged.Validate(); err != nil {
		s.sendErrorResponse(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error(), nil)
		return
	}
	if err := config.UpdateFile(func(doc map[string]any) error {
		doc["autopilot"] = config.AutopilotToYAML(merged)
		return nil
	}); err != nil {
		s.sendErrorResponse(w, http.StatusInternalServerError, "FIX_FAILED", err.Error(), nil)
		return
	}
	s.setAutopilotPolicy(merged)
	s.autopilotLog("policy", map[string]interface{}{
		"decision": "policy_saved", "mode": merged.Mode, "categories": merged.Categories,
		"daily_credit_cap": merged.DailyCreditCap, "max_replies_per_day": merged.MaxRepliesPerDay,
		"digest_enabled": merged.Digest.Enabled, "digest_weekday": merged.Digest.Weekday, "digest_hour": merged.Digest.Hour,
		"relevance_threshold": merged.RelevanceThreshold, "relevance_mode": merged.RelevanceMode, "relevance_pins": len(merged.RelevanceThresholdByCategory),
	})
	s.sendJSONResponse(w, http.StatusOK, autopilotResponse{Data: s.autopilotDTOFor()})
}

// handleSetupAutopilotSuggestions handles GET /api/v1/setup/autopilot/suggestions.
func (s *Server) handleSetupAutopilotSuggestions(w http.ResponseWriter, r *http.Request) {
	if !s.autopilotGuard(w, r, false) {
		return
	}
	list := s.autopilotSt().store.pendingSuggestions()
	if list == nil {
		list = []autopilotSuggestion{}
	}
	s.sendJSONResponse(w, http.StatusOK, autopilotSuggestionsResponse{Data: list})
}

// handleSetupAutopilotEvents handles GET /api/v1/setup/autopilot/events: what
// the watcher did on its own that the owner should hear about (digest_posted),
// newest first, at most autopilotMaxEvents.
func (s *Server) handleSetupAutopilotEvents(w http.ResponseWriter, r *http.Request) {
	if !s.autopilotGuard(w, r, false) {
		return
	}
	list := s.autopilotSt().store.events()
	if list == nil {
		list = []autopilotEvent{}
	}
	s.sendJSONResponse(w, http.StatusOK, autopilotEventsResponse{Data: list})
}

// handleSetupAutopilotApprove handles POST /api/v1/setup/autopilot/suggestions/{id}/approve:
// posts the stored draft as an autopilot reply and records the outcome.
func (s *Server) handleSetupAutopilotApprove(w http.ResponseWriter, r *http.Request) {
	if !s.autopilotGuard(w, r, true) {
		return
	}
	st := s.autopilotSt()
	sg, ok := st.store.takeSuggestion(r.PathValue("id"))
	if !ok {
		s.sendErrorResponse(w, http.StatusNotFound, "NOT_FOUND", "suggestion not found or expired", nil)
		return
	}
	apiKey := s.getTrackerAPIKey()
	if apiKey == "" || s.config == nil || s.config.TrackerURL == "" {
		st.store.restoreSuggestion(sg)
		s.sendErrorResponse(w, http.StatusServiceUnavailable, "PORTAL_PROXY_UNAVAILABLE",
			"Tracker API key not available; daemon must register with tracker first.", nil)
		return
	}
	if st.store.replied(sg.PostID) {
		s.sendErrorResponse(w, http.StatusConflict, "ALREADY_REPLIED", "the agent already replied in this thread", nil)
		return
	}
	replyID, status, code, msg := s.autopilotPostReplyWith(r.Context(), apiKey, sg.PostID, sg.Draft, autopilotReplyOpts{relevance: sg.Relevance, signals: sg.RelevanceSignals})
	if status != 0 {
		st.store.restoreSuggestion(sg)
		if status == http.StatusTooManyRequests && code == autopilotTrackerReplyCap {
			s.autopilotTrackerCapReached(st.now()) // the tracker's daily cap, not a backoff
		} else if status == http.StatusPaymentRequired || status == http.StatusTooManyRequests {
			s.autopilotBackOff(st.now(), "tracker answered "+code+" on approve")
		}
		if status < 400 || status > 599 {
			status = http.StatusBadGateway
		}
		s.sendErrorResponse(w, status, code, msg, nil)
		return
	}
	bounty := 0
	if sg.Bounty != nil {
		bounty = sg.Bounty.Amount
	}
	if err := st.store.recordReply(autopilotOutcome{
		ReplyID: replyID, PostID: sg.PostID, At: st.now(), Trigger: "approve", Bounty: bounty, Credits: sg.EstimatedCredits,
		Relevance: sg.Relevance, RelevanceSignals: sg.RelevanceSignals,
	}); err != nil {
		s.autopilotWarn("store", "Could not persist approved reply: "+err.Error())
	}
	if sg.LedgerID != "" {
		if err := st.ledger.markPosted(sg.LedgerID, replyID, st.now()); err != nil {
			s.autopilotWarn("ledger", "Could not persist ledger row: "+err.Error())
		}
	}
	// An approved answer to a raised bounty closes our ask on the post.
	if _, asked := st.store.ask(sg.PostID); asked {
		if err := st.store.markAskAnswered(sg.PostID, st.now()); err != nil {
			s.autopilotWarn("store", "Could not persist ask: "+err.Error())
		}
	}
	s.autopilotLog("approve", map[string]interface{}{"post_id": sg.PostID, "suggestion_id": sg.ID, "decision": "post", "reason": "approved", "reply_id": replyID})
	s.sendJSONResponse(w, http.StatusOK, map[string]interface{}{"data": map[string]string{"replyId": replyID}})
}

// handleSetupAutopilotDismiss handles POST /api/v1/setup/autopilot/suggestions/{id}/dismiss.
func (s *Server) handleSetupAutopilotDismiss(w http.ResponseWriter, r *http.Request) {
	if !s.autopilotGuard(w, r, true) {
		return
	}
	st := s.autopilotSt()
	sg, ok := st.store.takeSuggestion(r.PathValue("id"))
	if !ok {
		s.sendErrorResponse(w, http.StatusNotFound, "NOT_FOUND", "suggestion not found or expired", nil)
		return
	}
	s.autopilotLog("dismiss", map[string]interface{}{"post_id": sg.PostID, "suggestion_id": sg.ID, "decision": "dismiss", "reason": "owner"})
	s.sendJSONResponse(w, http.StatusOK, map[string]interface{}{"data": map[string]bool{"dismissed": true}})
}

// sendSetupError maps a decode/merge error to the setup error envelope.
func (s *Server) sendSetupError(w http.ResponseWriter, err error) {
	var se *setupError
	if errors.As(err, &se) {
		s.sendErrorResponse(w, se.status, se.code, se.Error(), nil)
		return
	}
	s.sendErrorResponse(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error(), nil)
}

// registerAutopilotRoutes wires the autopilot routes on the setup mux. The
// literal paths beat POST /api/v1/setup/{id}; the suggestion routes have more
// segments than {id} can match.
func (s *Server) registerAutopilotRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/setup/autopilot", s.handleSetupAutopilotGet)
	mux.HandleFunc("POST /api/v1/setup/autopilot", s.handleSetupAutopilotSet)
	mux.HandleFunc("GET /api/v1/setup/autopilot/suggestions", s.handleSetupAutopilotSuggestions)
	mux.HandleFunc("GET /api/v1/setup/autopilot/events", s.handleSetupAutopilotEvents)
	mux.HandleFunc("POST /api/v1/setup/autopilot/suggestions/{id}/approve", s.handleSetupAutopilotApprove)
	mux.HandleFunc("POST /api/v1/setup/autopilot/suggestions/{id}/dismiss", s.handleSetupAutopilotDismiss)
}
