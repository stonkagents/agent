//go:build windows
// +build windows

package download

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// checkDiskSpace validates there's enough disk space before download/assembly
// Returns descriptive error if disk is full or space is insufficient
func checkDiskSpace(path string, requiredBytes int64) error {
	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return fmt.Errorf("disk space check failed: invalid path: %w", err)
	}

	var freeBytesAvailable uint64
	if err := windows.GetDiskFreeSpaceEx(pathPtr, &freeBytesAvailable, nil, nil); err != nil {
		return fmt.Errorf("disk space check failed: cannot get filesystem stats: %w", err)
	}

	// Add 10% buffer for safety (prevent filling disk completely)
	requiredWithBuffer := int64(float64(requiredBytes) * 1.1)
	availableBytes := int64(freeBytesAvailable)

	if availableBytes < requiredWithBuffer {
		return fmt.Errorf("insufficient disk space: required %d bytes (with 10%% buffer), available %d bytes", requiredWithBuffer, availableBytes)
	}

	return nil
}
