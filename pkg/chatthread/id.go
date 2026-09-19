// Package chatthread provides stable session/thread identifiers shared by daemon and tracker.
package chatthread

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// PersonalContextTokenAddress is the Postgres token_address value for user↔personal-agent chat
// (not a real token contract). Empty string was used earlier; new rows use this sentinel.
const PersonalContextTokenAddress = "0x"

// StableID returns a deterministic session id for a user + optional context (e.g. token contract).
// Empty context means personal agent chat. Format matches legacy daemon agent_chat ut- prefix.
func StableID(userID, contextID string) string {
	userID = strings.TrimSpace(userID)
	contextID = strings.TrimSpace(contextID)
	sum := sha256.Sum256([]byte(userID + "|" + contextID))
	return "ut-" + hex.EncodeToString(sum[:16])
}
