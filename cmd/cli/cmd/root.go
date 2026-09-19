// Package: cmd/cli/cmd
// Feature: F-009 (CLI Tool)
// Story: US-009-01 (CLI Tool Scaffolding)
// Purpose: Root command and CLI initialization

package cmd

import (
	"os"
	"path/filepath"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/stonkagents/agent/internal/config"
)

var (
	cfgFile string
	verbose bool
	version string = "dev" // Set via SetVersion() from main package
)

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:   "stonkagents-cli",
	Short: "StonkAgents - P2P file sharing for AI agents",
	Long: `StonkAgents is a decentralized, peer-to-peer protocol for AI agents
to share data, context, and compute. Think "BitTorrent for AI Agents."

Commands:
  stonkagents-cli init      - Initialize StonkAgents with keypair generation
  stonkagents-cli share     - Share a file as .at-raw or .at-vec asset
  stonkagents-cli search    - Search for assets using semantic search
  stonkagents-cli download  - Download an asset by CID
  stonkagents-cli status    - Show daemon status and network stats
  stonkagents-cli doctor    - Troubleshoot connectivity and configuration issues

Visit https://docs.stonkagents.com for full documentation.`,
}

// SetVersion sets the CLI version (called from main package with ldflags-injected version)
func SetVersion(v string) {
	version = v
	rootCmd.Version = v
}

// Execute adds all child commands to the root command and sets flags appropriately.
// This is called by main.main(). It only needs to happen once to the rootCmd.
func Execute() {
	err := rootCmd.Execute()
	if err != nil {
		os.Exit(1)
	}
}

func init() {
	cobra.OnInitialize(initConfig)

	// Global flags
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default is $HOME/.stonkagents/config.yaml)")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "verbose output")

	// Bind flags to viper
	viper.BindPFlag("verbose", rootCmd.PersistentFlags().Lookup("verbose"))
}

// initConfig reads in config file and ENV variables if set.
func initConfig() {
	if cfgFile != "" {
		// Use config file from the flag
		viper.SetConfigFile(cfgFile)
	} else {
		// Find home directory
		home, err := os.UserHomeDir()
		if err != nil {
			color.Red("Error: %v", err)
			os.Exit(1)
		}

		// Search config in home directory with name ".stonkagents/config" (without extension)
		configPath := filepath.Join(home, config.DefaultDataDirName())
		viper.AddConfigPath(configPath)
		viper.SetConfigType("yaml")
		viper.SetConfigName("config")
	}

	viper.AutomaticEnv() // read in environment variables that match

	// If a config file is found, read it in
	if err := viper.ReadInConfig(); err == nil {
		if verbose {
			color.Green("Using config file: %s", viper.ConfigFileUsed())
		}
	}
}
