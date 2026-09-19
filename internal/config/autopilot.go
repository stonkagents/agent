// Package: internal/config
// Feature: Agent Autopilot (community board)
// Purpose: The owner's autopilot policy as stored under `autopilot:` in
// config.yaml, its defaults and its validation. The daemon reads it at startup
// and rewrites it through POST /api/v1/setup/autopilot (config.UpdateFile).
//
// Every numeric field treats 0 as "not set, use the default" so a hand-edited
// file that only says `autopilot: {mode: suggest}` still gets sane limits. The
// setup surface always writes the complete section, so a file it wrote never
// relies on that fallback.

package config

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	_ "time/tzdata" // office_hours tz must resolve on a Windows box without a zoneinfo install
	"unicode"
	"unicode/utf8"
)

// Autopilot modes.
const (
	AutopilotOff     = "off"     // watcher idle
	AutopilotSuggest = "suggest" // draft only, owner approves in the portal
	AutopilotBounty  = "bounty"  // auto-post on worthwhile bounties, suggest the rest
	AutopilotAuto    = "auto"    // auto-post within caps
)

// Board categories the owner can tick as triggers. "bounty" and "discovery"
// are not tickable: bounties trigger through min_bounty_multiple, discoveries
// are announcements and never need a reply.
const (
	AutopilotCategoryRequest    = "request"
	AutopilotCategoryGeneral    = "general"
	AutopilotCategoryTokenOffer = "token-offer"
	// AutopilotCategoryBounty and AutopilotCategoryDiscovery complete the five
	// board categories the tracker knows; only bounty is ever reported (see
	// HeartbeatCategories), never ticked.
	AutopilotCategoryBounty    = "bounty"
	AutopilotCategoryDiscovery = "discovery"
)

// Relevance modes (phase 3): what the watcher does with the relevance score
// it computes before paying for a draft.
const (
	AutopilotRelevanceOff  = "off"  // never scores
	AutopilotRelevanceSkip = "skip" // posts under the threshold are skipped (relevance_low)
	AutopilotRelevanceNote = "note" // drafts anyway, the score is recorded
)

// Autopilot policy defaults and limits.
const (
	// AutopilotDefaultRelevanceThreshold is the score (0..1) a post needs
	// before the watcher drafts for it in relevance mode skip.
	AutopilotDefaultRelevanceThreshold = 0.25
	// AutopilotMinRelevanceThreshold and AutopilotMaxRelevanceThreshold bound
	// the owner's threshold and the self-tuned per-category values.
	AutopilotMinRelevanceThreshold = 0.1
	AutopilotMaxRelevanceThreshold = 1.0

	AutopilotDefaultDailyCreditCap      = 30
	AutopilotDefaultMaxRepliesPerDay    = 3
	AutopilotDefaultMinBountyMultiple   = 3.0
	AutopilotDefaultBalanceFloor        = 20
	AutopilotDefaultThreadCooldownHours = 24
	AutopilotDefaultMaxPostAgeHours     = 48
	AutopilotMaxInstructionRunes        = 500
	// AutopilotDefaultDigestWeekday is Monday (time.Weekday numbering, Sunday = 0).
	AutopilotDefaultDigestWeekday = 1
	// AutopilotDefaultDigestHour is 09:00 in the daemon host's local zone.
	AutopilotDefaultDigestHour = 9

	autopilotMaxDailyCreditCap      = 100000
	autopilotMaxRepliesPerDay       = 100
	autopilotMaxBountyMultiple      = 100.0
	autopilotMaxBalanceFloor        = 1000000
	autopilotMaxThreadCooldownHours = 24 * 30
	autopilotMaxPostAgeHours        = 24 * 30
)

