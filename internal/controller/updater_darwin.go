//go:build darwin

// Package: internal/controller
// Feature: F-025 (Auto-Update System)
// Story: US-025-07 (macOS Install Sequence)
// Purpose: macOS-specific update operations: tar.gz extraction with path traversal
//          protection, codesign verification, binary replacement via launchctl.

package controller

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// maxFileSize is the maximum size of a single file in an update archive.
// Prevents resource exhaustion from oversized entries.
const maxFileSize = 500 * 1024 * 1024 // 500 MB

// extractTarGz extracts a .tar.gz archive to destDir.
// Validates all paths to prevent directory traversal attacks.
// Rejects files larger than maxFileSize (500 MB).
// Preserves file permissions from the archive.
func extractTarGz(archivePath, destDir string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	defer f.Close()

	gr, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("gzip reader: %w", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tar next: %w", err)
		}

		// Path traversal protection: resolve and verify target stays within destDir
		target := filepath.Join(destDir, hdr.Name)
		if !strings.HasPrefix(filepath.Clean(target), filepath.Clean(destDir)+string(os.PathSeparator)) {
			return fmt.Errorf("path traversal detected: %s", hdr.Name)
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(hdr.Mode)); err != nil {
				return fmt.Errorf("mkdir %s: %w", hdr.Name, err)
			}
		case tar.TypeReg:
			// Reject oversized files based on header — prevents resource exhaustion
			if hdr.Size > maxFileSize {
				return fmt.Errorf("file %s exceeds max size: %d > %d bytes", hdr.Name, hdr.Size, maxFileSize)
			}
			// Ensure parent directory exists
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return fmt.Errorf("mkdir parent %s: %w", hdr.Name, err)
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode))
			if err != nil {
				return fmt.Errorf("create %s: %w", hdr.Name, err)
			}
			// LimitReader as defense-in-depth (header check is primary guard)
			if _, err := io.Copy(out, io.LimitReader(tr, maxFileSize)); err != nil {
				out.Close()
				return fmt.Errorf("extract %s: %w", hdr.Name, err)
			}
			out.Close()
		default:
			// Skip symlinks, hardlinks, etc. — we only expect regular files and dirs
			continue
		}
	}

	return nil
}

// expectedTeamID is the Developer ID team identifier for the StonkAgents release signing certificate.
// Binaries must be signed with this team's certificate to pass verification.
const expectedTeamID = "FNC2N9HR59"

// runCodesign executes codesign --verify and returns combined stdout+stderr.
// Package-level var allows test injection.
var runCodesign = defaultRunCodesign

