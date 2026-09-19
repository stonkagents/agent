package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/stonkagents/agent/internal/setup"
)

// TestEnsureFirewallRule verifies the post-install helper adds the
// program-scoped rule once and leaves an existing matching rule alone.
func TestEnsureFirewallRule(t *testing.T) {
	const installDir = `C:\Program Files\StonkAgents`
	exe := installDir + `\stonkagents-daemon.exe`
	present := "Rule Name:                            StonkAgents Agent\r\n" +
		"----------------------------------------------------------------------\r\n" +
		"Enabled:                              Yes\r\n" +
		"Direction:                            In\r\n" +
		"Program:                              " + exe + "\r\n" +
		"Action:                               Allow\r\n"

	added := false
	var calls []string
	run := func(name string, args ...string) ([]byte, error) {
		line := name + " " + strings.Join(args, " ")
		calls = append(calls, line)
		switch {
		case strings.HasPrefix(line, "netsh advfirewall firewall show rule"):
			if added {
				return []byte(present), nil
			}
			return []byte("No rules match the specified criteria."), errors.New("exit status 1")
		case strings.HasPrefix(line, "netsh advfirewall firewall add rule"):
			added = true
			return []byte("Ok."), nil
		}
		return nil, errors.New("unexpected: " + line)
	}

	if c := ensureFirewallRule(run, installDir); c.Status != setup.StatusOK {
		t.Fatalf("first run: %+v", c)
	}
	adds := 0
	for _, c := range calls {
		if strings.Contains(c, " add rule ") {
			adds++
			if !strings.Contains(c, "program="+exe) || !strings.Contains(c, "dir=in action=allow") || !strings.Contains(c, "enable=yes") {
				t.Errorf("add rule args = %q", c)
			}
		}
	}
	if adds != 1 {
		t.Errorf("add rule issued %d times, want 1", adds)
	}

	calls = nil
	if c := ensureFirewallRule(run, installDir); c.Status != setup.StatusOK {
		t.Fatalf("second run: %+v", c)
	}
	for _, c := range calls {
		if strings.Contains(c, " add rule ") {
			t.Errorf("second run re-added the rule: %q", c)
		}
	}
}
