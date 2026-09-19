package setup

import (
	"mime"
	"net/http"
	"strings"
)

// MutationHeader must accompany every setup POST (daemon /api/v1/setup/{id}
// and controller /setup/{id}) with MutationHeaderValue. It is a CSRF
// defence: a custom header forces browsers to send a CORS preflight, which
// the servers only answer for allowlisted origins, so a hostile page cannot
// fire a mutating request with mode:"no-cors". Together with the JSON content
// type it rules out form and text/plain posts that need no preflight.
const (
	MutationHeader      = "X-StonkAgents-Setup"
	MutationHeaderValue = "1"
)

// HasMutationHeaders reports whether r carries Content-Type: application/json
// (parameters such as charset are fine) and MutationHeader: MutationHeaderValue.
// A request with Origin: null is refused regardless: the daemon allowlists
// "null" for the file:// SPA, but a sandboxed iframe on any site also sends
// Origin: null and would otherwise pass the preflight that the custom header
// is meant to force. Setup POSTs come from the portal (https) or the CLI (no
// Origin), never from a null origin.
func HasMutationHeaders(r *http.Request) bool {
	if strings.TrimSpace(r.Header.Get(MutationHeader)) != MutationHeaderValue {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(r.Header.Get("Origin")), "null") {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return err == nil && strings.EqualFold(mediaType, "application/json")
}

// SetMutationHeaders adds the headers HasMutationHeaders requires to h
// (used by the daemon's controller proxy and the CLI).
func SetMutationHeaders(h http.Header) {
	h.Set("Content-Type", "application/json")
	h.Set(MutationHeader, MutationHeaderValue)
}
