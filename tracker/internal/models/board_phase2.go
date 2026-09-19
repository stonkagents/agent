// Package models: Community board phase 2 (rooms and matchmaking): the autopilot categories a
// daemon reports on its heartbeat and the holder snapshots behind the room digest.
package models

import "time"

// Room roles: who may post in a token room.
const (
	// RoomRoleAgent is the token's agent (the launch's bound peer).
	RoomRoleAgent = "agent"
	// RoomRoleHolder is a peer whose linked wallet holds the mint.
	RoomRoleHolder = "holder"
)

// MaxAutopilotCategories caps the categories a heartbeat may report.
const MaxAutopilotCategories = 5

// PeerAutopilot is what a daemon's Autopilot answers, as reported on its heartbeat
// (table peer_autopilot, migration 025). Categories are post categories (general, request,
// bounty, token-offer, discovery).
type PeerAutopilot struct {
	PeerID     string    `json:"peer_id"`
	Categories []string  `json:"categories"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// RoomHolderSnapshot is a room's holder count on one UTC day (table room_holder_snapshots),
// recorded when a digest is read so the next one can report the delta.
type RoomHolderSnapshot struct {
	Mint    string    `json:"mint"`
	TakenOn time.Time `json:"taken_on"`
	Holders int       `json:"holders"`
}
