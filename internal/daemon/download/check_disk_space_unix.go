//go:build !windows
// +build !windows

package download

import (
	"fmt"
	"syscall"
)

// checkDiskSpace validates there's enough disk space before download/assembly
// Returns descriptive error if disk is full or space is insufficient
func checkDiskSpace(path string, requiredBytes int64) error {
	// Get filesystem stats
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return fmt.Errorf("disk space check failed: cannot get filesystem stats: %w", err)
	}

	// Calculate available space (blocks * block size)
	availableBytes := int64(stat.Bavail) * int64(stat.Bsize)

	// Add 10% buffer for safety (prevent filling disk completely)
	requiredWithBuffer := int64(float64(requiredBytes) * 1.1)

	if availableBytes < requiredWithBuffer {
		return fmt.Errorf("insufficient disk space: required %d bytes (with 10%% buffer), available %d bytes", requiredWithBuffer, availableBytes)
	}

	return nil
}
