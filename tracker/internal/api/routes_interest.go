package api

import (
	"net/http"

	"github.com/gorilla/mux"
	"github.com/stonkagents/agent/tracker/internal/ratelimit"
)

// setupInterestRoutes registers the roadmap interest routes on the /api subrouter.
//
// Public, rate limited by IP (5 per minute):
//
//	POST /api/v1/interest                store a capability vote, answer per-capability counts,
//	                                     forward to the chat webhook best-effort
//
// Admin (shared secret header X-Admin-Key = ADMIN_API_KEY; 503 ADMIN_DISABLED without it):
//
//	GET  /api/v1/admin/interest          newest-first page of submissions (?limit=&cursor=)
//	GET  /api/v1/admin/interest/summary  per-capability and per-priority counts
func (s *Server) setupInterestRoutes(portal *mux.Router) {
	if s.interest == nil {
		return
	}
	var submit http.Handler = http.HandlerFunc(s.interest.HandleSubmit)
	if s.limiter != nil {
		submit = ratelimit.NewMiddleware(s.limiter, InterestRateLimitConfig()).Wrap(submit)
	}
	portal.Handle("/v1/interest", submit).Methods(http.MethodPost)

	// Always registered: without ADMIN_API_KEY the handler answers 503 ADMIN_DISABLED.
	var list http.Handler = http.HandlerFunc(s.interest.HandleAdminList)
	var summary http.Handler = http.HandlerFunc(s.interest.HandleAdminSummary)
	if s.limiter != nil {
		mw := ratelimit.NewMiddleware(s.limiter, InterestAdminRateLimitConfig())
		list, summary = mw.Wrap(list), mw.Wrap(summary)
	}
	portal.Handle("/v1/admin/interest/summary", summary).Methods(http.MethodGet)
	portal.Handle("/v1/admin/interest", list).Methods(http.MethodGet)
}
