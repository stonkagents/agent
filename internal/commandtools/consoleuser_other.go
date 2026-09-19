//go:build !windows

package commandtools

import "errors"

// ConsoleUser only exists on Windows; the scheduled task is a Windows thing.
func ConsoleUser() (sid, name string, err error) {
	return "", "", errors.New("console user lookup is Windows only")
}
