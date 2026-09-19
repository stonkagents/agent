// Package: cmd/cli/cmd
// Feature: F-009 (CLI Tool)
// Story: US-009-02 (Basic CLI Commands)
// Purpose: Init command for keypair generation and onboarding

package cmd

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"os"

	"github.com/AlecAivazis/survey/v2"
	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/stonkagents/agent/internal/config"
	crypto "github.com/stonkagents/agent/pkg/cryptography"
)

// initCmd represents the init command
var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize StonkAgents with keypair generation",
	Long: `Interactive onboarding wizard that:
- Generates Ed25519 keypair for signing assets
- Creates ~/.stonkagents/config.yaml
- Sets up optional configuration

Example:
  stonkagents-cli init              # Interactive wizard
  stonkagents-cli init --force      # Overwrite existing config`,
	Run: func(cmd *cobra.Command, args []string) {
		force, _ := cmd.Flags().GetBool("force")

		color.Cyan("=== StonkAgents Initialization ===\n")

		// Check if config already exists
		if config.Exists() && !force {
			color.Red("Error: Config file already exists at ~/.stonkagents/config.yaml")
			fmt.Println("\nUse 'stonkagents-cli init --force' to overwrite, or delete the file manually.")
			os.Exit(1)
		}

		// Generate Ed25519 keypair
		fmt.Println("Generating Ed25519 keypair...")
		publicKey, privateKey, err := crypto.GenerateKeypair()
		if err != nil {
			color.Red("Error generating keypair: %v", err)
			os.Exit(1)
		}
		color.Green("  ✓ Keypair generated (32-byte public key, 64-byte private key)")

		// Generate PeerID from public key (SHA-256 hash, base64 encoded)
		peerID := generatePeerID(publicKey)

		// Get default config
		cfg := config.DefaultConfig()
		cfg.PeerID = peerID
		cfg.DisplayName = config.DefaultDisplayName(peerID) // every agent has a name; rename later from the portal
		cfg.PublicKey = base64.StdEncoding.EncodeToString(publicKey)
		// SECURITY: Private key is NEVER stored in config - only in environment variable
		// (cfg.PrivateKey removed for security - must be set via STONKAGENTS_PRIVATE_KEY env var)

		// Interactive prompts for optional configuration
		fmt.Println()
		var customDataDir bool
		prompt := &survey.Confirm{
			Message: "Customize data directory? (default: ~/.stonkagents/data)",
			Default: false,
		}
		survey.AskOne(prompt, &customDataDir)

		if customDataDir {
			var dataDir string
			dirPrompt := &survey.Input{
				Message: "Enter custom data directory:",
				Default: cfg.DataDir,
			}
			survey.AskOne(dirPrompt, &dataDir)
			cfg.DataDir = dataDir
		}

		// Save config
		fmt.Println("\nCreating config file...")
		if err := cfg.Save(); err != nil {
			color.Red("Error saving config: %v", err)
			os.Exit(1)
		}

		configPath, _ := config.ConfigPath()
		color.Green("  ✓ Config file created at %s", configPath)

		// Display summary
		fmt.Println("\n" + color.CyanString("=== Configuration Summary ==="))
		fmt.Printf("Peer ID:       %s\n", peerID)
		fmt.Printf("Display name:  %s\n", cfg.DisplayName)
		fmt.Printf("Public Key:    %s...\n", cfg.PublicKey[:32])
		fmt.Printf("Data Dir:      %s\n", cfg.DataDir)
		fmt.Printf("Daemon:        %s:%d\n", cfg.DaemonHost, cfg.DaemonPort)
		fmt.Printf("Sharing:       platform-agnostic (via achievements API)\n")

		// SECURITY: Display private key for environment variable setup
		privateKeyEncoded := base64.StdEncoding.EncodeToString(privateKey)
		fmt.Println("\n" + color.RedString("⚠ SECURITY: Private Key (keep secret!)"))
		fmt.Println(color.YellowString("Your private key has been generated but is NOT stored in config.yaml for security."))
		fmt.Println(color.YellowString("You must set it as an environment variable before using the daemon:\n"))
		fmt.Printf("export STONKAGENTS_PRIVATE_KEY=\"%s\"\n", privateKeyEncoded)
		fmt.Println(color.YellowString("\nAdd this to your ~/.bashrc, ~/.zshrc, or .env file."))

		color.Green("\n✓ Initialization complete!")

		fmt.Println("\n" + color.YellowString("Next steps:"))
		fmt.Println("  1. Set STONKAGENTS_PRIVATE_KEY environment variable (see above)")
		fmt.Println("  2. Run 'stonkagents-cli status' to check daemon connectivity")
		fmt.Println("  3. Run 'stonkagents-cli share <file>' to share your first file")
		fmt.Println("  4. Run 'stonkagents-cli search <query>' to discover assets")
	},
}

// generatePeerID creates a peer ID from a public key
// Format: "12D3Koo" + base64(SHA256(publicKey)[:20])
// This is a simplified version inspired by libp2p peer IDs
func generatePeerID(publicKey []byte) string {
	hash := sha256.Sum256(publicKey)
	encoded := base64.RawURLEncoding.EncodeToString(hash[:20])
	return "12D3Koo" + encoded
}

func init() {
	rootCmd.AddCommand(initCmd)

	// Flags for init command
	initCmd.Flags().Bool("force", false, "Overwrite existing config")
}