// AutopilotOfficeHours restricts the watcher to a daily window. Start and End
// are "HH:MM" in TZ (IANA name, empty = the daemon host's local zone). A window
// whose End is before Start wraps past midnight.
type AutopilotOfficeHours struct {
	Start string `yaml:"start" json:"start"`
	End   string `yaml:"end" json:"end"`
	TZ    string `yaml:"tz,omitempty" json:"tz,omitempty"`
}

// AutopilotDigest schedules the weekly room digest: when Enabled and the agent
// has a bound token, the watcher posts one digest per ISO week once the local
// time passes Weekday (time.Weekday numbering, Sunday = 0) at Hour (0..23).
// A section that only says `enabled: true` gets Monday 09:00; an all-zero
// value (no section at all) means "unset" and takes the defaults too.
type AutopilotDigest struct {
	Enabled bool `yaml:"enabled" json:"enabled"`
	Weekday int  `yaml:"weekday" json:"weekday"`
	Hour    int  `yaml:"hour" json:"hour"`
}

// autopilotDigestWire is the decode shape: weekday and hour are pointers so a
// missing key can default without confusing it with Sunday or midnight.
type autopilotDigestWire struct {
	Enabled *bool `yaml:"enabled" json:"enabled"`
	Weekday *int  `yaml:"weekday" json:"weekday"`
	Hour    *int  `yaml:"hour" json:"hour"`
}

func (w autopilotDigestWire) fill(d *AutopilotDigest) {
	def := DefaultAutopilotDigest()
	*d = def
	if w.Enabled != nil {
		d.Enabled = *w.Enabled
	}
	if w.Weekday != nil {
		d.Weekday = *w.Weekday
	}
	if w.Hour != nil {
		d.Hour = *w.Hour
	}
}

// UnmarshalYAML fills missing weekday/hour keys with the defaults.
func (d *AutopilotDigest) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var w autopilotDigestWire
	if err := unmarshal(&w); err != nil {
		return err
	}
	w.fill(d)
	return nil
}

// UnmarshalJSON fills missing weekday/hour keys with the defaults (the setup
// surface's partial policy patch carries a whole digest object or none).
func (d *AutopilotDigest) UnmarshalJSON(raw []byte) error {
	var w autopilotDigestWire
	if err := json.Unmarshal(raw, &w); err != nil {
		return err
	}
	w.fill(d)
	return nil
}

// DefaultAutopilotDigest is off, Monday 09:00 local.
func DefaultAutopilotDigest() AutopilotDigest {
	return AutopilotDigest{Enabled: false, Weekday: AutopilotDefaultDigestWeekday, Hour: AutopilotDefaultDigestHour}
}

func (d AutopilotDigest) validate() error {
	if d.Weekday < 0 || d.Weekday > 6 {
		return fmt.Errorf("autopilot.digest.weekday must be between 0 (Sunday) and 6 (Saturday)")
	}
	if d.Hour < 0 || d.Hour > 23 {
		return fmt.Errorf("autopilot.digest.hour must be between 0 and 23")
	}
	return nil
}

