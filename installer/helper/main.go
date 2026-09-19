// Package main: MSI post-install helper. Run by WiX custom action with
// one argument: "installDir;secretsDir;dataDir;logonUser". Writes config.yaml,
// creates secrets (via genkeys.exe), writes daemon.env, creates/starts the
// services, then registers and starts the background command tools task
// (installer/helper/commandtools.go). logonUser (the MSI's LogonUser
// property) may be empty; older callers pass three fields.
package main

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/stonkagents/agent/internal/config"
	"github.com/stonkagents/agent/internal/installenv"
	"github.com/stonkagents/agent/internal/setup"
)

// Service names, display names, ports and folders come from the environment
// profile (internal/installenv): production keeps StonkAgentsDaemon /
// StonkAgentsController and ports 7841/7840, dev and stg get their own so the
// three installs coexist.

// defaultTrackerURL is set at build time via -ldflags "-X main.defaultTrackerURL=...".
// Used when STONKAGENTS_TRACKER_URL is not set.
var defaultTrackerURL string

// defaultEnvironment is set at build time via -ldflags "-X main.defaultEnvironment=dev|stg|prd"
// (scripts/build-msi.ps1 -Environment). STONKAGENTS_ENV in the environment wins;
// when both are empty the environment is derived from the tracker URL.
var defaultEnvironment string

// resolveEnvironment picks the install environment: STONKAGENTS_ENV, then the
// baked defaultEnvironment, then the tracker host (tracker.dev.* is dev).
func resolveEnvironment(envVar, baked, trackerURL string) string {
	if v := strings.TrimSpace(envVar); v != "" {
		return installenv.Normalize(v)
	}
	if v := strings.TrimSpace(baked); v != "" {
		return installenv.Normalize(v)
	}
	return installenv.FromTrackerURL(trackerURL)
}

// defaultLiveAgentDownloadAPIKey is set at build time via -ldflags "-X main.defaultLiveAgentDownloadAPIKey=...".
// Written to daemon.env so POST /api/v1/live-agent/download requires X-Live-Agent-Key (stonkagents-replicator). Empty = omit line (open endpoint).
var defaultLiveAgentDownloadAPIKey string

