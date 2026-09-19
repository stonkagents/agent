// Package: internal/daemon
// Feature: F-027 (Gallery Page Completion)
// Story: US-027-07 (Fix Dead Buttons)
// Purpose: TDD tests for expanded manifest type detection

package daemon

import "testing"

func TestDetectManifestType_Vec(t *testing.T) {
	tests := []struct {
		filename string
	}{
		{"model.vec"},
		{"weights.npy"},
		{"checkpoint.pt"},
		{"data.bin"},
		{"UPPER.VEC"},
		{"Mixed.Npy"},
	}
	for _, tc := range tests {
		t.Run(tc.filename, func(t *testing.T) {
			got := detectManifestType(tc.filename)
			if got != "vec" {
				t.Errorf("detectManifestType(%q) = %q, want %q", tc.filename, got, "vec")
			}
		})
	}
}

func TestDetectManifestType_Traj(t *testing.T) {
	tests := []struct {
		filename string
	}{
		{"rollout.traj"},
		{"episode-42.traj"},
		{"DATA.TRAJ"},
	}
	for _, tc := range tests {
		t.Run(tc.filename, func(t *testing.T) {
			got := detectManifestType(tc.filename)
			if got != "traj" {
				t.Errorf("detectManifestType(%q) = %q, want %q", tc.filename, got, "traj")
			}
		})
	}
}

func TestDetectManifestType_ClawSkill(t *testing.T) {
	got := detectManifestType("chrome-devtools.claw-skill")
	if got != "claw-skill" {
		t.Errorf("detectManifestType(chrome-devtools.claw-skill) = %q, want %q", got, "claw-skill")
	}
}

func TestDetectManifestType_ClawPrompt(t *testing.T) {
	got := detectManifestType("effective-prompting.claw-prompt")
	if got != "claw-prompt" {
		t.Errorf("detectManifestType(effective-prompting.claw-prompt) = %q, want %q", got, "claw-prompt")
	}
}

func TestDetectManifestType_ClawMemory(t *testing.T) {
	got := detectManifestType("solana-reference.claw-memory")
	if got != "claw-memory" {
		t.Errorf("detectManifestType(solana-reference.claw-memory) = %q, want %q", got, "claw-memory")
	}
}

func TestDetectManifestType_ClawWorkflow(t *testing.T) {
	got := detectManifestType("first-agent-setup.claw-workflow")
	if got != "claw-workflow" {
		t.Errorf("detectManifestType(first-agent-setup.claw-workflow) = %q, want %q", got, "claw-workflow")
	}
}

func TestDetectManifestType_ClawContext(t *testing.T) {
	got := detectManifestType("defi-overview.claw-context")
	if got != "claw-context" {
		t.Errorf("detectManifestType(defi-overview.claw-context) = %q, want %q", got, "claw-context")
	}
}

func TestDetectManifestType_ClawTool(t *testing.T) {
	got := detectManifestType("sentry-mcp.claw-tool")
	if got != "claw-tool" {
		t.Errorf("detectManifestType(sentry-mcp.claw-tool) = %q, want %q", got, "claw-tool")
	}
}

func TestDetectManifestType_UnknownFallsBackToRaw(t *testing.T) {
	tests := []struct {
		filename string
	}{
		{"readme.md"},
		{"photo.png"},
		{"data.csv"},
		{"archive.tar.gz"},
		{"noextension"},
	}
	for _, tc := range tests {
		t.Run(tc.filename, func(t *testing.T) {
			got := detectManifestType(tc.filename)
			if got != "raw" {
				t.Errorf("detectManifestType(%q) = %q, want %q", tc.filename, got, "raw")
			}
		})
	}
}
