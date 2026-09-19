package config

import (
	"regexp"
	"testing"
)

func TestDefaultDisplayName_ShapeAndDeterminism(t *testing.T) {
	shape := regexp.MustCompile(`^[a-z]+_[a-z]+_[0-9]{2}$`)
	const peer = "12D3KooWQYhTNQdmr3ArTeUHRYzFg94BKyTkoWBDWez9kSCVe2Xo"
	first := DefaultDisplayName(peer)
	if !shape.MatchString(first) {
		t.Fatalf("name %q does not match <adjective>_<noun>_<2 digits>", first)
	}
	if !IsPlaceholderDisplayName(first) {
		t.Errorf("generated name %q not recognised as placeholder", first)
	}
	for i := 0; i < 5; i++ {
		if got := DefaultDisplayName(peer); got != first {
			t.Fatalf("not deterministic: %q then %q", first, got)
		}
	}
	if other := DefaultDisplayName(peer + "x"); other == first {
		t.Errorf("different peer ids produced the same name %q", first)
	}
	// Every combination stays within the cap the tracker and identity endpoint enforce.
	for _, a := range displayNameAdjectives {
		for _, n := range displayNameNouns {
			if name := a + "_" + n + "_00"; len([]rune(name)) > 50 || !shape.MatchString(name) {
				t.Errorf("bad word combination %q", name)
			}
		}
	}
	// No peer id: still a valid placeholder (random).
	if got := DefaultDisplayName(""); !shape.MatchString(got) {
		t.Errorf("random name %q malformed", got)
	}
}

func TestIsPlaceholderDisplayName(t *testing.T) {
	for _, ok := range []string{"swarm_operator_42", "quiet_courier_07", "neon_archivist_88"} {
		if !IsPlaceholderDisplayName(ok) {
			t.Errorf("%q should be a placeholder", ok)
		}
	}
	for _, no := range []string{"", "Atlas", "lalala_agent", "swarm_operator_4", "Swarm_operator_42", "swarm-operator-42"} {
		if IsPlaceholderDisplayName(no) {
			t.Errorf("%q should not be a placeholder", no)
		}
	}
}
