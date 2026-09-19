package installenv

import "testing"

func TestNormalize(t *testing.T) {
	cases := map[string]string{
		"": Prd, "prd": Prd, "prod": Prd, "production": Prd, "PRD": Prd, "nonsense": Prd,
		"dev": Dev, "Development": Dev, " dev ": Dev,
		"stg": Stg, "staging": Stg, "stage": Stg,
	}
	for in, want := range cases {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFromTrackerURL(t *testing.T) {
	cases := map[string]string{
		"https://tracker.dev.stonkagents.com/api/": Dev,
		"https://TRACKER.STG.stonkagents.com":      Stg,
		"https://tracker.dev.stonkagents.com":      Dev,
		"https://tracker.stg.stonkagents.com":      Stg,
		"https://tracker.stonkagents.com":          Prd,
		"http://localhost:7842":                    Prd,
		"":                                         Prd,
		"::bad":                                    Prd,
	}
	for in, want := range cases {
		if got := FromTrackerURL(in); got != want {
			t.Errorf("FromTrackerURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// Production must keep exactly the names, ports and codes shipped so far. The
// 2.5.0; datadir.go renames an existing one on upgrade).
func TestProductionUnchanged(t *testing.T) {
	p := ForEnv(Prd)
	want := Profile{
		Env: Prd, ProductName: "StonkAgents", InstallDirName: "StonkAgents", DataDirName: ".stonkagents",
		DaemonService: "StonkAgentsDaemon", ControllerService: "StonkAgentsController",
		DaemonDisplay: "StonkAgents Daemon", ControllerDisplay: "StonkAgents Controller",
		FirewallRule: "StonkAgents Agent", DaemonPort: 7841, ControllerPort: 7840, GatewayPort: 18789,
		CLIProfile:     "",
		MsiUpgradeCode: "B7E8E8A1-9C2D-4E5F-8A3B-1D2C3E4F5A6B", BundleUpgradeCode: "C8D9E0F1-2A3B-4C5D-6E7F-8A9B0C1D2E3F",
	}
	if p != want {
		t.Errorf("production profile changed:\n got %+v\nwant %+v", p, want)
	}
	if p.GatewayTaskName() != "OpenClaw Gateway" || p.CLIStateDirName() != ".openclaw" || p.CLIEnv() != nil {
		t.Errorf("production CLI profile must be the default: task %q dir %q env %v", p.GatewayTaskName(), p.CLIStateDirName(), p.CLIEnv())
	}
	if p.ControllerAddr() != "127.0.0.1:7840" || p.DaemonHealthURL() != "http://localhost:7841/health" || p.ControllerURL() != "http://127.0.0.1:7840" {
		t.Errorf("production URLs changed: %s %s %s", p.ControllerAddr(), p.DaemonHealthURL(), p.ControllerURL())
	}
}

// Nothing that identifies an install may be shared between two environments.
func TestEnvironmentsAreDisjoint(t *testing.T) {
	seen := map[string]string{} // "kind\x00value" -> env
	seenPort := map[int]string{}
	check := func(env, what, v string) {
		key := what + "\x00" + v
		if prev, ok := seen[key]; ok {
			t.Errorf("%s %q is shared by %s and %s", what, v, prev, env)
		}
		seen[key] = env
	}
	checkPort := func(env string, port int) {
		if prev, ok := seenPort[port]; ok {
			t.Errorf("port %d is shared by %s and %s", port, prev, env)
		}
		seenPort[port] = env
	}
	for _, env := range []string{Prd, Stg, Dev} {
		p := ForEnv(env)
		if p.Env != env {
			t.Fatalf("ForEnv(%q).Env = %q", env, p.Env)
		}
		check(env, "product name", p.ProductName)
		check(env, "install dir", p.InstallDirName)
		check(env, "data dir", p.DataDirName)
		check(env, "daemon service", p.DaemonService)
		check(env, "controller service", p.ControllerService)
		check(env, "daemon display", p.DaemonDisplay)
		check(env, "controller display", p.ControllerDisplay)
		check(env, "firewall rule", p.FirewallRule)
		check(env, "msi upgrade code", p.MsiUpgradeCode)
		check(env, "bundle upgrade code", p.BundleUpgradeCode)
		check(env, "gateway task", p.GatewayTaskName())
		check(env, "cli state dir", p.CLIStateDirName())
		checkPort(env, p.DaemonPort)
		checkPort(env, p.ControllerPort)
		checkPort(env, p.GatewayPort)
	}
}

func TestPortTable(t *testing.T) {
	cases := map[string][3]int{Prd: {7841, 7840, 18789}, Stg: {7851, 7850, 19002}, Dev: {7861, 7860, 19001}}
	for env, want := range cases {
		p := ForEnv(env)
		if got := [3]int{p.DaemonPort, p.ControllerPort, p.GatewayPort}; got != want {
			t.Errorf("%s ports = %v, want %v", env, got, want)
		}
	}
}

func TestCurrentReadsEnvVar(t *testing.T) {
	t.Setenv(EnvVar, "dev")
	if Current().Env != Dev {
		t.Errorf("Current() with %s=dev = %q", EnvVar, Current().Env)
	}
	t.Setenv(EnvVar, "")
	if Current().Env != Prd {
		t.Errorf("Current() with %s unset = %q", EnvVar, Current().Env)
	}
}

func TestCLIEnvForDev(t *testing.T) {
	got := ForEnv(Dev).CLIEnv()
	want := []string{"STONKAGENTS_ENV=dev", "OPENCLAW_PROFILE=dev", "OPENCLAW_GATEWAY_PORT=19001"}
	if len(got) != len(want) {
		t.Fatalf("CLIEnv = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("CLIEnv[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// The portal allowlist covers dev, stg and prd on every product domain.
func TestPortalOrigins(t *testing.T) {
	want := []string{
		"https://dev.stonkagents.com", "https://stg.stonkagents.com", "https://stonkagents.com",
	}
	got := PortalOrigins()
	if len(got) != len(want) {
		t.Fatalf("PortalOrigins() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("PortalOrigins()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
