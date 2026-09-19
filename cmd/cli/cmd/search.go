// Package: cmd/cli/cmd
// Feature: F-009 (CLI Tool)
// Story: US-009-02 (Basic CLI Commands)
// Purpose: Search command for semantic asset discovery

package cmd

import (
	"fmt"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
)

// searchResult represents a search result entry
type searchResult struct {
	CID         string
	Filename    string
	Description string
	Similarity  float64
	AssetType   string
}

// searchCmd represents the search command
var searchCmd = &cobra.Command{
	Use:   "search <query>",
	Short: "Search for assets using semantic search",
	Long: `Semantic search using vector embeddings to find relevant assets.

Examples:
  stonkagents-cli search "image of a cat"
  stonkagents-cli search "financial report Q4 2026" --limit 20
  stonkagents-cli search "CLIP embeddings" --type at-vec`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		query := args[0]
		limit, _ := cmd.Flags().GetInt("limit")
		assetType, _ := cmd.Flags().GetString("type")

		color.Cyan("=== Searching for Assets ===")
		fmt.Printf("Query: %s\n", query)
		if assetType != "" {
			fmt.Printf("Type:  %s\n", assetType)
		}
		fmt.Printf("Limit: %d\n\n", limit)

		// MVP: Simulate search results
		// Production would connect to daemon and perform semantic vector search
		results := generateMockSearchResults(query, assetType, limit)

		if len(results) == 0 {
			color.Yellow("No results found.")
			fmt.Println("\n[MVP Note] Semantic search requires daemon with vector database.")
			return
		}

		// Display results in formatted table
		displaySearchResults(results)

		color.Green("\nFound %d result(s)", len(results))

		fmt.Println("\n" + color.YellowString("[MVP Note]"))
		fmt.Println("Production version will:")
		fmt.Println("  - Connect to daemon for semantic search")
		fmt.Println("  - Use vector embeddings for similarity matching")
		fmt.Println("  - Return real assets from P2P network")
	},
}

// generateMockSearchResults creates simulated search results for MVP
func generateMockSearchResults(query, assetType string, limit int) []searchResult {
	// MVP: Return mock data
	mockResults := []searchResult{
		{
			CID:         "bafkreibta6xflzzucvm2kprttjkx4uiypwka26fyzcsedrihxwdg6le5fy",
			Filename:    "example-document.pdf",
			Description: "Sample document matching query",
			Similarity:  0.92,
			AssetType:   "at-raw",
		},
		{
			CID:         "bafkreifzjut3te2nhyekklss27nh3k72ysco7y32koao5eei66wof36n5e",
			Filename:    "clip-embedding.vec",
			Description: "CLIP ViT-B/32 embedding",
			Similarity:  0.85,
			AssetType:   "at-vec",
		},
	}

	// Filter by asset type if specified
	if assetType != "" {
		var filtered []searchResult
		for _, r := range mockResults {
			if r.AssetType == assetType {
				filtered = append(filtered, r)
			}
		}
		mockResults = filtered
	}

	// Apply limit
	if len(mockResults) > limit {
		mockResults = mockResults[:limit]
	}

	return mockResults
}

// displaySearchResults formats and prints search results as a table
func displaySearchResults(results []searchResult) {
	// Table header
	fmt.Printf("%-60s | %-25s | %-10s | %-8s\n", "CID", "Filename", "Type", "Score")
	fmt.Println("------------------------------------------------------------+---------------------------+------------+----------")

	// Table rows
	for _, r := range results {
		cidDisplay := r.CID
		if len(cidDisplay) > 60 {
			cidDisplay = cidDisplay[:57] + "..."
		}

		filenameDisplay := r.Filename
		if len(filenameDisplay) > 25 {
			filenameDisplay = filenameDisplay[:22] + "..."
		}

		fmt.Printf("%-60s | %-25s | %-10s | %.2f\n",
			cidDisplay, filenameDisplay, r.AssetType, r.Similarity)
	}
}

func init() {
	rootCmd.AddCommand(searchCmd)

	// Flags for search command
	searchCmd.Flags().Int("limit", 10, "Maximum number of results")
	searchCmd.Flags().String("type", "", "Filter by asset type (at-raw, at-vec)")
}
