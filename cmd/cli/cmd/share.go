// Package: cmd/cli/cmd
// Feature: F-009 (CLI Tool)
// Story: US-009-02 (Basic CLI Commands)
// Purpose: Share command for uploading assets

package cmd

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/stonkagents/agent/internal/config"
	"github.com/stonkagents/agent/internal/sharing"
	"github.com/stonkagents/agent/pkg/assets"
	"github.com/stonkagents/agent/pkg/manifest"
)

// shareCmd represents the share command
var shareCmd = &cobra.Command{
	Use:   "share <file>",
	Short: "Share a plain-text knowledge file as an .at-raw asset",
	Long: `Uploads a file to the StonkAgents network.

Agents share knowledge, so only plain-text files can be shared:
.txt, .md, .json, .csv, .yaml, code files and similar. Binaries, images,
archives, PDFs, office documents and .env files are refused.

Examples:
  stonkagents-cli share notes.md              # Share a Markdown note
  stonkagents-cli share dataset.csv           # Share a CSV table
  stonkagents-cli share prompt.txt --public   # Share publicly with P2P distribution`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		filePath := args[0]
		description, _ := cmd.Flags().GetString("description")

		color.Cyan("=== Sharing File ===")
		fmt.Printf("File: %s\n", filePath)

		// Load config to get private key
		cfg, err := config.Load()
		if err != nil {
			color.Red("Error: %v", err)
			fmt.Println("\nRun 'stonkagents-cli init' first to initialize StonkAgents.")
			os.Exit(1)
		}

		// Read file
		data, err := os.ReadFile(filePath)
		if err != nil {
			color.Red("Error reading file: %v", err)
			os.Exit(1)
		}

		// Plain-text rule (same as the daemon's POST /api/v1/share)
		if err := sharing.CheckReader(filePath, bytes.NewReader(data)); err != nil {
			color.Red("Error: %s", sharing.RuleMessage)
			fmt.Printf("Reason: %v\n", err)
			os.Exit(1)
		}

		// Decode private key from config
		privateKey, err := base64.StdEncoding.DecodeString(cfg.PrivateKey)
		if err != nil {
			color.Red("Error decoding private key: %v", err)
			os.Exit(1)
		}

		// Auto-detect file type
		isVector := isVectorEmbedding(filePath, data)
		filename := filepath.Base(filePath)

		if isVector {
			// Share as .at-vec
			fmt.Println("\n✓ Detected vector embedding (.at-vec)")

			// Auto-detect dimensions
			dimensions, err := assets.DetectEmbeddingDimensions(data)
			if err != nil {
				color.Red("Error detecting embedding dimensions: %v", err)
				os.Exit(1)
			}

			// For MVP, use generic model name if not specified
			model := "generic/embedding"
			framework := "pytorch"

			manifest, err := assets.CreateAtVecAsset(model, framework, description, data, privateKey)
			if err != nil {
				color.Red("Error creating .at-vec manifest: %v", err)
				os.Exit(1)
			}

			displayVecManifest(filename, manifest, int(dimensions), len(data))

		} else {
			// Share as .at-raw
			fmt.Println("\n✓ Detected raw file (.at-raw)")

			manifest, err := assets.CreateAtRawAsset(filename, data, privateKey)
			if err != nil {
				color.Red("Error creating .at-raw manifest: %v", err)
				os.Exit(1)
			}

			displayRawManifest(manifest, len(data))
		}

		color.Green("\n✓ File shared successfully!")
		fmt.Println("\n" + color.YellowString("Next steps:"))
		fmt.Println("  1. Run 'stonkagents-cli status' to check network connectivity")
		fmt.Println("  2. Share the CID with others for download")
		fmt.Println("  3. Keep daemon running to seed the file")
	},
}

// isVectorEmbedding detects if a file is a vector embedding based on extension and size
func isVectorEmbedding(filePath string, data []byte) bool {
	ext := strings.ToLower(filepath.Ext(filePath))

	// Check extension first
	vectorExtensions := map[string]bool{
		".npy": true,
		".pt":  true,
		".bin": true,
		".vec": true,
	}

	if vectorExtensions[ext] {
		// Also verify size is divisible by 4 (float32 alignment)
		return len(data)%4 == 0
	}

	return false
}

// displayRawManifest prints .at-raw manifest details
func displayRawManifest(manifest *manifest.AtRawManifest, fileSize int) {
	fmt.Println("\n" + color.CyanString("=== Asset Manifest (.at-raw) ==="))
	fmt.Printf("CID:       %s\n", manifest.Cid)
	fmt.Printf("Filename:  %s\n", manifest.Filename)
	fmt.Printf("MIME Type: %s\n", manifest.MimeType)
	fmt.Printf("Size:      %d bytes\n", fileSize)
	fmt.Printf("Signature: %s...\n", base64.StdEncoding.EncodeToString(manifest.Signature)[:32])
}

// displayVecManifest prints .at-vec manifest details
func displayVecManifest(filename string, m *manifest.AtVecManifest, dimensions, fileSize int) {
	fmt.Println("\n" + color.CyanString("=== Asset Manifest (.at-vec) ==="))
	fmt.Printf("CID:        %s\n", m.Cid)
	fmt.Printf("Filename:   %s\n", filename)
	fmt.Printf("Model:      %s\n", m.Model)
	fmt.Printf("Framework:  %s\n", m.Framework)
	fmt.Printf("Dimensions: %d\n", dimensions)
	fmt.Printf("Size:       %d bytes\n", fileSize)
	fmt.Printf("Signature:  %s...\n", base64.StdEncoding.EncodeToString(m.Signature)[:32])
	if m.Description != "" {
		fmt.Printf("Description: %s\n", m.Description)
	}
}

func init() {
	rootCmd.AddCommand(shareCmd)

	// Flags for share command
	shareCmd.Flags().Bool("public", false, "Share publicly with P2P distribution")
	shareCmd.Flags().String("description", "", "Description of the asset")
}
