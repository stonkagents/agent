// Package: cmd/cli/cmd
// Feature: F-009 (CLI Tool)
// Story: US-009-02 (Basic CLI Commands)
// Purpose: Doctor command for troubleshooting. Runs the same setup checks the
// portal's Permissions step shows: through the local daemon's setup surface
// (GET /api/v1/setup/status) when it is up, otherwise locally.

package cmd

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/stonkagents/agent/internal/config"
	"github.com/stonkagents/agent/internal/setup"
)

// Daemon and controller endpoints; vars so tests can point them elsewhere.
var (
	doctorDaemonURL     = "http://127.0.0.1:7841"
	doctorControllerURL = "http://127.0.0.1:7840"
)

// doctorStatusTimeout must exceed the daemon's own status budget (8s: the
// controller probe, netsh and sc run inside it) so a slow but healthy daemon
// is never reported as unreachable.
const (
	doctorStatusTimeout = 10 * time.Second
	doctorFixTimeout    = 20 * time.Second
)

// doctorCmd represents the doctor command
var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Troubleshoot connectivity and configuration issues",
	Long: `Runs diagnostic checks to troubleshoot common issues:
  - Config file validity (~/.stonkagents/config.yaml) and keypair
  - The agent setup checks the portal shows (service, controller, firewall,
    p2p, tracker, storage, bandwidth, autostart, origin), read from the
    running daemon (localhost:7841) or evaluated locally when it is down

Examples:
  stonkagents-cli doctor
  stonkagents-cli doctor --fix    # Apply the daemon's fixes for failing checks (daemon must be running)`,
	Run: func(cmd *cobra.Command, args []string) {
		fix, _ := cmd.Flags().GetBool("fix")
		os.Exit(runDoctor(fix))
	},
}

// runDoctor prints every check and returns the process exit code (1 when any
// check is failed; missing checks are warnings).
func runDoctor(fix bool) int {
	color.Cyan("=== StonkAgents Doctor ===\n")
	hasErrors := false

	fmt.Println("Checking config file...")
	cfg := checkConfigAndKeys(&hasErrors)

	fmt.Println("\nChecking agent setup...")
	checks, source := loadSetupChecks(cfg)
	if source == "daemon" {
		fmt.Println("  (from the running daemon at " + doctorDaemonURL + ")")
	} else {
		color.Yellow("  Daemon not reachable at %s; evaluating checks locally", doctorDaemonURL)
	}
	for _, c := range checks {
		printCheck(c)
		if c.Status == setup.StatusFailed {
			hasErrors = true
		}
	}

	if fix {
		fmt.Println("\nApplying fixes...")
		if source != "daemon" {
			color.Red("  ✗ --fix needs the daemon running (fixes are applied through it)")
			hasErrors = true
		} else {
			for _, c := range checks {
				if c.Status == setup.StatusOK || !setup.FixableIDs[c.ID] {
					continue
				}
				result, err := applySetupFix(c.ID)
				if err != nil {
					color.Red("  ✗ %-10s %v", c.ID, err)
					hasErrors = true
					continue
				}
				printCheck(result)
			}
		}
	}

	fmt.Println("\n" + color.CyanString("=== Summary ==="))
	if hasErrors {
		color.Red("Some checks failed. Please fix the issues above.")
		fmt.Println("\nCommon fixes:")
		fmt.Println("  - Run 'stonkagents-cli init' to create/reset config")
		fmt.Println("  - Check file permissions on ~/.stonkagents/")
		fmt.Println("  - Start the StonkAgents services, then run 'stonkagents-cli doctor --fix'")
		return 1
	}
	color.Green("All critical checks passed! ✓")
	return 0
}

// checkConfigAndKeys validates the config file and keypair; returns the config
// (nil when missing or invalid).
func checkConfigAndKeys(hasErrors *bool) *config.Config {
	if !config.Exists() {
		color.Red("  ✗ Config file not found")
		fmt.Println("    Run 'stonkagents-cli init' to create config file")
		*hasErrors = true
		return nil
	}
	cfg, err := config.Load()
	if err != nil {
		color.Red("  ✗ Config file invalid: %v", err)
		*hasErrors = true
		return nil
	}
	color.Green("  ✓ Config file exists and is valid")

	fmt.Println("\nChecking keypair...")
	publicKey, err1 := base64.StdEncoding.DecodeString(cfg.PublicKey)
	privateKey, err2 := base64.StdEncoding.DecodeString(cfg.PrivateKey)
	switch {
	case cfg.PrivateKey == "":
		color.Yellow("  ! Private key not set (STONKAGENTS_PRIVATE_KEY / secrets.env); the daemon derives identity from it")
	case err1 != nil || err2 != nil:
		color.Red("  ✗ Keypair base64 decoding failed")
		*hasErrors = true
	case len(publicKey) != 32 && cfg.PublicKey != "":
		color.Red("  ✗ Invalid public key length: %d (expected 32)", len(publicKey))
		*hasErrors = true
	case len(privateKey) != 64:
		color.Red("  ✗ Invalid private key length: %d (expected 64)", len(privateKey))
		*hasErrors = true
	default:
		color.Green("  ✓ Ed25519 keypair valid")
	}

	fmt.Println("\nChecking Peer ID...")
	switch {
	case cfg.PeerID == "":
		color.Yellow("  ! Peer ID empty in config (derived from the private key at daemon start)")
	case len(cfg.PeerID) < 10:
		color.Red("  ✗ Peer ID too short: %s", cfg.PeerID)
		*hasErrors = true
	default:
		color.Green("  ✓ Peer ID: %s", cfg.PeerID)
	}
	return cfg
}

