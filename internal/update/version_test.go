// Feature: F-025 (Auto-Update System)
// Story: US-025-03 (Tracker Version Awareness)
// Purpose: Tests for VersionEligible semver comparison used by tracker and controller
package update

import "testing"

func TestVersionEligible_OlderThanTarget_ReturnsTrue(t *testing.T) {
	if !VersionEligible("0.9.0", "1.0.0") {
		t.Error("0.9.0 should be eligible for update to 1.0.0")
	}
}

func TestVersionEligible_EqualToTarget_ReturnsFalse(t *testing.T) {
	if VersionEligible("1.0.0", "1.0.0") {
		t.Error("1.0.0 should NOT be eligible for update to 1.0.0")
	}
}

func TestVersionEligible_NewerThanTarget_ReturnsFalse(t *testing.T) {
	if VersionEligible("1.1.0", "1.0.0") {
		t.Error("1.1.0 should NOT be eligible for update to 1.0.0")
	}
}

func TestVersionEligible_DevVersion_AlwaysEligible(t *testing.T) {
	if !VersionEligible("dev", "1.0.0") {
		t.Error("dev version should always be eligible for update")
	}
}

func TestVersionEligible_EmptyVersion_AlwaysEligible(t *testing.T) {
	if !VersionEligible("", "1.0.0") {
		t.Error("empty version should always be eligible for update")
	}
}

func TestVersionEligible_NonSemver_AlwaysEligible(t *testing.T) {
	if !VersionEligible("nightly-abc123", "1.0.0") {
		t.Error("non-semver version should always be eligible for update")
	}
}

func TestVersionEligible_VPrefixedInput(t *testing.T) {
	tests := []struct {
		name    string
		current string
		target  string
		want    bool
	}{
		{"v-prefixed older", "v0.9.0", "v1.0.0", true},
		{"v-prefixed equal", "v1.0.0", "v1.0.0", false},
		{"v-prefixed current bare target", "v1.1.0", "1.0.0", false},
		{"bare current v-prefixed target", "0.9.0", "v1.0.0", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := VersionEligible(tt.current, tt.target)
			if got != tt.want {
				t.Errorf("VersionEligible(%q, %q) = %v, want %v", tt.current, tt.target, got, tt.want)
			}
		})
	}
}

func TestVersionEligible_PatchVersions(t *testing.T) {
	if !VersionEligible("1.0.0", "1.0.1") {
		t.Error("1.0.0 should be eligible for update to 1.0.1")
	}
	if VersionEligible("1.0.1", "1.0.0") {
		t.Error("1.0.1 should NOT be eligible for update to 1.0.0")
	}
}

func TestVersionEligible_MajorVersionJump(t *testing.T) {
	if !VersionEligible("1.9.9", "2.0.0") {
		t.Error("1.9.9 should be eligible for update to 2.0.0")
	}
}

func TestVersionEligible_PreReleaseVersion(t *testing.T) {
	// Pre-release versions are less than the release version per semver spec
	if !VersionEligible("1.0.0-beta.1", "1.0.0") {
		t.Error("1.0.0-beta.1 should be eligible for update to 1.0.0")
	}
}

func TestVersionEligible_InvalidTarget_ReturnsFalse(t *testing.T) {
	// If target is not valid semver, we can't determine eligibility — be conservative
	if VersionEligible("1.0.0", "not-semver") {
		t.Error("valid current with invalid target should return false")
	}
}

func TestEnsureVPrefix_BareVersion(t *testing.T) {
	got := ensureVPrefix("1.2.3")
	if got != "v1.2.3" {
		t.Errorf("ensureVPrefix(\"1.2.3\") = %q, want \"v1.2.3\"", got)
	}
}

func TestEnsureVPrefix_AlreadyPrefixed(t *testing.T) {
	got := ensureVPrefix("v1.2.3")
	if got != "v1.2.3" {
		t.Errorf("ensureVPrefix(\"v1.2.3\") = %q, want \"v1.2.3\"", got)
	}
}

func TestEnsureVPrefix_EmptyString(t *testing.T) {
	got := ensureVPrefix("")
	if got != "v" {
		t.Errorf("ensureVPrefix(\"\") = %q, want \"v\"", got)
	}
}
