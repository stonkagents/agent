package api

import (
	"net/http"

	"github.com/gorilla/mux"
	"github.com/stonkagents/agent/tracker/internal/ratelimit"
)

// setupFeedbackRoutes registers the portal feedback routes on the /api subrouter.
//
// Public, rate limited by IP (5 per minute):
//
//	POST /api/v1/feedback        store a submission, forward to the chat webhook best-effort;
//	                             X-Turnstile-Token is verified when TURNSTILE_SECRET_KEY is set
//
// Admin (shared secret header X-Admin-Key = ADMIN_API_KEY; 503 ADMIN_DISABLED without it):
//
//	GET  /api/v1/admin/feedback  newest-first page of submissions (?limit=&cursor=)
func (s *Server) setupFeedbackRoutes(portal *mux.Router) {
	if s.feedback == nil {
		return
	}
	var submit http.Handler = http.HandlerFunc(s.feedback.HandleSubmit)
	if s.limiter != nil {
		submit = ratelimit.NewMiddleware(s.limiter, FeedbackRateLimitConfig()).Wrap(submit)
	}
	portal.Handle("/v1/feedback", submit).Methods(http.MethodPost)

	// Always registered so an operator sees "ADMIN_API_KEY is not configured" (503), not a
	// generic 404 that looks like a wrong path.
	var admin http.Handler = http.HandlerFunc(s.feedback.HandleAdminList)
	if s.limiter != nil {
		admin = ratelimit.NewMiddleware(s.limiter, FeedbackAdminRateLimitConfig()).Wrap(admin)
	}
	portal.Handle("/v1/admin/feedback", admin).Methods(http.MethodGet)
}
