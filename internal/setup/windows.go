package setup

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/stonkagents/agent/internal/installenv"
)

// FirewallRuleName is the name of the program-scoped inbound allow rule the
// installer, the controller and the setup surface all manage. It is per
// environment (STONKAGENTS_ENV: "StonkAgents Agent" for production,
// "StonkAgents Dev Agent" for dev) so side-by-side installs keep separate rules.
func FirewallRuleName() string { return installenv.Current().FirewallRule }

// DaemonServiceName is the daemon's SCM service name for the current
// environment (see installer/helper/main.go and internal/installenv).
func DaemonServiceName() string { return installenv.Current().DaemonService }

// ControllerServiceName is the controller's SCM service name for the current
// environment.
func ControllerServiceName() string { return installenv.Current().ControllerService }

// CommandRunner executes a program and returns its combined output. It is the
// seam that lets the netsh/sc logic be unit-tested on any OS.
type CommandRunner func(name string, args ...string) ([]byte, error)

// FirewallRule is one rule parsed from `netsh advfirewall firewall show rule ... verbose`.
type FirewallRule struct {
	Name    string
	Enabled bool
	Program string
}

var (
	ruleNameRe = regexp.MustCompile(`(?i)^Rule Name:\s*(.+?)\s*$`)
	enabledRe  = regexp.MustCompile(`(?i)^Enabled:\s*(\S+)`)
	programRe  = regexp.MustCompile(`(?i)^Program:\s*(.+?)\s*$`)
)

// ParseNetshRules parses the output of `netsh advfirewall firewall show rule
// name=<name> verbose`. Unknown/localized labels are tolerated: a rule with no
// parsable Enabled line is treated as enabled, and a rule with no Program line
// keeps an empty Program (so the caller cannot mistake it for a match).
func ParseNetshRules(out string) []FirewallRule {
	var rules []FirewallRule
	var cur *FirewallRule
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimSpace(strings.TrimRight(raw, "\r"))
		if m := ruleNameRe.FindStringSubmatch(line); m != nil {
			rules = append(rules, FirewallRule{Name: m[1], Enabled: true})
			cur = &rules[len(rules)-1]
			continue
		}
		if cur == nil {
			continue
		}
		if m := enabledRe.FindStringSubmatch(line); m != nil {
			v := strings.ToLower(m[1])
			cur.Enabled = v != "no" && v != "false" && v != "0"
			continue
		}
		if m := programRe.FindStringSubmatch(line); m != nil {
			cur.Program = strings.Trim(m[1], `"`)
		}
	}
	return rules
}

// samePath compares Windows paths case-insensitively, ignoring quote and
// slash differences.
func samePath(a, b string) bool {
	norm := func(p string) string {
		p = strings.Trim(strings.TrimSpace(p), `"`)
		p = strings.ReplaceAll(p, "/", `\`)
		return strings.ToLower(p)
	}
	return norm(a) != "" && norm(a) == norm(b)
}

// FirewallRuleMatches reports whether one of rules is an enabled rule named
// FirewallRuleName whose program is exePath.
func FirewallRuleMatches(rules []FirewallRule, exePath string) bool {
	for _, r := range rules {
		if strings.EqualFold(r.Name, FirewallRuleName()) && r.Enabled && samePath(r.Program, exePath) {
			return true
		}
	}
	return false
}

// firewallProgramsScript is the PowerShell fallback that lists the program
// path(s) of the named rule when netsh prints localized labels that
// ParseNetshRules cannot read. One path per line; "Any" for an unscoped rule.
func firewallProgramsScript() string {
	return "(Get-NetFirewallRule -DisplayName '" + FirewallRuleName() + "' | Get-NetFirewallApplicationFilter).Program"
}

// firewallProgramsViaPowerShell returns the rule's program paths, or an error
// when PowerShell is unavailable or printed nothing usable.
func firewallProgramsViaPowerShell(run CommandRunner) ([]string, error) {
	out, err := run("powershell", "-NoProfile", "-NonInteractive", "-Command", firewallProgramsScript())
	if err != nil {
		return nil, fmt.Errorf("powershell: %s", firstLine(string(out), err))
	}
	var programs []string
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimSpace(strings.TrimRight(line, "\r")); line != "" {
			programs = append(programs, strings.Trim(line, `"`))
		}
	}
	if len(programs) == 0 {
		return nil, fmt.Errorf("powershell returned no program path")
	}
	return programs, nil
}

