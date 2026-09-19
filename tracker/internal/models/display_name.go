// Package: tracker/internal/models
// Purpose: Display name rules shared by registration, heartbeat and launch claim.
//
// Every agent has a display name. The daemon generates a placeholder
// (<adjective>_<noun>_<2 digits>, e.g. swarm_operator_42) when it has none; the
// tracker replaces a placeholder with <SYMBOL>_Agent when a launch is bound to the
// peer; the owner can rename from the portal. A placeholder sent later by the
// daemon never overwrites a name the tracker or the owner already set.

package models

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxDisplayNameRunes caps a display name in characters (runes). The daemon
// enforces the same limit before sending.
const MaxDisplayNameRunes = 50

// placeholderDisplayName matches the daemon-generated default name.
var placeholderDisplayName = regexp.MustCompile(`^[a-z]+_[a-z]+_[0-9]{2}$`)

// SanitizeDisplayName strips HTML angle brackets and control characters, trims
// whitespace, and truncates to MaxDisplayNameRunes (runes, so a multibyte name is
// never cut mid-character). Returns "" if nothing is left.
func SanitizeDisplayName(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '<' || r == '>' || unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > MaxDisplayNameRunes {
		runes := []rune(s)
		s = strings.TrimSpace(string(runes[:MaxDisplayNameRunes]))
	}
	return s
}

// IsPlaceholderDisplayName reports whether name is a daemon-generated default.
func IsPlaceholderDisplayName(name string) bool {
	return placeholderDisplayName.MatchString(name)
}

// ResolveDisplayName applies the overwrite rule for a name reported by the daemon
// (registration or heartbeat): the incoming name replaces the stored one only when
// it is non-empty and not a placeholder, or when nothing is stored yet.
func ResolveDisplayName(stored, incoming string) string {
	if incoming == "" {
		return stored
	}
	if stored == "" || !IsPlaceholderDisplayName(incoming) {
		return incoming
	}
	return stored
}

// AgentDisplayNameForSymbol is the name a bound agent gets from its token when the
// peer has no name or still carries a placeholder: "<SYMBOL>_Agent (symbol upper-cased)".
func AgentDisplayNameForSymbol(symbol string) string {
	base := SanitizeDisplayName(strings.ToUpper(strings.TrimSpace(symbol)))
	if base == "" {
		return ""
	}
	return SanitizeDisplayName(base + "_Agent")
}
