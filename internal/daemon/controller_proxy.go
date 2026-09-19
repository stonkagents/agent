// Package: internal/daemon
// Feature: F-025 (Auto-Update System)
// Story: US-025-04 (Daemon Update Relay)
// Purpose: Reverse proxy from daemon /api/v1/controller/* to controller on port 7840.
//          Frontend talks to daemon only; daemon fans out to controller for update ops.

package daemon

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

// controllerProxy returns an http.Handler that reverse-proxies requests
// to the controller, stripping the /api/v1/controller prefix.
func (s *Server) controllerProxy() http.Handler {
	target, err := url.Parse(s.controllerURL)
	if err != nil {
		// controllerURL is a constant; parse failure means a programming error.
		panic("invalid controllerURL: " + err.Error())
	}

	proxy := httputil.NewSingleHostReverseProxy(target)

	// Custom director: strip /api/v1/controller prefix before forwarding.
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.URL.Path = strings.TrimPrefix(req.URL.Path, "/api/v1/controller")
		if req.URL.Path == "" {
			req.URL.Path = "/"
		}
		req.URL.RawPath = "" // reset — the trimmed path is clean
	}

	// Strip CORS headers from controller response — the daemon's CORSMiddleware
	// is the single source of truth for CORS policy. Without this, the controller's
	// own corsMiddleware sets Access-Control-Allow-Origin, and then the daemon's
	// middleware sets it again, resulting in duplicate values that browsers reject.
	proxy.ModifyResponse = func(resp *http.Response) error {
		resp.Header.Del("Access-Control-Allow-Origin")
		resp.Header.Del("Access-Control-Allow-Methods")
		resp.Header.Del("Access-Control-Allow-Headers")
		resp.Header.Del("Access-Control-Max-Age")
		resp.Header.Del("Access-Control-Allow-Private-Network")
		return nil
	}

	// On error (controller down), return 502 Bad Gateway.
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) {
		w.WriteHeader(http.StatusBadGateway)
	}

	return proxy
}

// buildControllerProxyMux creates an http.ServeMux with only the controller
// proxy route registered. Used in tests to isolate proxy behavior.
func (s *Server) buildControllerProxyMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("/api/v1/controller/", s.controllerProxy())
	return mux
}