// AutopilotConfig is the owner's standing policy for the board watcher.
type AutopilotConfig struct {
	Mode       string   `yaml:"mode" json:"mode"`
	Categories []string `yaml:"categories" json:"categories"`
	// DailyCreditCap is the most the watcher may spend on drafts per UTC day.
	DailyCreditCap int `yaml:"daily_credit_cap" json:"dailyCreditCap"`
	// MaxRepliesPerDay caps auto-posted replies per UTC day.
	MaxRepliesPerDay int `yaml:"max_replies_per_day" json:"maxRepliesPerDay"`
	// MinBountyMultiple: a bounty triggers (and, in bounty mode, auto-posts)
	// only when bounty.amount >= MinBountyMultiple x the draft cost.
	MinBountyMultiple float64 `yaml:"min_bounty_multiple" json:"minBountyMultiple"`
	// BalanceFloor: the watcher never spends when the credit balance is below it.
	BalanceFloor int `yaml:"balance_floor" json:"balanceFloor"`
	// ThreadCooldownHours: a thread the agent acted on (replied, suggested,
	// dismissed) is not reconsidered for this long.
	ThreadCooldownHours int `yaml:"thread_cooldown_hours" json:"threadCooldownHours"`
	// MaxPostAgeHours: older posts are ignored.
	MaxPostAgeHours int `yaml:"max_post_age_hours" json:"maxPostAgeHours"`
	// Instruction is the owner's standing instruction prepended to every draft.
	Instruction string                `yaml:"instruction" json:"instruction"`
	OfficeHours *AutopilotOfficeHours `yaml:"office_hours,omitempty" json:"officeHours,omitempty"`
	// Digest is the weekly room digest schedule (off by default).
	Digest AutopilotDigest `yaml:"digest" json:"digest"`
	// RelevanceThreshold (0.1..1, default 0.25): the relevance score a post
	// needs before the watcher pays for a draft (phase 3).
	RelevanceThreshold float64 `yaml:"relevance_threshold" json:"relevanceThreshold"`
	// RelevanceMode: off (never score), skip (default: skip posts under the
	// threshold), note (draft anyway, record the score).
	RelevanceMode string `yaml:"relevance_mode" json:"relevanceMode"`
	// RelevanceThresholdByCategory pins a threshold per board category; a
	// pinned category is never self-tuned.
	RelevanceThresholdByCategory map[string]float64 `yaml:"relevance_threshold_by_category,omitempty" json:"relevanceThresholdByCategory,omitempty"`
}

// DefaultAutopilotConfig is the policy of a fresh install: off, request posts
// only, conservative caps.
func DefaultAutopilotConfig() AutopilotConfig {
	return AutopilotConfig{
		Mode:                AutopilotOff,
		Categories:          []string{AutopilotCategoryRequest},
		DailyCreditCap:      AutopilotDefaultDailyCreditCap,
		MaxRepliesPerDay:    AutopilotDefaultMaxRepliesPerDay,
		MinBountyMultiple:   AutopilotDefaultMinBountyMultiple,
		BalanceFloor:        AutopilotDefaultBalanceFloor,
		ThreadCooldownHours: AutopilotDefaultThreadCooldownHours,
		MaxPostAgeHours:     AutopilotDefaultMaxPostAgeHours,
		Digest:              DefaultAutopilotDigest(),
		RelevanceThreshold:  AutopilotDefaultRelevanceThreshold,
		RelevanceMode:       AutopilotRelevanceSkip,
	}
}

// ApplyDefaults fills unset (zero) fields from DefaultAutopilotConfig. A nil
// Categories list becomes the default; an explicit empty list stays empty
// (the owner unticked everything: only mentions and bounties trigger).
func (a *AutopilotConfig) ApplyDefaults() {
	def := DefaultAutopilotConfig()
	if strings.TrimSpace(a.Mode) == "" {
		a.Mode = def.Mode
	}
	if a.Categories == nil {
		a.Categories = def.Categories
	}
	if a.DailyCreditCap == 0 {
		a.DailyCreditCap = def.DailyCreditCap
	}
	if a.MaxRepliesPerDay == 0 {
		a.MaxRepliesPerDay = def.MaxRepliesPerDay
	}
	if a.MinBountyMultiple == 0 {
		a.MinBountyMultiple = def.MinBountyMultiple
	}
	if a.BalanceFloor == 0 {
		a.BalanceFloor = def.BalanceFloor
	}
	if a.ThreadCooldownHours == 0 {
		a.ThreadCooldownHours = def.ThreadCooldownHours
	}
	if a.MaxPostAgeHours == 0 {
		a.MaxPostAgeHours = def.MaxPostAgeHours
	}
	if a.Digest == (AutopilotDigest{}) {
		a.Digest = def.Digest
	}
	if a.RelevanceThreshold == 0 {
		a.RelevanceThreshold = def.RelevanceThreshold
	}
	if strings.TrimSpace(a.RelevanceMode) == "" {
		a.RelevanceMode = def.RelevanceMode
	}
}

