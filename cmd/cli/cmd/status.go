// Package: cmd/cli/cmd
// Feature: F-009 (CLI Tool)
// Story: US-009-02 (Basic CLI Commands)
// Purpose: Status command for daemon and network stats

package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/stonkagents/agent/internal/config"
)

// statusCmd represents the status command
var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show daemon status and network stats",
	Long: `Displays StonkAgents daemon status, peer count, reputation score,
and upload/download statistics.

Examples:
  stonkagents-cli status
  stonkagents-cli status --json    # Output as JSON for scripting`,
	Run: func(cmd *cobra.Command, args []string) {
		asJSON, _ := cmd.Flags().GetBool("json")

		// Load config to get peer ID
		cfg, err := config.Load()
		if err != nil {
			color.Red("Error: %v", err)
			fmt.Println("\nRun 'stonkagents-cli init' first to initialize StonkAgents.")
			os.Exit(1)
		}

		// MVP: Simulate daemon status (production would connect to localhost:7841)
		status := map[string]interface{}{
			"daemon":       "Not Running (MVP)",
			"version":      "1.0.0 (Genesis)",
			"peer_id":      cfg.PeerID,
			"peers":        0,
			"reputation":   0.0,
			"uploaded":     "0 B",
			"downloaded":   "0 B",
			"shared_files": 0,
			"daemon_host":  cfg.DaemonHost,
			"daemon_port":  cfg.DaemonPort,
		}

		if asJSON {
			jsonData, _ := json.MarshalIndent(status, "", "  ")
			fmt.Println(string(jsonData))
			return
		}

		color.Cyan("=== StonkAgents Status ===\n")

		// Display formatted status
		fmt.Printf("Daemon:         %s\n", status["daemon"])
		fmt.Printf("Version:        %s\n", status["version"])
		fmt.Printf("Peer ID:        %s\n", cfg.PeerID)
		fmt.Printf("Config:         %s:%d\n", cfg.DaemonHost, cfg.DaemonPort)
		fmt.Printf("Data Dir:       %s\n", cfg.DataDir)
		fmt.Printf("Sharing:        platform-agnostic\n")

		fmt.Println("\n" + color.YellowString("Network Stats:"))
		fmt.Printf("  Peers:        %v\n", status["peers"])
		fmt.Printf("  Reputation:   %.2f\n", status["reputation"])
		fmt.Printf("  Uploaded:     %s\n", status["uploaded"])
		fmt.Printf("  Downloaded:   %s\n", status["downloaded"])
		fmt.Printf("  Shared files: %v\n", status["shared_files"])

		color.Yellow("\n[MVP Note] Daemon not yet implemented")
		fmt.Println("Production version will connect to daemon at localhost:7841")
		fmt.Println("and display real-time P2P network statistics.")
	},
}

func init() {
	rootCmd.AddCommand(statusCmd)

	// Flags for status command
	statusCmd.Flags().Bool("json", false, "Output as JSON")
}
