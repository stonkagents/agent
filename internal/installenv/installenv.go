// Package installenv describes one StonkAgents installation environment
// (dev, stg, prd) so the three can live side by side on one Windows
// machine: distinct install and data folders, service names, ports,
// firewall rule, CLI profile and gateway task.
//
// Production keeps every name and port it has today (only the data folder was
// and their upgrade path are untouched. The environment is chosen once at
// install time (build parameter, or derived from the tracker URL) and
// written to daemon.env as STONKAGENTS_ENV; the daemon, the controller and
// the setup surface read it back through Current.
package installenv

import (
	"fmt"
	"net/url"
	"os"
	"strings"
)

// Environment names.
const (
	Dev = "dev"
	Stg = "stg"
	Prd = "prd"
)

// EnvVar carries the environment between the installer and the services
// (daemon.env, service environment, package arguments).
const EnvVar = "STONKAGENTS_ENV"

// Profile is everything that must differ per environment.
type Profile struct {
	Env string // dev, stg or prd

	ProductName    string // ARP entry, Start menu folder, window captions
	InstallDirName string // folder under Program Files
	DataDirName    string // folder under the user profile

	DaemonService     string // SCM service name
	ControllerService string
	DaemonDisplay     string // services.msc display name
	ControllerDisplay string
	FirewallRule      string // inbound allow rule for the daemon

	DaemonPort     int
	ControllerPort int
	GatewayPort    int // the CLI's gateway (OPENCLAW_GATEWAY_PORT)

	CLIProfile string // OPENCLAW_PROFILE; empty means the CLI default profile

	// Windows Installer identities: the MSI and the bundle each keep one
	// UpgradeCode per environment so an install never upgrades another
	// environment's. Production keeps the codes it has always had.
	MsiUpgradeCode    string
	BundleUpgradeCode string
}

var profiles = map[string]Profile{
	Prd: {
		Env:               Prd,
		ProductName:       "StonkAgents",
		InstallDirName:    "StonkAgents",
		DataDirName:       ".stonkagents",
		DaemonService:     "StonkAgentsDaemon",
		ControllerService: "StonkAgentsController",
		DaemonDisplay:     "StonkAgents Daemon",
		ControllerDisplay: "StonkAgents Controller",
		FirewallRule:      "StonkAgents Agent",
		DaemonPort:        7841,
		ControllerPort:    7840,
		GatewayPort:       18789,
		CLIProfile:        "",
		MsiUpgradeCode:    "B7E8E8A1-9C2D-4E5F-8A3B-1D2C3E4F5A6B",
		BundleUpgradeCode: "C8D9E0F1-2A3B-4C5D-6E7F-8A9B0C1D2E3F",
	},
	Stg: {
		Env:               Stg,
		ProductName:       "StonkAgents Staging",
		InstallDirName:    "StonkAgents Staging",
		DataDirName:       ".stonkagents-stg",
		DaemonService:     "stonkagents-daemon-stg",
		ControllerService: "stonkagents-controller-stg",
		DaemonDisplay:     "StonkAgents Staging Daemon",
		ControllerDisplay: "StonkAgents Staging Controller",
		FirewallRule:      "StonkAgents Staging Agent",
		DaemonPort:        7851,
		ControllerPort:    7850,
		GatewayPort:       19002,
		CLIProfile:        "stg",
		MsiUpgradeCode:    "5D2A7C31-8E4B-4F6A-9C1D-3B5E7F9A2C41",
		BundleUpgradeCode: "6E3B8D42-9F5C-4A7B-8D2E-4C6F8A1B3D52",
	},
	Dev: {
		Env:               Dev,
		ProductName:       "StonkAgents Dev",
		InstallDirName:    "StonkAgents Dev",
		DataDirName:       ".stonkagents-dev",
		DaemonService:     "stonkagents-daemon-dev",
		ControllerService: "stonkagents-controller-dev",
		DaemonDisplay:     "StonkAgents Dev Daemon",
		ControllerDisplay: "StonkAgents Dev Controller",
		FirewallRule:      "StonkAgents Dev Agent",
		DaemonPort:        7861,
		ControllerPort:    7860,
		GatewayPort:       19001,
		CLIProfile:        "dev",
		MsiUpgradeCode:    "7F4C9E53-A06D-4B8C-9E3F-5D7A9B2C4E63",
		BundleUpgradeCode: "8A5DAF64-B17E-4C9D-AF40-6E8BAC3D5F74",
	},
}