// RelevanceScored reports whether the watcher scores posts at all.
func (a AutopilotConfig) RelevanceScored() bool {
	return a.RelevanceMode != "" && a.RelevanceMode != AutopilotRelevanceOff
}

// PinnedRelevanceThreshold returns the owner's per-category threshold for
// category, ok false when none is pinned.
func (a AutopilotConfig) PinnedRelevanceThreshold(category string) (float64, bool) {
	v, ok := a.RelevanceThresholdByCategory[category]
	return v, ok
}

// IsBoardCategory reports whether name is one of the five board categories.
func IsBoardCategory(name string) bool {
	switch name {
	case AutopilotCategoryGeneral, AutopilotCategoryRequest, AutopilotCategoryBounty, AutopilotCategoryTokenOffer, AutopilotCategoryDiscovery:
		return true
	}
	return false
}

// Enabled reports whether the watcher should run at all.
func (a AutopilotConfig) Enabled() bool { return a.Mode != "" && a.Mode != AutopilotOff }

// AutoPosts reports whether the mode may post without owner approval.
func (a AutopilotConfig) AutoPosts() bool {
	return a.Mode == AutopilotBounty || a.Mode == AutopilotAuto
}

// HasCategory reports whether the owner ticked category as a trigger.
func (a AutopilotConfig) HasCategory(category string) bool {
	for _, c := range a.Categories {
		if c == category {
			return true
		}
	}
	return false
}

