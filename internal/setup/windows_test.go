package setup

import (
	"errors"
	"strings"
	"testing"
)

const netshVerboseOut = "\r\nRule Name:                            StonkAgents Agent\r\n" +
	"----------------------------------------------------------------------\r\n" +
	"Enabled:                              Yes\r\n" +
	"Direction:                            In\r\n" +
	"Profiles:                             Domain,Private,Public\r\n" +
	"Grouping:                             \r\n" +
	"LocalIP:                              Any\r\n" +
	"RemoteIP:                             Any\r\n" +
	"Protocol:                             Any\r\n" +
	"Edge traversal:                       Defer to user\r\n" +
	"Program:                              C:\\Program Files\\StonkAgents\\stonkagents-daemon.exe\r\n" +
	"InterfaceTypes:                       Any\r\n" +
	"Security:                             NotRequired\r\n" +
	"Rule source:                          Local Setting\r\n" +
	"Action:                               Allow\r\n" +
	"Ok.\r\n"

const netshNoRules = "\r\nNo rules match the specified criteria.\r\n"

const exe = `C:\Program Files\StonkAgents\stonkagents-daemon.exe`

// fakeRunner records commands and answers from a script keyed by the first
// two or three args (e.g. "netsh advfirewall firewall show").
type fakeRunner struct {
	calls   []string
	answers map[string]func() ([]byte, error)
}

func (f *fakeRunner) run(name string, args ...string) ([]byte, error) {
	line := name + " " + strings.Join(args, " ")
	f.calls = append(f.calls, line)
	for prefix, fn := range f.answers {
		if strings.HasPrefix(line, prefix) {
			return fn()
		}
	}
	return nil, errors.New("unexpected command: " + line)
}

func TestParseNetshRules_Verbose(t *testing.T) {
	rules := ParseNetshRules(netshVerboseOut)
	if len(rules) != 1 {
		t.Fatalf("rules = %d, want 1", len(rules))
	}
	r := rules[0]
	if r.Name != FirewallRuleName() || !r.Enabled || r.Program != exe {
		t.Errorf("rule = %+v", r)
	}
	if !FirewallRuleMatches(rules, strings.ToLower(exe)) {
		t.Error("expected case-insensitive path match")
	}
	if FirewallRuleMatches(rules, `D:\other\stonkagents-daemon.exe`) {
		t.Error("different program must not match")
	}
}

func TestParseNetshRules_DisabledAndNone(t *testing.T) {
	disabled := strings.Replace(netshVerboseOut, "Enabled:                              Yes", "Enabled:                              No", 1)
	if FirewallRuleMatches(ParseNetshRules(disabled), exe) {
		t.Error("disabled rule must not count")
	}
	if got := ParseNetshRules(netshNoRules); len(got) != 0 {
		t.Errorf("no-rules output parsed %d rules", len(got))
	}
}

