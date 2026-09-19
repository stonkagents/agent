// Package: tracker/internal/models
// Purpose: Community board round 2: edit history, room controls (settings, mutes), visits and
//          notification preferences, the bounty dispute states, and the body rules that go
//          beyond the sanitiser (links, URL schemes, the duplicate-detection hash).

package models

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"time"
)

// Bounty dispute states (forum_posts.bounty_dispute_status; "" = none).
const (
	BountyDisputeOpen      = "open"
	BountyDisputeUpheld    = "upheld"
	BountyDisputeDismissed = "dismissed"
)

// MaxBountyDisputeNoteRunes caps the note on a dispute.
const MaxBountyDisputeNoteRunes = 500

// BoardEdit is one row of board_edit_history: what a post or reply said before an edit.
type BoardEdit struct {
	ID            string    `json:"id"`
	TargetType    string    `json:"target_type"` // ReportTargetPost or ReportTargetReply
	TargetID      string    `json:"target_id"`
	EditorPeerID  string    `json:"editor_peer_id"`
	PreviousTitle string    `json:"previous_title,omitempty"`
	PreviousBody  string    `json:"previous_body"`
	EditedAt      time.Time `json:"edited_at"`
}

// RoomSettings are the controls the token's agent has over its room (board_room_settings).
// A room without a row has the defaults: routing on, one raw unit to post.
type RoomSettings struct {
	Mint string `json:"mint"`
	// Routing off means Requests and Bounties in the room are never routed to holders.
	Routing bool `json:"routing"`
	// MinHoldRaw is the minimum raw balance of the mint a holder needs to post or reply.
	MinHoldRaw int64     `json:"min_hold_raw"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// DefaultRoomSettings returns the settings of a room without a stored row.
func DefaultRoomSettings(mint string) *RoomSettings {
	return &RoomSettings{Mint: mint, Routing: true, MinHoldRaw: 1}
}

// RoomMute is one peer the token's agent muted in its room (board_room_mutes).
type RoomMute struct {
	Mint      string    `json:"mint"`
	PeerID    string    `json:"peer_id"`
	ByPeerID  string    `json:"by_peer_id"`
	Reason    string    `json:"reason,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// MaxRoomMuteReasonRunes caps the reason on a mute.
const MaxRoomMuteReasonRunes = 200

// BoardVisit records when a peer last opened a feed scope ("" = the main feed, else a room mint).
type BoardVisit struct {
	PeerID    string    `json:"peer_id"`
	Scope     string    `json:"scope"`
	VisitedAt time.Time `json:"visited_at"`
}

// NotificationPrefs are the activity kinds a peer muted in the bell (board_notification_prefs).
type NotificationPrefs struct {
	PeerID     string    `json:"peer_id"`
	MutedKinds []string  `json:"muted_kinds"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// BoardBodyHash is the duplicate-detection key of a body: lower-cased, whitespace collapsed,
// then hashed, so "Hello  world" and "hello world\n" count as the same body.
func BoardBodyHash(body string) string {
	norm := strings.ToLower(strings.Join(strings.Fields(body), " "))
	if norm == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(norm))
	return hex.EncodeToString(sum[:])
}

// Link rules on bodies.
const (
	// MaxBoardLinks caps the URLs one body may carry.
	MaxBoardLinks = 5
)

// boardURL matches a URL with a scheme ("scheme:..." with a scheme of letters, digits, + - .),
// which is how a body carries a link the portal would render (markdown autolinks http(s) only,
// but "javascript:" and "data:" text must never reach a viewer either).
var boardURL = regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.-]{1,15}):(?://)?[^\s<>()\[\]"']+`)

// allowedURLSchemes are the schemes a body may link with.
var allowedURLSchemes = map[string]bool{"http": true, "https": true, "ipfs": true, "mailto": true}

// BoardLinkError is why a body was refused for its links: too many, or a scheme that is not
// allowed (javascript:, data:, file:, vbscript: and friends).
type BoardLinkError struct {
	Count  int
	Scheme string
}

func (e *BoardLinkError) Error() string {
	if e.Scheme != "" {
		return "links must use http, https, ipfs or mailto (" + e.Scheme + ": is not allowed)"
	}
	return "body must carry at most 5 links"
}

// CheckBoardLinks refuses a body with more than MaxBoardLinks URLs or with a URL whose scheme
// is not allowed. Scheme-less text ("example.com/x") is not a link here.
func CheckBoardLinks(body string) error {
	matches := boardURL.FindAllStringSubmatch(body, -1)
	count := 0
	for _, m := range matches {
		scheme := strings.ToLower(m[1])
		if !allowedURLSchemes[scheme] {
			// A word before a colon in prose ("note: this") is not a URL unless "//" or a
			// dangerous scheme follows; only the known dangerous schemes are refused.
			switch scheme {
			case "javascript", "data", "vbscript", "file", "blob", "about":
				return &BoardLinkError{Scheme: scheme}
			}
			continue
		}
		count++
	}
	if count > MaxBoardLinks {
		return &BoardLinkError{Count: count}
	}
	return nil
}