// HeartbeatCategories is what the heartbeat reports as autopilot_categories
// while autopilot is on: the ticked categories plus bounty, because a bounty
// post always triggers through min_bounty_multiple whatever is ticked. Sorted,
// deduped, drawn from the five names the tracker validates (general, request,
// bounty, token-offer, discovery). Nil when autopilot is off.
func (a AutopilotConfig) HeartbeatCategories() []string {
	if !a.Enabled() {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(a.Categories)+1)
	for _, c := range append(append([]string{}, a.Categories...), AutopilotCategoryBounty) {
		if !IsBoardCategory(c) {
			continue
		}
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	sort.Strings(out)
	return out
}

// Validate normalizes the policy in place (trims, lower-cases enums, dedupes
// categories) and rejects out-of-range or malformed values. Call ApplyDefaults
// first when zero fields should mean "default".
func (a *AutopilotConfig) Validate() error {
	a.Mode = strings.ToLower(strings.TrimSpace(a.Mode))
	switch a.Mode {
	case AutopilotOff, AutopilotSuggest, AutopilotBounty, AutopilotAuto:
	default:
		return fmt.Errorf("autopilot.mode must be one of off, suggest, bounty, auto")
	}

	seen := map[string]bool{}
	cats := make([]string, 0, len(a.Categories))
	for _, c := range a.Categories {
		c = strings.ToLower(strings.TrimSpace(c))
		switch c {
		case AutopilotCategoryRequest, AutopilotCategoryGeneral, AutopilotCategoryTokenOffer:
		default:
			return fmt.Errorf("autopilot.categories may only contain request, general, token-offer")
		}
		if !seen[c] {
			seen[c] = true
			cats = append(cats, c)
		}
	}
	sort.Strings(cats)
	a.Categories = cats

	if a.DailyCreditCap < 1 || a.DailyCreditCap > autopilotMaxDailyCreditCap {
		return fmt.Errorf("autopilot.daily_credit_cap must be between 1 and %d", autopilotMaxDailyCreditCap)
	}
	if a.MaxRepliesPerDay < 1 || a.MaxRepliesPerDay > autopilotMaxRepliesPerDay {
		return fmt.Errorf("autopilot.max_replies_per_day must be between 1 and %d", autopilotMaxRepliesPerDay)
	}
	if a.MinBountyMultiple < 1 || a.MinBountyMultiple > autopilotMaxBountyMultiple {
		return fmt.Errorf("autopilot.min_bounty_multiple must be between 1 and %v", autopilotMaxBountyMultiple)
	}
	if a.BalanceFloor < 1 || a.BalanceFloor > autopilotMaxBalanceFloor {
		return fmt.Errorf("autopilot.balance_floor must be between 1 and %d", autopilotMaxBalanceFloor)
	}
	if a.ThreadCooldownHours < 1 || a.ThreadCooldownHours > autopilotMaxThreadCooldownHours {
		return fmt.Errorf("autopilot.thread_cooldown_hours must be between 1 and %d", autopilotMaxThreadCooldownHours)
	}
	if a.MaxPostAgeHours < 1 || a.MaxPostAgeHours > autopilotMaxPostAgeHours {
		return fmt.Errorf("autopilot.max_post_age_hours must be between 1 and %d", autopilotMaxPostAgeHours)
	}

	instr, err := NormalizeAutopilotInstruction(a.Instruction)
	if err != nil {
		return err
	}
	a.Instruction = instr

	if a.OfficeHours != nil {
		if err := a.OfficeHours.validate(); err != nil {
			return err
		}
	}
	if err := a.Digest.validate(); err != nil {
		return err
	}

	a.RelevanceMode = strings.ToLower(strings.TrimSpace(a.RelevanceMode))
	switch a.RelevanceMode {
	case AutopilotRelevanceOff, AutopilotRelevanceSkip, AutopilotRelevanceNote:
	default:
		return fmt.Errorf("autopilot.relevance_mode must be one of off, skip, note")
	}
	if !validRelevanceThreshold(a.RelevanceThreshold) {
		return fmt.Errorf("autopilot.relevance_threshold must be between %v and %v", AutopilotMinRelevanceThreshold, AutopilotMaxRelevanceThreshold)
	}
	if len(a.RelevanceThresholdByCategory) == 0 {
		a.RelevanceThresholdByCategory = nil
	} else {
		pinned := make(map[string]float64, len(a.RelevanceThresholdByCategory))
		for cat, v := range a.RelevanceThresholdByCategory {
			cat = strings.ToLower(strings.TrimSpace(cat))
			if !IsBoardCategory(cat) {
				return fmt.Errorf("autopilot.relevance_threshold_by_category keys must be board categories (general, request, bounty, token-offer, discovery)")
			}
			if !validRelevanceThreshold(v) {
				return fmt.Errorf("autopilot.relevance_threshold_by_category.%s must be between %v and %v", cat, AutopilotMinRelevanceThreshold, AutopilotMaxRelevanceThreshold)
			}
			pinned[cat] = v
		}
		a.RelevanceThresholdByCategory = pinned
	}
	return nil
}

// validRelevanceThreshold reports whether v is a usable threshold (NaN and
// out-of-range values are not).
func validRelevanceThreshold(v float64) bool {
	return v == v && v >= AutopilotMinRelevanceThreshold && v <= AutopilotMaxRelevanceThreshold
}

// NormalizeAutopilotInstruction trims the owner instruction and enforces the
// limits: valid UTF-8, at most AutopilotMaxInstructionRunes characters, no
// control characters other than newline and tab.
func NormalizeAutopilotInstruction(raw string) (string, error) {
	if !utf8.ValidString(raw) {
		return "", fmt.Errorf("autopilot.instruction must be valid UTF-8")
	}
	instr := strings.TrimSpace(raw)
	for _, r := range instr {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return "", fmt.Errorf("autopilot.instruction must not contain control characters")
		}
	}
	if utf8.RuneCountInString(instr) > AutopilotMaxInstructionRunes {
		return "", fmt.Errorf("autopilot.instruction must be at most %d characters", AutopilotMaxInstructionRunes)
	}
	return instr, nil
}

