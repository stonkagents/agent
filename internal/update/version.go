// Package update provides shared auto-update types and functions used by both
// the sign-manifest CLI (signer) and the controller (verifier).
//
// Feature: F-025 (Auto-Update System)
// Story: US-025-03 (Tracker Version Awareness)
// Purpose: Semver comparison for determining update eligibility
package update

import (
	"strings"

	"golang.org/x/mod/semver"
)

// VersionEligible returns true if current is older than target, meaning the
// node should be offered the update. Non-semver, "dev", and empty current
// versions are always considered eligible (nodes with unknown versions should
// accept updates rather than silently stay stale).
//
// Both "1.2.0" and "v1.2.0" formats are accepted — the v-prefix is normalized
// internally. If target is not valid semver, returns false (cannot determine
// eligibility without a valid target to compare against).
func VersionEligible(current, target string) bool {
	normalizedTarget := ensureVPrefix(target)
	if !semver.IsValid(normalizedTarget) {
		return false
	}

	normalizedCurrent := ensureVPrefix(current)
	if !semver.IsValid(normalizedCurrent) {
		// Non-semver, "dev", empty — always eligible
		return true
	}

	return semver.Compare(normalizedCurrent, normalizedTarget) < 0
}

// ensureVPrefix normalizes a version string for golang.org/x/mod/semver,
// which requires the "v" prefix. Handles both "1.2.0" → "v1.2.0" and
// "v1.2.0" → "v1.2.0" (no double-prefix).
func ensureVPrefix(v string) string {
	if strings.HasPrefix(v, "v") {
		return v
	}
	return "v" + v
}
