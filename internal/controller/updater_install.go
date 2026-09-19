// Package: internal/controller
// Feature: F-025 (Auto-Update System)
// Story: US-025-05 (Controller Update Engine)
// Purpose: Binary and config backup for rollback support. Copies existing
//          binaries and config to a backup directory before install.

package controller

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// binaryNames lists the binaries that need backup before update.
// Includes genkeys and stonkagents-notify — both shipped in DMG and update archives.
// stonkagents is the only required binary; others are optional (skip if absent).
var binaryNames = []string{"stonkagents", "stonkagents-controller", "genkeys", "stonkagents-notify"}

// archiveBinaryPath returns the path of binary name inside extractDir. Returns "" when it does not exist.
func archiveBinaryPath(extractDir, name string) string {
	for _, candidate := range []string{name} {
		if candidate == "" {
			continue
		}
		p := filepath.Join(extractDir, candidate)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// backupBinaries copies daemon and controller binaries from binDir to backupDir
// with a .bak suffix. Skips missing binaries (not an error — a fresh install
// may only have one binary).
func backupBinaries(binDir, backupDir string) error {
	for _, name := range binaryNames {
		src := filepath.Join(binDir, name)
		if _, err := os.Stat(src); os.IsNotExist(err) {
			continue // skip missing — not an error
		}
		dst := filepath.Join(backupDir, name+".bak")
		if err := copyFile(src, dst); err != nil {
			return fmt.Errorf("backup %s: %w", name, err)
		}
	}
	return nil
}

// backupConfig copies a config file to backupDir with a .bak suffix.
func backupConfig(configPath, backupDir string) error {
	name := filepath.Base(configPath)
	dst := filepath.Join(backupDir, name+".bak")
	if err := copyFile(configPath, dst); err != nil {
		return fmt.Errorf("backup config %s: %w", name, err)
	}
	return nil
}

// rollbackBinaries restores .bak files from backupDir to binDir.
// Skips missing backups (not all binaries may have been backed up).
func rollbackBinaries(backupDir, binDir string) error {
	for _, name := range binaryNames {
		src := filepath.Join(backupDir, name+".bak")
		if _, err := os.Stat(src); os.IsNotExist(err) {
			continue // skip missing backup
		}
		dst := filepath.Join(binDir, name)
		if err := copyFile(src, dst); err != nil {
			return fmt.Errorf("rollback %s: %w", name, err)
		}
	}
	return nil
}

// waitForHealthy polls the daemon health endpoint until it returns 200 OK
// or the configured timeout expires. Uses healthPollInterval between attempts.
func (u *Updater) waitForHealthy(ctx context.Context, healthURL string) error {
	timeout := u.healthCheckTimeout
	interval := u.healthPollInterval

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	client := &http.Client{Timeout: 5 * time.Second}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Try immediately, then on each tick
	for {
		if err := checkHealth(ctx, client, healthURL); err == nil {
			return nil
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("health check timed out after %s", timeout)
		case <-ticker.C:
			// next attempt
		}
	}
}

// checkHealth performs a single health check. Returns nil on 200 OK.
func checkHealth(ctx context.Context, client *http.Client, healthURL string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unhealthy: HTTP %d", resp.StatusCode)
	}
	return nil
}

// rollbackConfig restores config.yaml.bak from backupDir to its original path.
func rollbackConfig(backupDir, configPath string) error {
	name := filepath.Base(configPath)
	src := filepath.Join(backupDir, name+".bak")
	if _, err := os.Stat(src); os.IsNotExist(err) {
		return nil // no backup to restore
	}
	return copyFile(src, configPath)
}

// cleanupBackups removes .bak files from backupDir after successful update.
func cleanupBackups(backupDir string) error {
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".bak") {
			os.Remove(filepath.Join(backupDir, e.Name()))
		}
	}
	return nil
}

// cleanupStaleBackups removes .bak files from backupDir that are older than maxAge.
// Returns the number of files removed. Tolerates missing backupDir (no error).
func cleanupStaleBackups(backupDir string, maxAge time.Duration) (int, error) {
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}

	cutoff := time.Now().Add(-maxAge)
	removed := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".bak") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			if err := os.Remove(filepath.Join(backupDir, e.Name())); err == nil {
				removed++
			}
		}
	}
	return removed, nil
}

// copyFile copies src to dst, preserving file permissions.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	info, err := in.Stat()
	if err != nil {
		return err
	}

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode())
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}