func TestCheckFirewallRule(t *testing.T) {
	t.Run("present", func(t *testing.T) {
		f := &fakeRunner{answers: map[string]func() ([]byte, error){
			"netsh advfirewall firewall show": func() ([]byte, error) { return []byte(netshVerboseOut), nil },
		}}
		c := CheckFirewallRule(f.run, exe)
		if c.Status != StatusOK {
			t.Fatalf("status = %s (%s)", c.Status, c.Message)
		}
		if !strings.Contains(f.calls[0], `name=`+FirewallRuleName()+" verbose") {
			t.Errorf("show rule call = %q", f.calls[0])
		}
	})
	t.Run("absent", func(t *testing.T) {
		f := &fakeRunner{answers: map[string]func() ([]byte, error){
			"netsh advfirewall firewall show": func() ([]byte, error) { return []byte(netshNoRules), errors.New("exit status 1") },
		}}
		c := CheckFirewallRule(f.run, exe)
		if c.Status != StatusMissing {
			t.Fatalf("status = %s (%s)", c.Status, c.Message)
		}
	})
	t.Run("absent on localized netsh (exit code decides, not the message)", func(t *testing.T) {
		f := &fakeRunner{answers: map[string]func() ([]byte, error){
			"netsh advfirewall firewall show": func() ([]byte, error) { return []byte(netshNoRulesDE), errors.New("exit status 1") },
		}}
		c := CheckFirewallRule(f.run, exe)
		if c.Status != StatusMissing || c.Detail["netshOutput"] != strings.TrimSpace(netshNoRulesDE) {
			t.Fatalf("check = %+v", c)
		}
		for _, call := range f.calls {
			if strings.HasPrefix(call, "powershell") {
				t.Errorf("powershell must not run when netsh reports no rule: %q", call)
			}
		}
	})
	t.Run("present on localized netsh, program via powershell", func(t *testing.T) {
		f := &fakeRunner{answers: map[string]func() ([]byte, error){
			"netsh advfirewall firewall show": func() ([]byte, error) { return []byte(netshVerboseOutDE), nil },
			"powershell":                      func() ([]byte, error) { return []byte(exe + "\r\n"), nil },
		}}
		c := CheckFirewallRule(f.run, exe)
		if c.Status != StatusOK {
			t.Fatalf("check = %+v", c)
		}
		ps := ""
		for _, call := range f.calls {
			if strings.HasPrefix(call, "powershell") {
				ps = call
			}
		}
		if !strings.Contains(ps, "-NoProfile") || !strings.Contains(ps, "Get-NetFirewallRule -DisplayName '"+FirewallRuleName()+"'") || !strings.Contains(ps, "Get-NetFirewallApplicationFilter") {
			t.Errorf("powershell call = %q", ps)
		}
	})
	t.Run("present on localized netsh, powershell reports another program", func(t *testing.T) {
		f := &fakeRunner{answers: map[string]func() ([]byte, error){
			"netsh advfirewall firewall show": func() ([]byte, error) { return []byte(netshVerboseOutDE), nil },
			"powershell":                      func() ([]byte, error) { return []byte(`C:\old\stonkagents-daemon.exe` + "\r\n"), nil },
		}}
		c := CheckFirewallRule(f.run, exe)
		if c.Status != StatusMissing {
			t.Fatalf("check = %+v", c)
		}
		if got, _ := c.Detail["existingPrograms"].([]string); len(got) != 1 || got[0] != `C:\old\stonkagents-daemon.exe` {
			t.Errorf("existingPrograms = %v", c.Detail["existingPrograms"])
		}
	})
	t.Run("present but program unknown is ok (never duplicated)", func(t *testing.T) {
		f := &fakeRunner{answers: map[string]func() ([]byte, error){
			"netsh advfirewall firewall show": func() ([]byte, error) { return []byte(netshVerboseOutDE), nil },
			"powershell": func() ([]byte, error) {
				return []byte("Get-NetFirewallRule : not recognized"), errors.New("exit status 1")
			},
		}}
		c := CheckFirewallRule(f.run, exe)
		if c.Status != StatusOK || c.Detail["programUnverified"] != true {
			t.Fatalf("check = %+v", c)
		}
	})
	t.Run("empty exe", func(t *testing.T) {
		if c := CheckFirewallRule(nil, ""); c.Status != StatusFailed {
			t.Fatalf("status = %s", c.Status)
		}
	})
}

// German netsh: every label differs from the English ones ParseNetshRules knows.
const netshVerboseOutDE = "\r\nRegelname:                            StonkAgents Agent\r\n" +
	"----------------------------------------------------------------------\r\n" +
	"Aktiviert:                            Ja\r\n" +
	"Richtung:                             Eingehend\r\n" +
	"Profile:                              Domäne,Privat,Öffentlich\r\n" +
	"Gruppierung:                          \r\n" +
	"LokaleIP:                             Beliebig\r\n" +
	"RemoteIP:                             Beliebig\r\n" +
	"Protokoll:                            Beliebig\r\n" +
	"Edgeausnahme:                         Benutzer zurückstellen\r\n" +
	"Programm:                             C:\\Program Files\\StonkAgents\\stonkagents-daemon.exe\r\n" +
	"Schnittstellentypen:                  Beliebig\r\n" +
	"Sicherheit:                           Nicht erforderlich\r\n" +
	"Regelquelle:                          Lokale Einstellung\r\n" +
	"Aktion:                               Zulassen\r\n" +
	"OK.\r\n"

const netshNoRulesDE = "\r\nKeine Regeln stimmen mit den angegebenen Kriterien überein.\r\n"

