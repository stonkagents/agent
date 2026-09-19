// Package: cmd/cli
// Feature: F-009 (CLI Tool)
// Story: US-009-01 (CLI Tool Scaffolding)
// Purpose: CLI entry point for StonkAgents

package main

import "github.com/stonkagents/agent/cmd/cli/cmd"

// Version is the build version, can be overridden via ldflags:
// go build -ldflags "-X main.Version=1.0.0" ./cmd/cli
var Version = "dev"

func main() {
	cmd.SetVersion(Version)
	cmd.Execute()
}
