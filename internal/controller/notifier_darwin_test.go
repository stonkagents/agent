//go:build darwin

// Package: internal/controller
// Feature: F-025 (Auto-Update System)
// Story: US-025-07 (macOS Install Sequence)
// Purpose: TDD tests for DarwinNotifier — path resolution, argument passing,
//          error handling. Uses injectable command runner (same pattern as runCodesign).

package controller

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// === TD-041: notifyToolPath returns error when tool not found ===

func TestNotifyToolPath_FindsInLocalBin(t *testing.T) {
	// Create a fake stonkagents-notify in a temp dir simulating ~/.local/bin/
	tmpHome := t.TempDir()
	binDir := filepath.Join(tmpHome, ".local", "bin")
	os.MkdirAll(binDir, 0755)
	toolPath := filepath.Join(binDir, "stonkagents-notify")
	os.WriteFile(toolPath, []byte("#!/bin/sh\n"), 0755)

	got, err := notifyToolPathFrom(tmpHome, "/nonexistent/controller")
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if got != toolPath {
		t.Errorf("expected %s, got %s", toolPath, got)
	}
}

func TestNotifyToolPath_FindsAlongsideController(t *testing.T) {
	tmpDir := t.TempDir()
	toolPath := filepath.Join(tmpDir, "stonkagents-notify")
	os.WriteFile(toolPath, []byte("#!/bin/sh\n"), 0755)

	got, err := notifyToolPathFrom("/nonexistent/home", tmpDir)
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if got != toolPath {
		t.Errorf("expected %s, got %s", toolPath, got)
	}
}

func TestNotifyToolPath_ReturnsErrorWhenNotFound(t *testing.T) {
	_, err := notifyToolPathFrom("/nonexistent/home", "/nonexistent/controller")
	if err == nil {
		t.Fatal("expected error when tool not found, got nil")
	}
	if !strings.Contains(err.Error(), "stonkagents-notify not found") {
		t.Errorf("expected 'not found' error, got: %v", err)
	}
}

// === TD-042: DarwinNotifier send with injectable command runner ===

// mockNotifierDeps overrides findNotifyTool and runNotifyCmd for isolated testing.
// Returns a cleanup function to restore originals.
func mockNotifierDeps(t *testing.T) func() {
	t.Helper()
	origFinder := findNotifyTool
	origRunner := runNotifyCmd

	findNotifyTool = func() (string, error) {
		return "/mock/stonkagents-notify", nil
	}
	runNotifyCmd = func(tool string, args ...string) ([]byte, error) {
		return nil, nil
	}

	return func() {
		findNotifyTool = origFinder
		runNotifyCmd = origRunner
	}
}

func TestDarwinNotifier_SendPassesCorrectArgs(t *testing.T) {
	cleanup := mockNotifierDeps(t)
	defer cleanup()

	var gotArgs []string
	runNotifyCmd = func(tool string, args ...string) ([]byte, error) {
		gotArgs = append([]string{tool}, args...)
		return nil, nil
	}

	n := &DarwinNotifier{logger: nil}
	err := n.send("Title", "Subtitle", "Body text")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(gotArgs) != 4 {
		t.Fatalf("expected 4 args, got %d: %v", len(gotArgs), gotArgs)
	}
	// First arg is the tool path — we don't check exact path, just the other 3
	if gotArgs[1] != "Title" || gotArgs[2] != "Subtitle" || gotArgs[3] != "Body text" {
		t.Errorf("wrong args: %v", gotArgs)
	}
}

func TestDarwinNotifier_SendReturnsErrorOnToolFailure(t *testing.T) {
	cleanup := mockNotifierDeps(t)
	defer cleanup()

	runNotifyCmd = func(tool string, args ...string) ([]byte, error) {
		return []byte("some stderr"), fmt.Errorf("exit status 1")
	}

	n := &DarwinNotifier{logger: nil}
	err := n.send("Title", "Sub", "Body")
	if err == nil {
		t.Fatal("expected error from failed tool, got nil")
	}
}

func TestDarwinNotifier_NotifyUpdateAvailable_FormatsCorrectly(t *testing.T) {
	cleanup := mockNotifierDeps(t)
	defer cleanup()

	var gotArgs []string
	runNotifyCmd = func(tool string, args ...string) ([]byte, error) {
		gotArgs = args
		return nil, nil
	}

	n := &DarwinNotifier{logger: nil}
	n.NotifyUpdateAvailable("0.3.0", "Bug fixes and improvements")

	if len(gotArgs) != 3 {
		t.Fatalf("expected 3 args, got %d: %v", len(gotArgs), gotArgs)
	}
	if gotArgs[0] != "StonkAgents Update" {
		t.Errorf("wrong title: %q", gotArgs[0])
	}
	if gotArgs[1] != "v0.3.0 available" {
		t.Errorf("wrong subtitle: %q", gotArgs[1])
	}
}

func TestDarwinNotifier_NotifyUpdateFailed_FormatsCorrectly(t *testing.T) {
	cleanup := mockNotifierDeps(t)
	defer cleanup()

	var gotArgs []string
	runNotifyCmd = func(tool string, args ...string) ([]byte, error) {
		gotArgs = args
		return nil, nil
	}

	n := &DarwinNotifier{logger: nil}
	n.NotifyUpdateFailed("0.3.0", "codesign check failed")

	if len(gotArgs) != 3 {
		t.Fatalf("expected 3 args, got %d: %v", len(gotArgs), gotArgs)
	}
	if gotArgs[0] != "StonkAgents Update Failed" {
		t.Errorf("wrong title: %q", gotArgs[0])
	}
	if gotArgs[2] != "codesign check failed" {
		t.Errorf("wrong body: %q", gotArgs[2])
	}
}