// mustGenerateGatewayToken returns a cryptographically random token for gateway Bearer auth (base64url, 32 bytes).
func mustGenerateGatewayToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("gateway token rand: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// secretsFileHasKey returns true if path exists and contains a non-empty STONKAGENTS_PRIVATE_KEY= value (not just a comment).
func secretsFileHasKey(path string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") || line == "" {
			continue
		}
		if strings.HasPrefix(line, "STONKAGENTS_PRIVATE_KEY=") {
			val := strings.TrimSpace(strings.TrimPrefix(line, "STONKAGENTS_PRIVATE_KEY="))
			if val != "" && val != "<base64>" {
				return true
			}
			break
		}
	}
	return false
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: setuphelper.exe installDir;secretsDir;dataDir[;logonUser]")
		os.Exit(1)
	}
	parts := strings.SplitN(strings.TrimSpace(os.Args[1]), ";", 4)
	if len(parts) < 3 {
		fmt.Fprintln(os.Stderr, "expected installDir;secretsDir;dataDir[;logonUser]")
		os.Exit(1)
	}
	logonUser := ""
	if len(parts) == 4 {
		logonUser = strings.Trim(strings.TrimSpace(parts[3]), `"`)
	}
	// Trim quotes so MSI/WiX passing paths like "C:\...\data" doesn't end up in config
	installDir := strings.Trim(strings.TrimSpace(parts[0]), `"`)
	secretsDir := strings.Trim(strings.TrimSpace(parts[1]), `"`)
	dataDir := strings.Trim(strings.TrimSpace(parts[2]), `"`)
	if installDir == "" || secretsDir == "" || dataDir == "" {
		fmt.Fprintln(os.Stderr, "installDir, secretsDir, dataDir must be non-empty")
		os.Exit(1)
	}

	logf := func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) }

	configPath := filepath.Join(installDir, "config.yaml")
	secretsFile := filepath.Join(secretsDir, "secrets.env")
	daemonEnvPath := filepath.Join(installDir, "daemon.env")
	genkeysExe := filepath.Join(installDir, "genkeys.exe")

	// 1) Write config.yaml (single-quoted data_dir so Windows backslashes and " don't break YAML; escape ' and %)
	// peer_id and public_key empty: daemon derives them from private key (secrets.env) at StartP2P.
	// Tracker URL: env STONKAGENTS_TRACKER_URL > baked defaultTrackerURL (from build -ldflags) > config.DefaultTrackerURL.
	trackerURL := config.TrackerURLFromEnv()
	if trackerURL == "" {
		trackerURL = strings.TrimSpace(defaultTrackerURL)
	}
	if trackerURL == "" {
		trackerURL = config.DefaultTrackerURL
	}
	env := installenv.ForEnv(resolveEnvironment(os.Getenv(installenv.EnvVar), defaultEnvironment, trackerURL))
	trackerURLEscaped := strings.ReplaceAll(trackerURL, `\`, `\\`)
	trackerURLEscaped = strings.ReplaceAll(trackerURLEscaped, `"`, `\"`)
	dataDirEscaped := strings.ReplaceAll(dataDir, "'", "''")
	dataDirEscaped = strings.ReplaceAll(dataDirEscaped, "%", "%%")
	configYaml := fmt.Sprintf(`peer_id: ""
public_key: ""
daemon_host: "127.0.0.1"
daemon_port: %d
bootstrap_peers: []
tracker_url: "%s"
ask_use_tracker: true
data_dir: '%s'
gateway_url: "%s"
`, env.DaemonPort, trackerURLEscaped, dataDirEscaped, env.GatewayURL())
	if err := os.WriteFile(configPath, []byte(configYaml), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "write config: %v\n", err)
		os.Exit(1)
	}

	// 2) Create secrets dir; generate secrets.env if missing or only has placeholder
	if err := os.MkdirAll(secretsDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "mkdir secrets: %v\n", err)
		os.Exit(1)
	}
	needSecrets := !secretsFileHasKey(secretsFile)
	if needSecrets {
		wroteSecrets := false
		if _, err := os.Stat(genkeysExe); err == nil {
			// genkeys --out <path> writes STONKAGENTS_PRIVATE_KEY= directly (no stdout parsing)
			cmd := exec.Command(genkeysExe, "--out", secretsFile)
			cmd.Dir = installDir
			tmpDir := os.TempDir()
			if tmpDir != "" {
				cmd.Env = append(os.Environ(), "TEMP="+tmpDir, "TMP="+tmpDir)
			}
			if runErr := cmd.Run(); runErr == nil {
				wroteSecrets = true
			} else {
				fmt.Fprintf(os.Stderr, "genkeys failed (continuing with placeholder): %v\n", runErr)
			}
		}
		if !wroteSecrets {
			// No genkeys.exe or genkeys failed; write placeholder only if file doesn't exist (don't overwrite real keys)
			if _, err := os.Stat(secretsFile); os.IsNotExist(err) {
				_ = os.WriteFile(secretsFile, []byte("# Add: STONKAGENTS_PRIVATE_KEY=<base64>\n"), 0600)
			}
		}
	}

	// 3) Write daemon.env (include gateway token so daemon can auth to StonkAgents gateway; post-install reads it for onboard)
	gatewayToken := mustGenerateGatewayToken()
	daemonEnv := "STONKAGENTS_CONFIG_PATH=" + configPath + "\nSTONKAGENTS_SECRETS_PATH=" + secretsFile + "\nSTONKAGENTS_GATEWAY_TOKEN=" + gatewayToken + "\n"
	// The environment: the controller (which loads daemon.env) and the daemon (which
	// gets it from the service wrapper) derive their service names and ports from it.
	daemonEnv += installenv.EnvVar + "=" + env.Env + "\n"
	if k := strings.TrimSpace(defaultLiveAgentDownloadAPIKey); k != "" {
		daemonEnv += "STONKAGENTS_LIVE_AGENT_DOWNLOAD_API_KEY=" + k + "\n"
	}
	// Pin the controller's update manifest to the same environment as the tracker
	// (tracker.dev.* -> releases.dev.*). Without this a dev install reads the
	// production manifest and offers whatever production ships as an "update".
	if releasesURL := releasesURLForTracker(trackerURL); releasesURL != "" {
		daemonEnv += "STONKAGENTS_RELEASES_URL=" + releasesURL + "\n"
	}
	if err := os.WriteFile(daemonEnvPath, []byte(daemonEnv), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "write daemon.env: %v\n", err)
		os.Exit(1)
	}

	// 4) Create Windows services. Use full path to sc.exe for SYSTEM (MSI custom action).
	scExe := filepath.Join(os.Getenv("SystemRoot"), "System32", "sc.exe")
	if os.Getenv("SystemRoot") == "" {
		scExe = "sc.exe"
	}

	// Remove any previous registrations of this environment's names so the services
	// below are created fresh (a dev or stg install must never touch another
	// environment's services). Errors are ignored: the services may simply not exist.
	removeService := func(name string) {
		removeServiceAndWait(scExe, name, serviceWait)
	}
	removeService(env.DaemonService)
	removeService(env.ControllerService)

	// 4a) Daemon service (binPath = stonkagents-svc.exe). No failure-restart so controller manages lifecycle.
	svcExe := filepath.Join(installDir, "stonkagents-svc.exe")
	if out, err := createServiceRetry(scExe, serviceWait, env.DaemonService, "binPath=", svcExe, "start=", "auto", "DisplayName=", env.DaemonDisplay); err != nil {
		fmt.Fprintf(os.Stderr, "sc create daemon: %s %v\n", out, err)
		os.Exit(1)
	}
	_ = exec.Command(scExe, "description", env.DaemonService, env.ProductName+": P2P knowledge sync for AI agents").Run()

	// 4b) Controller service (always running; exposes HTTP to start/stop daemon).
	controllerExe := filepath.Join(installDir, "stonkagents-controller-svc.exe")
	if out, err := createServiceRetry(scExe, serviceWait, env.ControllerService, "binPath=", controllerExe, "start=", "auto", "DisplayName=", env.ControllerDisplay); err != nil {
		fmt.Fprintf(os.Stderr, "sc create controller: %s %v\n", out, err)
		os.Exit(1)
	}
	_ = exec.Command(scExe, "description", env.ControllerService, env.ProductName+": Controller service (start/stop daemon)").Run()
	_ = exec.Command(scExe, "failure", env.ControllerService, "reset=86400", "actions=restart/30000/restart/30000/restart/30000").Run()

	// 4c) Program-scoped inbound firewall rule for the daemon (idempotent; the MSI
	// custom action runs elevated). Non-fatal: the portal's Permissions step can
	// add it later through the controller (POST /setup/firewall).
	if check := ensureFirewallRule(system32Runner(), installDir); check.Status != setup.StatusOK {
		fmt.Fprintf(os.Stderr, "firewall rule (non-fatal): %s\n", check.Message)
	}

	// 5) Start controller first (so UI can call /status, /start, /stop), then daemon.
	if out, err := exec.Command(scExe, "start", env.ControllerService).CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "sc start controller (non-fatal): %s %v\n", out, err)
	}
	if out, err := exec.Command(scExe, "start", env.DaemonService).CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "sc start daemon (non-fatal): %s %v\n", out, err)
	}

	// 6) Post-install onboarding (npm install, stonkagents onboard, gateway start)
	// runs in the background, after the installer has finished: register the
	// "<ProductName> command tools" scheduled task for the installing user and
	// start it. It runs stonkagents-tools.exe, which needs the user's profile (npm's
	// global prefix, the CLI config, the gateway task), not SYSTEM's. Progress
	// lands in <dataDir>\command-tools.json for the portal.
	startCommandTools(commandRunner, env, installDir, dataDir, logonUser, logf)
}

