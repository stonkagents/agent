// Package daemon: the OpenClaw gateway link for the portal.
//
//	GET /api/v1/setup/gateway   200 {data: {url, dashboardUrl, running}}
//
// Loopback only: dashboardUrl carries the gateway token in the URL fragment,
// the same link `stonkagents dashboard` copies, so the browser opens the
// gateway's own UI already signed in. `url` is the bare gateway base.
package daemon

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/stonkagents/agent/internal/installenv"
)

type gatewayLinkDTO struct {
	URL          string `json:"url"`
	DashboardURL string `json:"dashboardUrl"`
	Running      bool   `json:"running"`
}

type gatewayLinkResponse struct {
	Data gatewayLinkDTO `json:"data"`
}

// gatewayBaseURL is the configured gateway base, else this environment's default.
func (s *Server) gatewayBaseURL() string {
	if s.config != nil {
		if u := strings.TrimSpace(s.config.GatewayURL); u != "" {
			return strings.TrimRight(u, "/")
		}
	}
	return installenv.Current().GatewayURL()
}

// gatewayDashboardURL is the gateway UI link with the token in the fragment.
func gatewayDashboardURL(base, token string) string {
	link := strings.TrimRight(base, "/") + "/"
	if token == "" {
		return link
	}
	return link + "#token=" + url.QueryEscape(token)
}

// gatewayRunning answers whether anything listens on the gateway base URL.
func gatewayRunning(ctx context.Context, base string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/", nil)
	if err != nil {
		return false
	}
	client := &http.Client{Timeout: 1500 * time.Millisecond}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return true
}

// handleSetupGatewayGet handles GET /api/v1/setup/gateway.
func (s *Server) handleSetupGatewayGet(w http.ResponseWriter, r *http.Request) {
	if !isLoopbackRequest(r) {
		s.sendErrorResponse(w, http.StatusForbidden, "FORBIDDEN", "Only localhost requests allowed", nil)
		return
	}
	base := s.gatewayBaseURL()
	token := ""
	if s.config != nil {
		token = s.config.GatewayToken
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	s.sendJSONResponse(w, http.StatusOK, gatewayLinkResponse{Data: gatewayLinkDTO{
		URL:          base,
		DashboardURL: gatewayDashboardURL(base, token),
		Running:      gatewayRunning(ctx, base),
	}})
}