// Normalize maps the accepted spellings ("dev", "staging", "prod", "production",
// "") onto dev, stg or prd. Unknown values fall back to prd, so a missing or
// garbled setting never renames a production install.
func Normalize(env string) string {
	switch strings.ToLower(strings.TrimSpace(env)) {
	case Dev, "development":
		return Dev
	case Stg, "staging", "stage":
		return Stg
	default:
		return Prd
	}
}

// ForEnv returns the profile for an environment name (see Normalize).
func ForEnv(env string) Profile {
	return profiles[Normalize(env)]
}

// Current returns the profile named by STONKAGENTS_ENV, production when unset.
func Current() Profile {
	return ForEnv(os.Getenv(EnvVar))
}

// FromTrackerURL derives the environment from the tracker host the build is
// pointed at: tracker.dev.* is dev, tracker.stg.* is stg, anything else prd.
func FromTrackerURL(trackerURL string) string {
	u, err := url.Parse(strings.TrimSpace(trackerURL))
	if err != nil || u.Host == "" {
		return Prd
	}
	host := strings.ToLower(u.Hostname())
	switch {
	case strings.HasPrefix(host, "tracker.dev."):
		return Dev
	case strings.HasPrefix(host, "tracker.stg."):
		return Stg
	}
	return Prd
}

// DaemonURL is the daemon's loopback base URL.
func (p Profile) DaemonURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d", p.DaemonPort)
}

// ControllerURL is the controller's loopback base URL.
func (p Profile) ControllerURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d", p.ControllerPort)
}

// GatewayURL is the CLI gateway's loopback base URL.
func (p Profile) GatewayURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d", p.GatewayPort)
}

// ControllerAddr is the controller's listen address.
func (p Profile) ControllerAddr() string {
	return fmt.Sprintf("127.0.0.1:%d", p.ControllerPort)
}

// DaemonHealthURL is what the controller polls.
func (p Profile) DaemonHealthURL() string {
	return fmt.Sprintf("http://localhost:%d/health", p.DaemonPort)
}

// GatewayTaskName is the scheduled task the CLI registers for this profile
// ("OpenClaw Gateway" for the default profile, "OpenClaw Gateway (dev)" otherwise).
func (p Profile) GatewayTaskName() string {
	if p.CLIProfile == "" {
		return "OpenClaw Gateway"
	}
	return "OpenClaw Gateway (" + p.CLIProfile + ")"
}

// CLIStateDirName is the CLI's state folder under the user profile
// (".openclaw" for the default profile, ".openclaw-dev" otherwise).
func (p Profile) CLIStateDirName() string {
	if p.CLIProfile == "" {
		return ".openclaw"
	}
	return ".openclaw-" + p.CLIProfile
}

// CLIEnv is the environment the CLI needs to act inside this profile:
// OPENCLAW_PROFILE and OPENCLAW_GATEWAY_PORT, plus STONKAGENTS_ENV. Empty
// for production, whose CLI keeps its defaults.
func (p Profile) CLIEnv() []string {
	if p.CLIProfile == "" {
		return nil
	}
	return []string{
		EnvVar + "=" + p.Env,
		"OPENCLAW_PROFILE=" + p.CLIProfile,
		fmt.Sprintf("OPENCLAW_GATEWAY_PORT=%d", p.GatewayPort),
	}
}

// LoopbackOrigins are the browser origins of this environment's own
// daemon, for CORS allowlists.
func (p Profile) LoopbackOrigins() []string {
	return []string{
		fmt.Sprintf("http://localhost:%d", p.DaemonPort),
		fmt.Sprintf("http://127.0.0.1:%d", p.DaemonPort),
	}
}