// loadSetupChecks returns the setup checks from the daemon when it answers,
// otherwise the locally evaluated equivalents. source is "daemon" or "local".
func loadSetupChecks(cfg *config.Config) ([]setup.Check, string) {
	if checks, ok := fetchDaemonSetupStatus(); ok {
		return checks, "daemon"
	}
	return localSetupChecks(cfg), "local"
}

func fetchDaemonSetupStatus() ([]setup.Check, bool) {
	client := &http.Client{Timeout: doctorStatusTimeout}
	resp, err := client.Get(doctorDaemonURL + "/api/v1/setup/status")
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false
	}
	var body setup.Response
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || len(body.Checks) == 0 {
		return nil, false
	}
	return body.Checks, true
}

// applySetupFix posts the daemon fix for id and returns the re-evaluated check.
// The request carries the mutation headers the setup surface requires.
func applySetupFix(id string) (setup.Check, error) {
	req, err := http.NewRequest(http.MethodPost, doctorDaemonURL+"/api/v1/setup/"+id, strings.NewReader("{}"))
	if err != nil {
		return setup.Check{}, err
	}
	setup.SetMutationHeaders(req.Header)
	client := &http.Client{Timeout: doctorFixTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return setup.Check{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		var e struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&e)
		if e.Error.Message == "" {
			e.Error.Message = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return setup.Check{}, fmt.Errorf("%s (%s)", e.Error.Message, e.Error.Code)
	}
	var body setup.FixResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return setup.Check{}, err
	}
	return body.Check, nil
}

// localSetupChecks evaluates what can be known without the daemon: the
// daemon-state checks (service, p2p) are reported missing, the rest run the
// same code the daemon uses.
func localSetupChecks(cfg *config.Config) []setup.Check {
	dataDir, trackerURL := "", ""
	var up, down float64
	if cfg != nil {
		dataDir, trackerURL, up, down = cfg.DataDir, cfg.TrackerURL, cfg.UploadCapMbps, cfg.DownloadCapMbps
	}
	tracker := setup.Missing(setup.IDTracker, "daemon not running; registration state unknown", map[string]any{"trackerUrl": trackerURL})
	if dataDir != "" {
		if _, err := os.Stat(filepath.Join(dataDir, "tracker_api_key")); err == nil {
			tracker.Detail["apiKeyPresent"] = true
		} else {
			tracker.Message = "not registered with the tracker yet"
			tracker.Detail["apiKeyPresent"] = false
		}
	}
	return []setup.Check{
		setup.Missing(setup.IDService, "daemon not running on "+doctorDaemonURL, nil),
		setup.ControllerCheck(doctorControllerURL),
		setup.Firewall(localDaemonExePath()),
		setup.Missing(setup.IDP2P, "daemon not running", nil),
		tracker,
		setup.StorageCheck(dataDir),
		setup.BandwidthCheck(up, down),
		setup.Autostart(),
		setup.OK(setup.IDOrigin, map[string]any{"origin": nil}),
	}
}

// localDaemonExePath guesses the installed daemon binary for the firewall
// check: next to this CLI, else the default Windows install directory.
func localDaemonExePath() string {
	if p := setup.DefaultDaemonExePath(); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	if runtime.GOOS == "windows" {
		if pf := os.Getenv("ProgramFiles"); pf != "" {
			return filepath.Join(pf, "StonkAgents", "stonkagents-daemon.exe")
		}
	}
	return setup.DefaultDaemonExePath()
}

func printCheck(c setup.Check) {
	msg := ""
	if c.Message != "" {
		msg = ": " + c.Message
	}
	switch c.Status {
	case setup.StatusOK:
		color.Green("  ✓ %-10s ok%s", c.ID, msg)
	case setup.StatusMissing:
		color.Yellow("  ! %-10s missing%s", c.ID, msg)
	default:
		color.Red("  ✗ %-10s failed%s", c.ID, msg)
	}
}

func init() {
	rootCmd.AddCommand(doctorCmd)

	// Flags for doctor command
	doctorCmd.Flags().Bool("fix", false, "Apply the daemon's fixes for failing checks (firewall, autostart, storage, bandwidth, origin)")
}
