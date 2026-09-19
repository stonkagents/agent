//go:build darwin

// Package: internal/controller

package controller

import (
	"testing"
)

// mockLaunchctl replaces runLaunchctl and records every invocation.
func mockLaunchctl(t *testing.T) *[][]string {
	t.Helper()
	orig := runLaunchctl
	calls := &[][]string{}
	runLaunchctl = func(args ...string) ([]byte, error) {
		*calls = append(*calls, args)
		return nil, nil
	}
	t.Cleanup(func() { runLaunchctl = orig })
	return calls
}

func TestIsDaemonRunning_MatchesLabel(t *testing.T) {
	orig := runLaunchctl
	t.Cleanup(func() { runLaunchctl = orig })

	cases := []struct {
		name string
		out  string
		want bool
	}{
		{"new label", "123\t0\tcom.stonkagents.daemon\n", true},
		{"neither", "123\t0\tcom.apple.Finder\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runLaunchctl = func(args ...string) ([]byte, error) { return []byte(tc.out), nil }
			if got := IsDaemonRunning(); got != tc.want {
				t.Errorf("IsDaemonRunning() = %v, want %v", got, tc.want)
			}
		})
	}
}