func TestParseNetshRules_LocalizedYieldsNothing(t *testing.T) {
	if got := ParseNetshRules(netshVerboseOutDE); len(got) != 0 {
		t.Errorf("German output parsed %d rules; labels must not be guessed", len(got))
	}
}

func TestAddFirewallRule_Localized(t *testing.T) {
	t.Run("present and unverifiable: no delete, no add", func(t *testing.T) {
		f := &fakeRunner{answers: map[string]func() ([]byte, error){
			"netsh advfirewall firewall show": func() ([]byte, error) { return []byte(netshVerboseOutDE), nil },
			"powershell":                      func() ([]byte, error) { return nil, errors.New("exit status 1") },
		}}
		c := AddFirewallRule(f.run, exe)
		if c.Status != StatusOK {
			t.Fatalf("check = %+v", c)
		}
		for _, call := range f.calls {
			if strings.Contains(call, " add rule") || strings.Contains(call, " delete rule") {
				t.Errorf("rule must not be touched when its program is unknown: %q", call)
			}
		}
	})
	t.Run("stale program via powershell is replaced", func(t *testing.T) {
		deleted := false
		f := &fakeRunner{}
		f.answers = map[string]func() ([]byte, error){
			"netsh advfirewall firewall show": func() ([]byte, error) { return []byte(netshVerboseOutDE), nil },
			"powershell": func() ([]byte, error) {
				if deleted {
					return []byte(exe + "\r\n"), nil
				}
				return []byte(`C:\old\stonkagents-daemon.exe` + "\r\n"), nil
			},
			"netsh advfirewall firewall delete": func() ([]byte, error) { deleted = true; return []byte("1 Regel(n) gelöscht."), nil },
			"netsh advfirewall firewall add":    func() ([]byte, error) { return []byte("OK."), nil },
		}
		c := AddFirewallRule(f.run, exe)
		if c.Status != StatusOK || !deleted {
			t.Fatalf("check = %+v deleted=%v", c, deleted)
		}
	})
}

func TestAddFirewallRule(t *testing.T) {
	t.Run("idempotent when present", func(t *testing.T) {
		f := &fakeRunner{answers: map[string]func() ([]byte, error){
			"netsh advfirewall firewall show": func() ([]byte, error) { return []byte(netshVerboseOut), nil },
		}}
		c := AddFirewallRule(f.run, exe)
		if c.Status != StatusOK {
			t.Fatalf("status = %s (%s)", c.Status, c.Message)
		}
		for _, call := range f.calls {
			if strings.Contains(call, " add rule") {
				t.Errorf("add rule must not run when the rule exists: %q", call)
			}
		}
	})
	t.Run("adds when absent", func(t *testing.T) {
		added := false
		f := &fakeRunner{}
		f.answers = map[string]func() ([]byte, error){
			"netsh advfirewall firewall show": func() ([]byte, error) {
				if added {
					return []byte(netshVerboseOut), nil
				}
				return []byte(netshNoRules), errors.New("exit status 1")
			},
			"netsh advfirewall firewall add": func() ([]byte, error) { added = true; return []byte("Ok."), nil },
		}
		c := AddFirewallRule(f.run, exe)
		if c.Status != StatusOK {
			t.Fatalf("status = %s (%s)", c.Status, c.Message)
		}
		want := "netsh advfirewall firewall add rule name=" + FirewallRuleName() + " dir=in action=allow program=" + exe + " enable=yes"
		found := false
		for _, call := range f.calls {
			if call == want {
				found = true
			}
		}
		if !found {
			t.Errorf("add rule call missing; calls = %v", f.calls)
		}
	})
	t.Run("replaces stale program", func(t *testing.T) {
		stale := strings.Replace(netshVerboseOut, exe, `C:\old\stonkagents-daemon.exe`, 1)
		deleted := false
		f := &fakeRunner{}
		f.answers = map[string]func() ([]byte, error){
			"netsh advfirewall firewall show": func() ([]byte, error) {
				if deleted {
					return []byte(netshVerboseOut), nil
				}
				return []byte(stale), nil
			},
			"netsh advfirewall firewall delete": func() ([]byte, error) { deleted = true; return []byte("Deleted 1 rule(s)."), nil },
			"netsh advfirewall firewall add":    func() ([]byte, error) { return []byte("Ok."), nil },
		}
		c := AddFirewallRule(f.run, exe)
		if c.Status != StatusOK {
			t.Fatalf("status = %s (%s)", c.Status, c.Message)
		}
		if !deleted {
			t.Error("stale rule was not deleted")
		}
	})
	t.Run("add fails", func(t *testing.T) {
		f := &fakeRunner{answers: map[string]func() ([]byte, error){
			"netsh advfirewall firewall show": func() ([]byte, error) { return []byte(netshNoRules), errors.New("exit status 1") },
			"netsh advfirewall firewall add": func() ([]byte, error) {
				return []byte("The requested operation requires elevation"), errors.New("exit status 1")
			},
		}}
		c := AddFirewallRule(f.run, exe)
		if c.Status != StatusFailed || !strings.Contains(c.Message, "elevation") {
			t.Fatalf("check = %+v", c)
		}
	})
}

