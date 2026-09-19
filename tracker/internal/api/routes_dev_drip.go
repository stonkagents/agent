package api

import (
	"net/http"

	"github.com/gorilla/mux"
	"github.com/stonkagents/agent/tracker/internal/ratelimit"
)

// setupDevDripRoutes registers the devnet drip route on the /api subrouter. Nothing is
// registered (404) unless the bootstrap wired a handler, which it does only with
// DEV_DRIP_SECRET_KEY set on a devnet launchpad.
//
// Public, rate limited by IP (10 per minute; the service enforces the real caps —
// one per wallet per 24 h, three per IP per hour — from the dev_drips table):
//
//	POST /api/dev/drip   {"wallet": "<base58>"}
func (s *Server) setupDevDripRoutes(portal *mux.Router) {
	if s.devDrip == nil {
		return
	}
	var drip http.Handler = http.HandlerFunc(s.devDrip.HandleDrip)
	if s.limiter != nil {
		drip = ratelimit.NewMiddleware(s.limiter, DevDripRateLimitConfig()).Wrap(drip)
	}
	portal.Handle("/dev/drip", drip).Methods(http.MethodPost)
}
