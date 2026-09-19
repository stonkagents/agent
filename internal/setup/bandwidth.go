package setup

import "fmt"

// Default caps applied by POST /api/v1/setup/bandwidth when the body carries
// no values (the portal's fix button sends none).
const (
	DefaultUploadMbps   = 10.0
	DefaultDownloadMbps = 50.0
)

// Bounds for a cap sent to POST /api/v1/setup/bandwidth. Below MinCapMbps a
// chunk of a few hundred KB takes seconds and the agent is effectively
// unusable; above MaxCapMbps the value is meaningless for a home link.
const (
	MinCapMbps = 0.5
	MaxCapMbps = 10_000.0
)

// ValidateCapMbps reports whether mbps lies within [MinCapMbps, MaxCapMbps].
func ValidateCapMbps(mbps float64) error {
	if mbps != mbps || mbps < MinCapMbps || mbps > MaxCapMbps { // NaN fails both comparisons
		return fmt.Errorf("must be between %g and %g Mbps", MinCapMbps, MaxCapMbps)
	}
	return nil
}

// MbpsToBytesPerSecond converts a megabit-per-second cap to bytes/second.
func MbpsToBytesPerSecond(mbps float64) int64 {
	if mbps <= 0 {
		return 0
	}
	return int64(mbps * 1_000_000 / 8)
}

// BandwidthCheck is ok when both caps are configured (> 0). Detail carries
// the current caps in Mbps, or null when unset.
func BandwidthCheck(uploadMbps, downloadMbps float64) Check {
	detail := map[string]any{"uploadMbps": nil, "downloadMbps": nil}
	if uploadMbps > 0 {
		detail["uploadMbps"] = uploadMbps
	}
	if downloadMbps > 0 {
		detail["downloadMbps"] = downloadMbps
	}
	switch {
	case uploadMbps > 0 && downloadMbps > 0:
		return OK(IDBandwidth, detail)
	case uploadMbps > 0:
		return Missing(IDBandwidth, "no download cap configured", detail)
	case downloadMbps > 0:
		return Missing(IDBandwidth, "no upload cap configured", detail)
	default:
		return Missing(IDBandwidth, "no bandwidth caps configured", detail)
	}
}
