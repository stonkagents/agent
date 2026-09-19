package models

import (
	"strings"
	"testing"
)

func TestIsPlaceholderDisplayName(t *testing.T) {
	for _, ok := range []string{"swarm_operator_42", "quiet_courier_07", "neon_archivist_88"} {
		if !IsPlaceholderDisplayName(ok) {
			t.Errorf("%q should be a placeholder", ok)
		}
	}
	for _, no := range []string{"", "Atlas", "lalala_agent", "swarm_operator", "swarm_operator_4", "swarm_operator_123",
		"Swarm_operator_42", "swarm-operator-42", "swarm_operator_42 ", "a_b_c_42", "swarm_operator_4x"} {
		if IsPlaceholderDisplayName(no) {
			t.Errorf("%q should not be a placeholder", no)
		}
	}
}

func TestResolveDisplayName(t *testing.T) {
	cases := []struct{ stored, incoming, want string }{
		{"", "", ""},
		{"", "swarm_operator_42", "swarm_operator_42"}, // placeholder fills an empty slot
		{"", "Atlas", "Atlas"},
		{"lalala_agent", "swarm_operator_42", "lalala_agent"}, // placeholder never overwrites
		{"Atlas", "quiet_courier_07", "Atlas"},
		{"lalala_agent", "Atlas", "Atlas"}, // owner rename wins
		{"Atlas", "", "Atlas"},             // empty never clears
		{"swarm_operator_42", "", "swarm_operator_42"},
		{"swarm_operator_42", "neon_archivist_88", "swarm_operator_42"}, // placeholder over placeholder: keep
		{"swarm_operator_42", "Atlas", "Atlas"},
	}
	for _, c := range cases {
		if got := ResolveDisplayName(c.stored, c.incoming); got != c.want {
			t.Errorf("ResolveDisplayName(%q, %q) = %q, want %q", c.stored, c.incoming, got, c.want)
		}
	}
}

func TestAgentDisplayNameForSymbol(t *testing.T) {
	cases := map[string]string{
		"LALALA": "LALALA_Agent", "stnk2": "STNK2_Agent", " Knots ": "KNOTS_Agent", "nomad": "NOMAD_Agent", "": "", "  ": "",
		"<X>": "X_Agent", "A\x00B": "AB_Agent",
	}
	for in, want := range cases {
		if got := AgentDisplayNameForSymbol(in); got != want {
			t.Errorf("AgentDisplayNameForSymbol(%q) = %q, want %q", in, got, want)
		}
	}
	long := AgentDisplayNameForSymbol(strings.Repeat("é", 60))
	if n := len([]rune(long)); n > MaxDisplayNameRunes {
		t.Errorf("long symbol name has %d runes", n)
	}
}

func TestSanitizeDisplayName(t *testing.T) {
	if got := SanitizeDisplayName("  <b>Nova</b>\t "); got != "bNova/b" {
		t.Errorf("sanitize = %q", got)
	}
	if got := SanitizeDisplayName(strings.Repeat("é", 60)); len([]rune(got)) != MaxDisplayNameRunes {
		t.Errorf("rune cap = %d", len([]rune(got)))
	}
}