const scQcAuto = "[SC] QueryServiceConfig SUCCESS\r\n\r\nSERVICE_NAME: StonkAgentsDaemon\r\n" +
	"        TYPE               : 10  WIN32_OWN_PROCESS\r\n" +
	"        START_TYPE         : 2   AUTO_START\r\n" +
	"        ERROR_CONTROL      : 1   NORMAL\r\n" +
	"        BINARY_PATH_NAME   : C:\\Program Files\\StonkAgents\\stonkagents-svc.exe\r\n"

const scQcDemand = "[SC] QueryServiceConfig SUCCESS\r\n\r\nSERVICE_NAME: StonkAgentsController\r\n" +
	"        START_TYPE         : 3   DEMAND_START\r\n"

func TestParseScStartType(t *testing.T) {
	if got := ParseScStartType(scQcAuto); got != StartTypeAuto {
		t.Errorf("auto = %d", got)
	}
	if got := ParseScStartType(scQcDemand); got != StartTypeDemand {
		t.Errorf("demand = %d", got)
	}
	if got := ParseScStartType("[SC] OpenService FAILED 1060:\r\nThe specified service does not exist"); got != -1 {
		t.Errorf("missing = %d", got)
	}
}

func TestCheckAndSetAutostart(t *testing.T) {
	configured := map[string]bool{}
	f := &fakeRunner{}
	f.answers = map[string]func() ([]byte, error){
		"sc qc " + DaemonServiceName(): func() ([]byte, error) { return []byte(scQcAuto), nil },
		"sc qc " + ControllerServiceName(): func() ([]byte, error) {
			if configured[ControllerServiceName()] {
				return []byte(strings.Replace(scQcDemand, "3   DEMAND_START", "2   AUTO_START", 1)), nil
			}
			return []byte(scQcDemand), nil
		},
		"sc config": func() ([]byte, error) {
			configured[ControllerServiceName()] = true
			return []byte("[SC] ChangeServiceConfig SUCCESS"), nil
		},
	}
	c := CheckAutostart(f.run, DaemonServiceName(), ControllerServiceName())
	if c.Status != StatusMissing || !strings.Contains(c.Message, ControllerServiceName()) {
		t.Fatalf("check = %+v", c)
	}
	if c.Detail[DaemonServiceName()] != "auto" || c.Detail[ControllerServiceName()] != "manual" {
		t.Errorf("detail = %v", c.Detail)
	}

	c = SetAutostart(f.run, DaemonServiceName(), ControllerServiceName())
	if c.Status != StatusOK {
		t.Fatalf("after fix: %+v", c)
	}
	sawConfig := false
	for _, call := range f.calls {
		if call == "sc config "+DaemonServiceName()+" start= auto" {
			sawConfig = true
		}
	}
	if !sawConfig {
		t.Errorf("sc config start= auto not issued; calls = %v", f.calls)
	}
}

func TestSetAutostart_Failure(t *testing.T) {
	f := &fakeRunner{answers: map[string]func() ([]byte, error){
		"sc config": func() ([]byte, error) {
			return []byte("[SC] OpenService FAILED 5:\r\nAccess is denied."), errors.New("exit status 1")
		},
	}}
	c := SetAutostart(f.run, DaemonServiceName())
	if c.Status != StatusFailed || !strings.Contains(c.Message, "OpenService FAILED") {
		t.Fatalf("check = %+v", c)
	}
}
