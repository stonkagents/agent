// Package: tracker/internal/reputation
// Feature: F-032 (Peers & Reputation)
// Story: US-032-01 (Peer Data Foundation)
// Purpose: Derives rank tier from composite score with bootstrap rule

package reputation

// Rank tier thresholds (composite 0-1 scale).
const (
	RankOGThreshold     = 0.80
	RankGoldThreshold   = 0.60
	RankSilverThreshold = 0.40
	RankBronzeThreshold = 0.20
)

// Rank tier string constants.
const (
	RankOG     = "og"
	RankGold   = "gold"
	RankSilver = "silver"
	RankBronze = "bronze"
	RankNew    = "new"
)

// DeriveRank returns the tier name for a peer's composite score.
// Bootstrap rule: peers with zero transfer activity are always "new"
// regardless of default EigenTrust scores (~0.45 from security=1.0 + citizenship=1.0).
func DeriveRank(composite float64, totalUpload, totalDownload int64, sharedFiles int) string {
	if totalUpload == 0 && totalDownload == 0 && sharedFiles == 0 {
		return RankNew
	}
	switch {
	case composite >= RankOGThreshold:
		return RankOG
	case composite >= RankGoldThreshold:
		return RankGold
	case composite >= RankSilverThreshold:
		return RankSilver
	case composite >= RankBronzeThreshold:
		return RankBronze
	default:
		return RankNew
	}
}
