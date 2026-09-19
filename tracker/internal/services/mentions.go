// Package services: @display_name mentions in board posts and replies (phase 1).
// Purpose: Find the @tokens in a body and resolve each to a peer at write time. A token runs
//          from the @ to the first character outside [A-Za-z0-9_] (or the end of the body) and
//          must equal a display name in the peer table, case-insensitive: the same rule the
//          portal uses to render the link, so what is notified is what is linked.

package services

import (
	"context"
	"regexp"
	"strings"

	"github.com/stonkagents/agent/tracker/internal/repository"
)

// Mention limits.
const (
	// MaxMentionsPerBody caps how many peers one post or reply can mention (notify).
	MaxMentionsPerBody = 5
	// MinMentionRunes / MaxMentionRunes bound a mentionable display name ([A-Za-z0-9_]{3,50}).
	MinMentionRunes = 3
	MaxMentionRunes = 50
)

// mentionToken matches "@name" where name is the maximal [A-Za-z0-9_] run after an @ that is
// not glued to a preceding word character (so an email address is not a mention).
var mentionToken = regexp.MustCompile(`(?:^|[^A-Za-z0-9_])@([A-Za-z0-9_]+)`)

// MentionTokens returns the candidate names after "@" in body, in order, duplicates kept.
// Runs shorter than MinMentionRunes or longer than MaxMentionRunes are not candidates.
func MentionTokens(body string) []string {
	matches := mentionToken.FindAllStringSubmatch(body, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		if n := len(m[1]); n < MinMentionRunes || n > MaxMentionRunes {
			continue
		}
		out = append(out, m[1])
	}
	return out
}

// ResolveMentions maps the @tokens of body to peer ids: a token resolves when it equals a
// display name in peers, matched case-insensitively (no prefix matching: "@alice_bobs" is not
// "alice_bob"). The author is never mentioned, peers are unique, and at most
// MaxMentionsPerBody are returned, in order of first appearance. A nil peers repo or a lookup
// error yields no mentions.
func ResolveMentions(ctx context.Context, peers repository.PeerRepository, body, authorPeerID string) []string {
	if peers == nil {
		return nil
	}
	tokens := MentionTokens(body)
	if len(tokens) == 0 {
		return nil
	}
	candidates := make([]string, 0, len(tokens))
	seen := make(map[string]struct{}, len(tokens))
	for _, tok := range tokens {
		lower := strings.ToLower(tok)
		if _, dup := seen[lower]; dup {
			continue
		}
		seen[lower] = struct{}{}
		candidates = append(candidates, lower)
	}
	found, err := peers.PeerIDsByDisplayNames(ctx, candidates)
	if err != nil || len(found) == 0 {
		return nil
	}
	var out []string
	picked := make(map[string]struct{})
	for _, tok := range tokens {
		peerID, ok := found[strings.ToLower(tok)]
		if !ok || peerID == authorPeerID {
			continue
		}
		if _, dup := picked[peerID]; dup {
			continue
		}
		picked[peerID] = struct{}{}
		out = append(out, peerID)
		if len(out) >= MaxMentionsPerBody {
			break
		}
	}
	return out
}