func defaultRunCodesign(binaryPath string) (string, error) {
	cmd := exec.Command("codesign", "--verify", "--strict", "--verbose=2", binaryPath)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// verifyCodesign checks that the binary at binaryPath is validly code-signed
// with the expected team identifier (FNC2N9HR59).
func verifyCodesign(binaryPath string) error {
	output, err := runCodesign(binaryPath)
	if err != nil {
		return fmt.Errorf("codesign verification failed for %s: %w\n%s",
			filepath.Base(binaryPath), err, output)
	}

	// Parse output for TeamIdentifier
	if !strings.Contains(output, "TeamIdentifier="+expectedTeamID) {
		return fmt.Errorf("wrong team ID in %s: expected %s, output: %s",
			filepath.Base(binaryPath), expectedTeamID, output)
	}

	return nil
}

// stopDaemonFn and startDaemonFn wrap launchctl operations.
// Package-level vars allow test injection.
var stopDaemonFn = StopDaemon
var startDaemonFn = StartDaemon

// replaceBinaries copies new binaries from extractDir to binDir,
// overwriting existing files. Only copies known binary names.
func replaceBinaries(extractDir, binDir string) error {
	for _, name := range binaryNames {
		src := archiveBinaryPath(extractDir, name)
		if src == "" {
			continue // skip missing — archive may not contain all binaries
		}
		dst := filepath.Join(binDir, name)
		if err := copyFile(src, dst); err != nil {
			return fmt.Errorf("replace %s: %w", name, err)
		}
	}
	return nil
}

// installDarwin performs the full macOS update sequence:
// 1. Extract tar.gz to extractDir
// 2. Verify codesign on all extracted binaries
// 3. Backup current binaries
// 4. Stop daemon via launchctl
// 5. Replace binaries
// 6. Start daemon via launchctl
// 7. Wait for healthy
// 8. On health check failure: rollback + restart
func (u *Updater) installDarwin(ctx context.Context, archivePath, extractDir, binDir, backupDir, healthURL string) error {
	// Step 1: Extract
	u.logger.Info("[installDarwin] extracting archive", "path", archivePath)
	if err := extractTarGz(archivePath, extractDir); err != nil {
		return fmt.Errorf("extract: %w", err)
	}

	// Step 1b: Verify required binary (daemon) exists in archive
	if archiveBinaryPath(extractDir, "stonkagents") == "" {
		return fmt.Errorf("required binary missing from archive: stonkagents")
	}

	// Step 2: Verify codesign on all extracted binaries
	for _, name := range binaryNames {
		binPath := archiveBinaryPath(extractDir, name)
		if binPath == "" {
			continue // optional binaries (genkeys, stonkagents-notify) may be absent
		}
		if err := verifyCodesign(binPath); err != nil {
			return fmt.Errorf("codesign: %w", err)
		}
	}

	// Step 3: Backup current binaries
	u.logger.Info("[installDarwin] backing up current binaries")
	if err := backupBinaries(binDir, backupDir); err != nil {
		return fmt.Errorf("backup: %w", err)
	}

	// Step 4: Stop daemon
	u.logger.Info("[installDarwin] stopping daemon")
	if err := stopDaemonFn(); err != nil {
		u.logger.Warn("[installDarwin] stop daemon failed (may not be running)", "err", err)
		// Continue — daemon may not be running
	}

	// Step 5: Replace binaries
	u.logger.Info("[installDarwin] replacing binaries")
	if err := replaceBinaries(extractDir, binDir); err != nil {
		// Rollback immediately
		u.logger.Error("[installDarwin] replace failed, rolling back", "err", err)
		if rbErr := rollbackBinaries(backupDir, binDir); rbErr != nil {
			u.logger.Error("[installDarwin] rollback also failed", "rollback_err", rbErr)
		}
		if startErr := startDaemonFn(); startErr != nil {
			u.logger.Error("[installDarwin] restart after rollback failed", "start_err", startErr)
		}
		return fmt.Errorf("replace: %w", err)
	}

	// Step 6: Start daemon
	u.logger.Info("[installDarwin] starting daemon")
	if err := startDaemonFn(); err != nil {
		u.logger.Error("[installDarwin] start failed, rolling back", "err", err)
		if rbErr := rollbackBinaries(backupDir, binDir); rbErr != nil {
			u.logger.Error("[installDarwin] rollback also failed", "rollback_err", rbErr)
		}
		if startErr := startDaemonFn(); startErr != nil {
			u.logger.Error("[installDarwin] restart after rollback failed", "start_err", startErr)
		}
		return fmt.Errorf("start daemon: %w", err)
	}

	// Step 7: Wait for healthy
	u.logger.Info("[installDarwin] waiting for daemon health")
	if err := u.waitForHealthy(ctx, healthURL); err != nil {
		// Step 8: Rollback on failure
		u.logger.Error("[installDarwin] health check failed, rolling back", "err", err)
		if stopErr := stopDaemonFn(); stopErr != nil {
			u.logger.Error("[installDarwin] stop for rollback failed", "stop_err", stopErr)
		}
		if rbErr := rollbackBinaries(backupDir, binDir); rbErr != nil {
			u.logger.Error("[installDarwin] rollback also failed", "rollback_err", rbErr)
		}
		if startErr := startDaemonFn(); startErr != nil {
			u.logger.Error("[installDarwin] restart after rollback failed", "start_err", startErr)
		}
		return fmt.Errorf("health check: %w", err)
	}

	// Success — clean up backups
	u.logger.Info("[installDarwin] update installed successfully")
	cleanupBackups(backupDir)
	return nil
}