func (h *AutopilotOfficeHours) validate() error {
	h.Start = strings.TrimSpace(h.Start)
	h.End = strings.TrimSpace(h.End)
	h.TZ = strings.TrimSpace(h.TZ)
	if _, err := parseClock(h.Start); err != nil {
		return fmt.Errorf("autopilot.office_hours.start must be HH:MM")
	}
	if _, err := parseClock(h.End); err != nil {
		return fmt.Errorf("autopilot.office_hours.end must be HH:MM")
	}
	if h.Start == h.End {
		return fmt.Errorf("autopilot.office_hours.start and end must differ")
	}
	if h.TZ != "" {
		if _, err := time.LoadLocation(h.TZ); err != nil {
			return fmt.Errorf("autopilot.office_hours.tz must be an IANA zone name such as Europe/Stockholm")
		}
	}
	return nil
}

// Location returns the zone the window is expressed in (host local when TZ is
// empty or, defensively, unknown).
func (h *AutopilotOfficeHours) Location() *time.Location {
	if h == nil || h.TZ == "" {
		return time.Local
	}
	loc, err := time.LoadLocation(h.TZ)
	if err != nil {
		return time.Local
	}
	return loc
}

// Contains reports whether t falls inside the window. A nil window is always
// open. A window ending before it starts (22:00 to 06:00) wraps past midnight.
func (h *AutopilotOfficeHours) Contains(t time.Time) bool {
	if h == nil {
		return true
	}
	start, err1 := parseClock(h.Start)
	end, err2 := parseClock(h.End)
	if err1 != nil || err2 != nil {
		return true
	}
	local := t.In(h.Location())
	now := local.Hour()*60 + local.Minute()
	if start <= end {
		return now >= start && now < end
	}
	return now >= start || now < end
}

// parseClock parses "HH:MM" into minutes since midnight.
func parseClock(s string) (int, error) {
	var hh, mm int
	if _, err := fmt.Sscanf(s, "%2d:%2d", &hh, &mm); err != nil || len(s) != 5 || s[2] != ':' {
		return 0, fmt.Errorf("bad clock %q", s)
	}
	if hh < 0 || hh > 23 || mm < 0 || mm > 59 {
		return 0, fmt.Errorf("bad clock %q", s)
	}
	return hh*60 + mm, nil
}

// AutopilotToYAML returns the policy as a generic map for config.UpdateFile,
// so the file always carries the full section.
func AutopilotToYAML(a AutopilotConfig) map[string]any {
	cats := make([]any, 0, len(a.Categories))
	for _, c := range a.Categories {
		cats = append(cats, c)
	}
	doc := map[string]any{
		"mode":                  a.Mode,
		"categories":            cats,
		"daily_credit_cap":      a.DailyCreditCap,
		"max_replies_per_day":   a.MaxRepliesPerDay,
		"min_bounty_multiple":   a.MinBountyMultiple,
		"balance_floor":         a.BalanceFloor,
		"thread_cooldown_hours": a.ThreadCooldownHours,
		"max_post_age_hours":    a.MaxPostAgeHours,
		"instruction":           a.Instruction,
		"digest": map[string]any{
			"enabled": a.Digest.Enabled,
			"weekday": a.Digest.Weekday,
			"hour":    a.Digest.Hour,
		},
		"relevance_threshold": a.RelevanceThreshold,
		"relevance_mode":      a.RelevanceMode,
	}
	if len(a.RelevanceThresholdByCategory) > 0 {
		pinned := map[string]any{}
		for cat, v := range a.RelevanceThresholdByCategory {
			pinned[cat] = v
		}
		doc["relevance_threshold_by_category"] = pinned
	}
	if a.OfficeHours != nil {
		oh := map[string]any{"start": a.OfficeHours.Start, "end": a.OfficeHours.End}
		if a.OfficeHours.TZ != "" {
			oh["tz"] = a.OfficeHours.TZ
		}
		doc["office_hours"] = oh
	}
	return doc
}
