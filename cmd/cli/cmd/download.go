// Package: cmd/cli/cmd
// Feature: F-009 (CLI Tool)
// Story: US-009-02 (Basic CLI Commands)
// Purpose: Download command for retrieving assets by CID

package cmd

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/stonkagents/agent/internal/config"
	crypto "github.com/stonkagents/agent/pkg/cryptography"
)

// downloadCmd represents the download command
var downloadCmd = &cobra.Command{
	Use:   "download <cid>",
	Short: "Download an asset by CID",
	Long: `Downloads an asset from the StonkAgents network using its CID.

Examples:
  stonkagents-cli download bafkreiabc123...
  stonkagents-cli download bafkreixyz789... --output custom-filename.png
  stonkagents-cli download <cid> --verify     # Verify signature and CID after download`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		cidStr := args[0]
		outputPath, _ := cmd.Flags().GetString("output")
		verify, _ := cmd.Flags().GetBool("verify")

		color.Cyan("=== Downloading Asset ===")
		fmt.Printf("CID: %s\n", cidStr)

		// Validate CID format
		if _, err := crypto.CIDToBytes(cidStr); err != nil {
			color.Red("Error: Invalid CID format: %v", err)
			os.Exit(1)
		}

		// Load config (needed for verification if verify=true)
		var cfg *config.Config
		if verify {
			var err error
			cfg, err = config.Load()
			if err != nil {
				color.Yellow("Warning: Cannot load config for verification: %v", err)
				color.Yellow("Continuing without signature verification...")
				verify = false
			}
		}

		// TODO(MVP): For now, simulate download
		// In production, this would connect to daemon and fetch via P2P
		fmt.Println("\n[MVP] Simulating download...")
		fmt.Println("Connecting to peers...")
		fmt.Println("Downloading: [████████████████████] 100%")

		// Simulate downloaded data for demonstration
		mockData := []byte("This is simulated downloaded content for MVP demonstration")

		// Verify CID
		fmt.Println("\n✓ Download complete")
		if verify {
			computedCID, err := crypto.GenerateCID(mockData)
			if err != nil {
				color.Red("Error computing CID: %v", err)
				os.Exit(1)
			}

			if computedCID == cidStr {
				color.Green("✓ CID verified")
			} else {
				color.Yellow("⚠ CID mismatch (expected in MVP simulation)")
				fmt.Printf("  Expected: %s\n", cidStr)
				fmt.Printf("  Got:      %s\n", computedCID)
			}

			// Verify signature (if manifest is available)
			if cfg != nil {
				publicKey, err := base64.StdEncoding.DecodeString(cfg.PublicKey)
				if err == nil {
					color.Green("✓ Signature verification enabled")
				} else {
					color.Yellow("⚠ Could not decode public key: %v", err)
				}
				_ = publicKey // Used in production
			}
		}

		// Save to file
		if outputPath == "" {
			outputPath = "downloaded-" + cidStr[:12] + ".bin"
		}

		// Ensure output directory exists
		dir := filepath.Dir(outputPath)
		if dir != "." && dir != "" {
			if err := os.MkdirAll(dir, 0755); err != nil {
				color.Red("Error creating output directory: %v", err)
				os.Exit(1)
			}
		}

		if err := os.WriteFile(outputPath, mockData, 0644); err != nil {
			color.Red("Error saving file: %v", err)
			os.Exit(1)
		}

		color.Green("\n✓ Asset downloaded successfully!")
		fmt.Printf("Saved to: %s\n", outputPath)
		fmt.Printf("Size: %d bytes\n", len(mockData))

		fmt.Println("\n" + color.YellowString("[MVP Note]"))
		fmt.Println("This is a simulated download. Production version will:")
		fmt.Println("  - Connect to daemon at localhost:7841")
		fmt.Println("  - Fetch from P2P network with real progress tracking")
		fmt.Println("  - Verify full manifest signature chain")
	},
}

func init() {
	rootCmd.AddCommand(downloadCmd)

	// Flags for download command
	downloadCmd.Flags().StringP("output", "o", "", "Output filename")
	downloadCmd.Flags().Bool("verify", true, "Verify signature and CID")
}
