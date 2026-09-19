// Package: internal/daemon
// Feature: Agent Autopilot (community board)
// Purpose: The tracker DTOs the watcher reads and the pure decision gate
// (autopilotDecide). No I/O here, so the whole decision table in autopilot.go
// is unit-testable without a tracker.

package daemon

import (
	"math"
	"strings"
	"time"

	"github.com/stonkagents/agent/internal/config"
)

// --- tracker DTOs -------------------------------------------------------

// autopilotPost is the subset of the tracker's PortalPost the gate needs.
// content/body and time/createdAt are both accepted so a tracker rename does
// not silently stop the watcher.
type autopilotPost struct {
	ID                string           `json:"id"`
	Author            string           `json:"author"`
	AuthorDisplayName string           `json:"authorDisplayName"`
	Title             string           `json:"title"`
	Content           string           `json:"content"`
	Body              string           `json:"body"`
	Category          string           `json:"category"`
	Time              string           `json:"time"`
	CreatedAt         string           `json:"createdAt"`
	IsAuthor          bool             `json:"isAuthor"`
	Bounty            *autopilotBounty `json:"bounty"`
}

func (p autopilotPost) text() string {
	if p.Content != "" {
		return p.Content
	}
	return p.Body
}

func (p autopilotPost) created() (time.Time, bool) {
	for _, raw := range []string{p.Time, p.CreatedAt} {
		if raw == "" {
			continue
		}
		if t, err := time.Parse(time.RFC3339, raw); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func (p autopilotPost) authorName() string {
	if p.AuthorDisplayName != "" {
		return p.AuthorDisplayName
	}
	return p.Author
}

// autopilotReply is the subset of the tracker's PortalReply the gate needs.
type autopilotReply struct {
	ID     string `json:"id"`
	Author string `json:"author"`
}

// --- decision -----------------------------------------------------------

// autopilotAction is what the gate decided for one post.
type autopilotAction int

const (
	autopilotSkip autopilotAction = iota
	autopilotSuggestAction
	autopilotPostAction
	// autopilotAskAction: bounty hunter mode posts a short bounty ask instead
	// of a full answer (phase 3).
	autopilotAskAction
)

func (a autopilotAction) String() string {
	switch a {
	case autopilotSuggestAction:
		return "suggest"
	case autopilotPostAction:
		return "post"
	case autopilotAskAction:
		return "ask"
	}
	return "skip"
}

// autopilotCandidate is a post plus the local facts the gate consults.
type autopilotCandidate struct {
	post        autopilotPost
	replied     bool
	pending     bool
	seenAt      time.Time
	seen        bool
	peerID      string
	displayName string
	// asked: the agent already posted a bounty ask on the post (askAmount).
	// raised: a bounty_raised activity brought the post back; the ask gate
	// and the cooldown step aside and the bounty is measured against the ask.
	asked     bool
	askAmount int
	raised    bool
}

// autopilotDecision is the gate's verdict.
type autopilotDecision struct {
	action  autopilotAction
	reason  string // skip reason, or the trigger for suggest/post
	trigger string // category | bounty | mention
}

// bountyMeetsThreshold reports whether the post carries an open credit bounty
// worth at least min_bounty_multiple drafts.
func bountyMeetsThreshold(b *autopilotBounty, multiple float64) bool {
	if b == nil || b.Amount <= 0 {
		return false
	}
	if b.Status != "" && b.Status != "open" {
		return false
	}
	if c := strings.ToLower(b.Currency); c != "" && c != "credits" {
		return false
	}
	return float64(b.Amount) >= multiple*float64(autopilotDraftCost)
}

// mentionsAgent reports whether the post text addresses this agent by
// @displayName or by the leading characters of its peer id.
func mentionsAgent(text, displayName, peerID string) bool {
	lower := strings.ToLower(text)
	if name := strings.ToLower(strings.TrimSpace(displayName)); name != "" && strings.Contains(lower, "@"+name) {
		return true
	}
	if len(peerID) >= autopilotMentionPrefixLen && strings.Contains(text, peerID[:autopilotMentionPrefixLen]) {
		return true
	}
	return false
}

// autopilotDecide applies the free gate and the mode to one candidate. Pure:
// no I/O, so the whole table is unit-testable.
func autopilotDecide(policy config.AutopilotConfig, c autopilotCandidate, now time.Time) autopilotDecision {
	p := c.post
	skip := func(reason string) autopilotDecision { return autopilotDecision{action: autopilotSkip, reason: reason} }
	if p.IsAuthor || (c.peerID != "" && p.Author == c.peerID) {
		return skip("own_post")
	}
	if c.replied {
		return skip("already_replied")
	}
	if c.pending {
		return skip("suggestion_pending")
	}
	if c.asked && !c.raised {
		return skip("ask_pending")
	}
	if !c.raised && c.seen && now.Sub(c.seenAt) < time.Duration(policy.ThreadCooldownHours)*time.Hour {
		return skip("cooldown")
	}
	created, ok := p.created()
	if !ok {
		return skip("no_timestamp")
	}
	if !c.raised && now.Sub(created) > time.Duration(policy.MaxPostAgeHours)*time.Hour {
		return skip("too_old")
	}

	bountyOK := bountyMeetsThreshold(p.Bounty, policy.MinBountyMultiple)
	if c.raised {
		// The owner raised the bounty after our ask: it counts once it meets the ask.
		bountyOK = c.askAmount > 0 && bountyMeetsThreshold(p.Bounty, float64(c.askAmount)/float64(autopilotDraftCost))
		if !bountyOK {
			return skip("bounty_under_ask")
		}
	}
	trigger := ""
	switch {
	case c.raised:
		trigger = "bounty"
	case mentionsAgent(p.Title+"\n"+p.text(), c.displayName, c.peerID):
		trigger = "mention"
	case bountyOK:
		trigger = "bounty"
	case policy.HasCategory(p.Category):
		trigger = "category"
	default:
		return skip("no_trigger")
	}

	action := autopilotSuggestAction
	switch {
	case policy.Mode == config.AutopilotSuggest:
	case p.Category == config.AutopilotCategoryTokenOffer:
		// Token offers are never answered without the owner, whatever the mode.
	case policy.Mode == config.AutopilotAuto:
		action = autopilotPostAction
	case policy.Mode == config.AutopilotBounty && bountyOK:
		action = autopilotPostAction
	case policy.Mode == config.AutopilotBounty && askable(p):
		// No bounty, or one under the multiple: ask for one instead of answering.
		action = autopilotAskAction
	}
	return autopilotDecision{action: action, reason: trigger, trigger: trigger}
}

// askable reports whether a bounty ask may go on the post: the tracker
// accepts asks on Request and Bounty posts only.
func askable(p autopilotPost) bool {
	return p.Category == config.AutopilotCategoryRequest || p.Category == config.AutopilotCategoryBounty
}

// openCreditBounty returns the amount of the post's open credit bounty, 0
// when there is none (a completed, expired or non-credit bounty counts as none).
func openCreditBounty(b *autopilotBounty) int {
	if !bountyMeetsThreshold(b, 0) {
		return 0
	}
	return b.Amount
}

// autopilotAskAmount is the credits an ask requests: the larger of
// min_bounty_multiple x the draft cost and the current open bounty + 10,
// rounded up to a multiple of 10.
func autopilotAskAmount(policy config.AutopilotConfig, b *autopilotBounty) int {
	want := int(math.Ceil(policy.MinBountyMultiple * float64(autopilotDraftCost)))
	if current := openCreditBounty(b); current > 0 && current+10 > want {
		want = current + 10
	}
	if rem := want % 10; rem != 0 {
		want += 10 - rem
	}
	return want
}