// CheckFirewallRule evaluates the firewall check with the given runner:
// ok when an enabled "StonkAgents Agent" rule exists for exePath.
//
// Presence is decided from the netsh exit code (non-zero when no rule
// matches), never from its message text, so a localized Windows behaves the
// same as an English one. The program path is read from the English labels
// when present and from PowerShell otherwise; when the rule exists but the
// path cannot be determined at all the check reports ok with
// detail.programUnverified so the fix never adds a duplicate rule.
func CheckFirewallRule(run CommandRunner, exePath string) Check {
	detail := map[string]any{"rule": FirewallRuleName(), "program": exePath}
	if strings.TrimSpace(exePath) == "" {
		return Failed(IDFirewall, "daemon executable path unknown", detail)
	}
	out, err := run("netsh", "advfirewall", "firewall", "show", "rule", "name="+FirewallRuleName(), "verbose")
	text := string(out)
	if err != nil {
		if line := firstLine(text, nil); line != "" {
			detail["netshOutput"] = line
		}
		return Missing(IDFirewall, "no inbound firewall rule for the agent", detail)
	}
	rules := ParseNetshRules(text)
	if FirewallRuleMatches(rules, exePath) {
		return OK(IDFirewall, detail)
	}
	programs := rulePrograms(rules)
	if len(programs) == 0 {
		// Localized netsh (no "Rule Name:"/"Program:" lines): ask PowerShell.
		ps, perr := firewallProgramsViaPowerShell(run)
		if perr != nil {
			detail["programUnverified"] = true
			detail["programError"] = perr.Error()
			return OK(IDFirewall, detail)
		}
		for _, p := range ps {
			if samePath(p, exePath) {
				return OK(IDFirewall, detail)
			}
		}
		programs = ps
	}
	detail["existingPrograms"] = programs
	return Missing(IDFirewall, "firewall rule exists but does not cover the installed agent", detail)
}

// AddFirewallRule idempotently ensures the program-scoped inbound allow rule
// for exePath exists and returns the resulting check. A stale rule with the
// same name but another program (previous install directory) is replaced; a
// present rule whose program could not be read is left alone (never
// duplicated).
func AddFirewallRule(run CommandRunner, exePath string) Check {
	if strings.TrimSpace(exePath) == "" {
		return Failed(IDFirewall, "daemon executable path unknown", nil)
	}
	current := CheckFirewallRule(run, exePath)
	if current.Status == StatusOK {
		return current
	}
	if _, stale := current.Detail["existingPrograms"]; stale {
		if out, err := run("netsh", "advfirewall", "firewall", "delete", "rule", "name="+FirewallRuleName()); err != nil {
			return Failed(IDFirewall, fmt.Sprintf("netsh delete rule failed: %s", firstLine(string(out), err)), current.Detail)
		}
	}
	out, err := run("netsh", "advfirewall", "firewall", "add", "rule",
		"name="+FirewallRuleName(), "dir=in", "action=allow", "program="+exePath, "enable=yes")
	if err != nil {
		return Failed(IDFirewall, fmt.Sprintf("netsh add rule failed: %s", firstLine(string(out), err)), current.Detail)
	}
	return CheckFirewallRule(run, exePath)
}

// rulePrograms lists the non-empty program paths of rules.
func rulePrograms(rules []FirewallRule) []string {
	var out []string
	for _, r := range rules {
		if r.Program != "" {
			out = append(out, r.Program)
		}
	}
	return out
}

// firstLine returns the first non-empty output line, or err's text.
func firstLine(out string, err error) string {
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	if err != nil {
		return err.Error()
	}
	return "unknown error"
}

// SCM start types as printed by `sc qc` (START_TYPE column).
const (
	StartTypeBoot     = 0
	StartTypeSystem   = 1
	StartTypeAuto     = 2
	StartTypeDemand   = 3
	StartTypeDisabled = 4
)

var startTypeRe = regexp.MustCompile(`(?i)START_TYPE\s*:\s*(\d+)`)

// ParseScStartType extracts the numeric START_TYPE from `sc qc <service>`
// output. Returns -1 when absent (service missing, access denied, ...).
func ParseScStartType(out string) int {
	m := startTypeRe.FindStringSubmatch(out)
	if m == nil {
		return -1
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return -1
	}
	return n
}

func startTypeName(n int) string {
	switch n {
	case StartTypeBoot:
		return "boot"
	case StartTypeSystem:
		return "system"
	case StartTypeAuto:
		return "auto"
	case StartTypeDemand:
		return "manual"
	case StartTypeDisabled:
		return "disabled"
	default:
		return "unknown"
	}
}

// CheckAutostart is ok when every named service has START_TYPE auto (2).
func CheckAutostart(run CommandRunner, services ...string) Check {
	detail := map[string]any{}
	var notAuto []string
	for _, svc := range services {
		out, err := run("sc", "qc", svc)
		st := ParseScStartType(string(out))
		if st < 0 {
			detail[svc] = "not installed"
			notAuto = append(notAuto, svc)
			if err != nil {
				detail[svc+"Error"] = firstLine(string(out), err)
			}
			continue
		}
		detail[svc] = startTypeName(st)
		if st != StartTypeAuto {
			notAuto = append(notAuto, svc)
		}
	}
	if len(notAuto) == 0 {
		return OK(IDAutostart, detail)
	}
	return Missing(IDAutostart, "not set to start automatically: "+strings.Join(notAuto, ", "), detail)
}

// SetAutostart runs `sc config <svc> start= auto` for each service and
// returns the resulting check.
func SetAutostart(run CommandRunner, services ...string) Check {
	for _, svc := range services {
		if out, err := run("sc", "config", svc, "start=", "auto"); err != nil {
			return Failed(IDAutostart, fmt.Sprintf("sc config %s failed: %s", svc, firstLine(string(out), err)), map[string]any{"service": svc})
		}
	}
	return CheckAutostart(run, services...)
}