// releasesURLForTracker derives the release host that belongs to a tracker host:
// https://tracker.<env>.stonkagents.com -> https://releases.<env>.stonkagents.com/ and
// https://tracker.stonkagents.com -> https://releases.stonkagents.com/. Returns "" for
// anything else (localhost, custom hosts), which leaves the controller on its
// built-in default.
func releasesURLForTracker(trackerURL string) string {
	u, err := url.Parse(strings.TrimSpace(trackerURL))
	if err != nil || u.Host == "" {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if !strings.HasPrefix(host, "tracker.") {
		return ""
	}
	return "https://releases." + strings.TrimPrefix(host, "tracker.") + "/"
}

// daemonExeName is the daemon binary the firewall rule is scoped to.
const daemonExeName = "stonkagents-daemon.exe"

// system32Runner runs netsh/sc/powershell by full path so the MSI custom
// action (SYSTEM, minimal PATH) finds them.
func system32Runner() setup.CommandRunner {
	root := os.Getenv("SystemRoot")
	return func(name string, args ...string) ([]byte, error) {
		if root != "" {
			switch name {
			case "netsh", "sc":
				name = filepath.Join(root, "System32", name+".exe")
			case "powershell":
				name = filepath.Join(root, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
			}
		}
		return exec.Command(name, args...).CombinedOutput()
	}
}

// ensureFirewallRule adds the "StonkAgents Agent" inbound allow rule for
// <installDir>\stonkagents-daemon.exe when it is missing or points at another
// path, and returns the resulting check.
func ensureFirewallRule(run setup.CommandRunner, installDir string) setup.Check {
	// Explicit backslash join: this path is handed to netsh on Windows even
	// when the helper is unit-tested on another OS.
	exe := strings.TrimRight(installDir, `\/`) + `\` + daemonExeName
	return setup.AddFirewallRule(run, exe)
}
