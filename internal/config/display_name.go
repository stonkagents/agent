// Package: internal/config
// Purpose: Default agent display name. Every agent has a name from the start: when
//          config.yaml has none the daemon generates "<adjective>_<noun>_<2 digits>"
//          (the style of the portal placeholder "swarm_operator_42"), seeded from the
//          peer id so a reinstall with the same key yields the same name. The tracker
//          recognises this shape as a placeholder (tracker/internal/models) and
//          replaces it with "<symbol>_agent" once a token is bound.

package config

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"regexp"
)

// placeholderDisplayName is the shape of a generated default name.
var placeholderDisplayName = regexp.MustCompile(`^[a-z]+_[a-z]+_[0-9]{2}$`)

var displayNameAdjectives = []string{
	"amber", "bold", "brisk", "calm", "clever", "cosmic", "crisp", "dusk", "eager", "ember",
	"fleet", "frost", "gentle", "gilded", "hollow", "humble", "iron", "ivory", "jade", "keen",
	"lucid", "lunar", "mellow", "misty", "neon", "nimble", "noble", "onyx", "pale", "plucky",
	"quiet", "rapid", "rusty", "silent", "solar", "steady", "swarm", "swift", "tidy", "vivid",
	"wild", "witty", "zesty",
}

var displayNameNouns = []string{
	"archivist", "beacon", "courier", "curator", "drifter", "envoy", "forager", "gardener",
	"harbor", "herald", "keeper", "lantern", "mariner", "nomad", "operator", "oracle", "pilot",
	"pioneer", "ranger", "relay", "scout", "seeder", "sentinel", "signal", "steward", "tinker",
	"voyager", "warden", "weaver", "whisper",
}

// DefaultDisplayName returns the placeholder name for peerID, always the same for the
// same id. With an empty id (identity not known yet) the name is random.
func DefaultDisplayName(peerID string) string {
	var seed [10]byte
	if peerID == "" {
		_, _ = rand.Read(seed[:])
	} else {
		sum := sha256.Sum256([]byte("stonkagents-display-name:" + peerID))
		copy(seed[:], sum[:])
	}
	adj := displayNameAdjectives[binary.BigEndian.Uint32(seed[0:4])%uint32(len(displayNameAdjectives))]
	noun := displayNameNouns[binary.BigEndian.Uint32(seed[4:8])%uint32(len(displayNameNouns))]
	num := binary.BigEndian.Uint16(seed[8:10]) % 100
	return fmt.Sprintf("%s_%s_%02d", adj, noun, num)
}

// IsPlaceholderDisplayName reports whether name has the generated default shape.
func IsPlaceholderDisplayName(name string) bool {
	return placeholderDisplayName.MatchString(name)
}
