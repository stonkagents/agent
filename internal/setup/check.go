// Package setup implements the agent setup checks shared by the daemon
// (GET/POST /api/v1/setup/*), the controller (privileged fixes) and the CLI
// `doctor` command.
//
// Contract (portal: apps/portal/src/lib/api/daemon-setup.ts):
//
//	SetupCheck = { id, status: 'ok'|'missing'|'failed', message?, detail? }
//
// Windows-specific pieces (firewall, autostart, D:\ candidates) are behind
// build tags; on other platforms they report status "missing" with message
// "Windows only" so the portal and doctor render an honest row.
package setup

// Status is the state of one setup check.
type Status string

const (
	StatusOK      Status = "ok"
	StatusMissing Status = "missing"
	StatusFailed  Status = "failed"
)

// Check ids, in the order the portal expects them.
const (
	IDService    = "service"
	IDController = "controller"
	IDFirewall   = "firewall"
	IDP2P        = "p2p"
	IDTracker    = "tracker"
	IDStorage    = "storage"
	IDBandwidth  = "bandwidth"
	IDAutostart  = "autostart"
	IDOrigin     = "origin"
)

// CheckIDs lists every check id in evaluation order.
var CheckIDs = []string{
	IDService, IDController, IDFirewall, IDP2P, IDTracker,
	IDStorage, IDBandwidth, IDAutostart, IDOrigin,
}

// FixableIDs are the checks that accept POST /api/v1/setup/{id}.
var FixableIDs = map[string]bool{
	IDFirewall: true, IDStorage: true, IDBandwidth: true, IDAutostart: true, IDOrigin: true,
}

// PrivilegedIDs are the fixes the daemon proxies to the controller service.
var PrivilegedIDs = map[string]bool{IDFirewall: true, IDAutostart: true}

// Check is one row of the setup status.
type Check struct {
	ID      string         `json:"id"`
	Status  Status         `json:"status"`
	Message string         `json:"message,omitempty"`
	Detail  map[string]any `json:"detail,omitempty"`
}

// OK builds an ok check.
func OK(id string, detail map[string]any) Check {
	return Check{ID: id, Status: StatusOK, Detail: detail}
}

// Missing builds a missing check with a one-line message.
func Missing(id, message string, detail map[string]any) Check {
	return Check{ID: id, Status: StatusMissing, Message: message, Detail: detail}
}

// Failed builds a failed check with a one-line message.
func Failed(id, message string, detail map[string]any) Check {
	return Check{ID: id, Status: StatusFailed, Message: message, Detail: detail}
}

// WindowsOnly is the stub result for a Windows-only check on another OS.
func WindowsOnly(id string) Check {
	return Missing(id, "Windows only", nil)
}

// IsKnownID reports whether id is one of CheckIDs.
func IsKnownID(id string) bool {
	for _, known := range CheckIDs {
		if known == id {
			return true
		}
	}
	return false
}

// Response wraps a status list: GET /api/v1/setup/status → {checks}.
type Response struct {
	Checks []Check `json:"checks"`
}

// FixResponse wraps a single re-evaluated check: POST /api/v1/setup/{id} → {check}.
type FixResponse struct {
	Check Check `json:"check"`
}
