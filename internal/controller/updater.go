// Package: internal/controller
// Feature: F-025 (Auto-Update System)
// Story: US-025-05 (Controller Update Engine)
// Purpose: Update state machine — tracks update lifecycle from notify through
//          download, verify, install, restart, and completion/failure/cancel.

package controller

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/stonkagents/agent/internal/installenv"
	"github.com/stonkagents/agent/internal/update"
)

// UpdateState represents the current phase of the update lifecycle.
type UpdateState string

const (
	StateIdle        UpdateState = "IDLE"
	StateAvailable   UpdateState = "AVAILABLE"
	StateDownloading UpdateState = "DOWNLOADING"
	StateVerifying   UpdateState = "VERIFYING"
	StateInstalling  UpdateState = "INSTALLING"
	StateRestarting  UpdateState = "RESTARTING"
	StateComplete    UpdateState = "COMPLETE"
	StateFailed      UpdateState = "FAILED"
	StateCancelled   UpdateState = "CANCELLED"
)

// StatusResponse is the JSON contract for GET /update/status.
// Flat object — no { data: T } envelope (controller is localhost-only, high-freq polling).
type StatusResponse struct {
	State           UpdateState `json:"state"`
	CurrentVersion  string      `json:"current_version"`
	LatestVersion   string      `json:"latest_version"`
	ReleaseNotes    string      `json:"release_notes"`
	Force           bool        `json:"force"`
	Progress        int         `json:"progress"`
	BytesDownloaded int64       `json:"bytes_downloaded"`
	BytesTotal      int64       `json:"bytes_total"`
	Error           *string     `json:"error"`
	// InstallerURL is the user-facing installer (Setup EXE / DMG) of the offered
	// release once the manifest has been fetched. ManualInstall is true on
	// platforms without an in-place install pipeline (Windows): the portal offers
	// the installer as a download instead of calling /update/start.
	InstallerURL  string `json:"installer_url,omitempty"`
	ManualInstall bool   `json:"manual_install"`
}

// ErrManualInstall is returned by Start on platforms without an install pipeline.
var ErrManualInstall = errors.New("in-place update is not available on this platform; download and run the installer")

// DefaultReleaseBaseURL is the single source of truth for the release host.
// The releases.*.stonkagents.com hosts stay live for installs that baked them.
const DefaultReleaseBaseURL = "https://releases.stonkagents.com/"

// envReleasesURL overrides the release base URL (and therefore the manifest
// URL) without a rebuild, e.g. for a staging mirror of releases.stonkagents.com.
const envReleasesURL = "STONKAGENTS_RELEASES_URL"

// releaseBaseURLFromEnv returns the release base URL: STONKAGENTS_RELEASES_URL
// when set (trailing slash normalised), else DefaultReleaseBaseURL.
func releaseBaseURLFromEnv() string {
	if u := strings.TrimSpace(os.Getenv(envReleasesURL)); u != "" {
		return strings.TrimRight(u, "/") + "/"
	}
	return DefaultReleaseBaseURL
}

// InstallFn is the platform-specific install function signature.
// Receives the downloaded archive path, temp extraction dir, binary dir, backup dir, and health URL.
type InstallFn func(ctx context.Context, archivePath, extractDir, binDir, backupDir, healthURL string) error

// UpdaterConfig holds all dependencies for the Updater.
// Later tasks add fields — the constructor signature never changes.
type UpdaterConfig struct {
	CurrentVersion        string
	PubKey                ed25519.PublicKey
	ManifestURL           string
	ReleaseBaseURL        string
	Logger                *slog.Logger
	HealthCheckTimeout    time.Duration
	HealthPollInterval    time.Duration
	MaxRetries            int
	RetryBaseDelay        time.Duration
	ManifestFetchCooldown time.Duration
	Notifier              Notifier  // Optional; nil means no notifications
	InstallFn             InstallFn // Platform-specific install (defaults to installDarwin on macOS)
	BinDir                string    // Binary directory (default: ~/.local/bin/)
	BackupDir             string    // Backup directory (default: ~/.local/bin/.update-backup/)
	HealthURL             string    // Daemon health endpoint (default: http://localhost:7841/health)
}

// Updater manages the controller's update lifecycle.
type Updater struct {
	mu              sync.RWMutex
	state           UpdateState
	currentVersion  string
	latestVersion   string
	releaseNotes    string
	force           bool
	progress        int
	bytesDownloaded int64
	bytesTotal      int64
	lastError       *string

	// Resolved manifest data (populated by Notify → eager fetch)
	resolvedPlatform *update.PlatformRelease // URL + SHA256 for current OS/arch
	lastFetchTime    time.Time               // rate limit manifest fetches

	// Config
	pubKey                ed25519.PublicKey
	manifestURL           string
	releaseBaseURL        string
	logger                *slog.Logger
	healthCheckTimeout    time.Duration
	healthPollInterval    time.Duration
	maxRetries            int
	retryBaseDelay        time.Duration
	manifestFetchCooldown time.Duration
	notifier              Notifier // nil = no notifications

	// Orchestration
	installFn InstallFn          // platform-specific install
	binDir    string             // where binaries live
	backupDir string             // where backups go
	healthURL string             // daemon health endpoint
	cancelFn  context.CancelFunc // cancel running update goroutine
}

// NewUpdater creates an Updater with sensible defaults.
func NewUpdater(cfg UpdaterConfig) *Updater {
	// Both default off one host so a STONKAGENTS_RELEASES_URL override moves
	// the manifest and the archives together.
	if cfg.ReleaseBaseURL == "" {
		cfg.ReleaseBaseURL = releaseBaseURLFromEnv()
	}
	if cfg.ManifestURL == "" {
		cfg.ManifestURL = cfg.ReleaseBaseURL + "manifest.json"
	}
	if cfg.HealthCheckTimeout == 0 {
		cfg.HealthCheckTimeout = 30 * time.Second
	}
	if cfg.HealthPollInterval == 0 {
		cfg.HealthPollInterval = 2 * time.Second
	}
	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = 3
	}
	if cfg.RetryBaseDelay == 0 {
		cfg.RetryBaseDelay = 1 * time.Second
	}
	if cfg.ManifestFetchCooldown == 0 {
		cfg.ManifestFetchCooldown = 15 * time.Minute
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.BinDir == "" {
		home, _ := os.UserHomeDir()
		cfg.BinDir = filepath.Join(home, ".local", "bin")
	}
	if cfg.BackupDir == "" {
		cfg.BackupDir = filepath.Join(cfg.BinDir, ".update-backup")
	}
	if cfg.HealthURL == "" {
		cfg.HealthURL = installenv.Current().DaemonHealthURL()
	}
	return &Updater{
		state:                 StateIdle,
		currentVersion:        cfg.CurrentVersion,
		pubKey:                cfg.PubKey,
		manifestURL:           cfg.ManifestURL,
		releaseBaseURL:        cfg.ReleaseBaseURL,
		logger:                cfg.Logger,
		healthCheckTimeout:    cfg.HealthCheckTimeout,
		healthPollInterval:    cfg.HealthPollInterval,
		maxRetries:            cfg.MaxRetries,
		retryBaseDelay:        cfg.RetryBaseDelay,
		manifestFetchCooldown: cfg.ManifestFetchCooldown,
		notifier:              cfg.Notifier,
		installFn:             cfg.InstallFn,
		binDir:                cfg.BinDir,
		backupDir:             cfg.BackupDir,
		healthURL:             cfg.HealthURL,
	}
}

// State returns the current update state (thread-safe).
func (u *Updater) State() UpdateState {
	u.mu.RLock()
	defer u.mu.RUnlock()
	return u.state
}

// Status returns the full status response (thread-safe).
func (u *Updater) Status() StatusResponse {
	u.mu.RLock()
	defer u.mu.RUnlock()
	return StatusResponse{
		State:           u.state,
		CurrentVersion:  u.currentVersion,
		LatestVersion:   u.latestVersion,
		ReleaseNotes:    u.releaseNotes,
		Force:           u.force,
		Progress:        u.progress,
		BytesDownloaded: u.bytesDownloaded,
		BytesTotal:      u.bytesTotal,
		Error:           u.lastError,
		InstallerURL:    u.installerURL(),
		ManualInstall:   u.installFn == nil,
	}
}

// installerURL returns the resolved platform's installer URL (falling back to
// the archive URL), or "" before the manifest has been fetched. Caller holds u.mu.
func (u *Updater) installerURL() string {
	if u.resolvedPlatform == nil {
		return ""
	}
	if u.resolvedPlatform.Installer != nil && u.resolvedPlatform.Installer.URL != "" {
		return u.resolvedPlatform.Installer.URL
	}
	return u.resolvedPlatform.URL
}

// Notify is called when the daemon reports a newer version from the tracker.
// Eagerly fetches and verifies the manifest, selects platform, resolves force flag.
// Falls back to stub behavior (no fetch) when pubKey is not set (unit tests).
func (u *Updater) Notify(version, notes string) {
	u.mu.Lock()
	defer u.mu.Unlock()

	// Only allow notify from IDLE, FAILED, or CANCELLED states.
	switch u.state {
	case StateIdle, StateFailed, StateCancelled:
		// proceed
	default:
		u.logger.Info("[Updater.Notify] ignored: active update in progress",
			"state", string(u.state), "version", version)
		return
	}

	// Must be newer than current version.
	if !update.VersionEligible(u.currentVersion, version) {
		u.logger.Info("[Updater.Notify] ignored: not newer",
			"current", u.currentVersion, "offered", version)
		return
	}

	// Eager manifest fetch when pubKey is available (production path).
	// Rate limited: max 1 fetch per manifestFetchCooldown.
	if len(u.pubKey) > 0 && time.Since(u.lastFetchTime) >= u.manifestFetchCooldown {
		u.mu.Unlock() // unlock during HTTP fetch to avoid blocking Status() calls
		manifest, err := u.fetchAndVerifyManifest()
		u.mu.Lock() // re-lock for state mutation
		if err != nil {
			u.logger.Error("[Updater.Notify] manifest fetch failed, staying in current state",
				"err", err, "version", version)
			return // do NOT transition to AVAILABLE
		}
		u.lastFetchTime = time.Now()

		// Platform selection
		plat, err := selectPlatform(manifest)
		if err != nil {
			u.logger.Error("[Updater.Notify] unsupported platform", "err", err)
			return
		}
		u.resolvedPlatform = plat

		// Use the manifest's authoritative data instead of the daemon's relay — and
		// check eligibility again against it. The daemon can relay a newer version
		// than this manifest carries (it heard about a dev release while this
		// controller reads another release host); offering an older build as an
		// "update" is wrong.
		if !update.VersionEligible(u.currentVersion, manifest.Version) {
			u.logger.Info("[Updater.Notify] ignored: manifest version not newer",
				"current", u.currentVersion, "relayed", version, "manifest", manifest.Version, "manifest_url", u.manifestURL)
			return
		}
		version = manifest.Version
		notes = manifest.ReleaseNotes
		u.force = isForceUpdate(manifest, u.currentVersion)
	}

	u.state = StateAvailable
	u.latestVersion = version
	u.releaseNotes = notes
	u.progress = 0
	u.lastError = nil
	u.logger.Info("[Updater.Notify] update available",
		"current", u.currentVersion, "latest", version)

	// Fire-and-forget notification (errors logged, not propagated)
	if u.notifier != nil {
		if err := u.notifier.NotifyUpdateAvailable(version, notes); err != nil {
			u.logger.Error("[Updater.Notify] notification failed", "err", err)
		}
	}
}

// Start begins the update download. Only valid from AVAILABLE state.
// When resolvedPlatform and installFn are set, spawns a goroutine that
// executes the full pipeline: download → install → complete/fail.
func (u *Updater) Start() error {
	u.mu.Lock()
	defer u.mu.Unlock()

	if u.state != StateAvailable {
		return fmt.Errorf("cannot start update from state %s", u.state)
	}
	// No install pipeline (Windows): stay AVAILABLE so the portal keeps offering
	// the installer download instead of a download that never progresses.
	if u.installFn == nil {
		return ErrManualInstall
	}

	u.state = StateDownloading
	u.progress = 0
	u.logger.Info("[Updater.Start] download starting", "version", u.latestVersion)

	// Orchestrate full pipeline when platform is resolved and installFn is set
	if u.resolvedPlatform != nil && u.installFn != nil {
		ctx, cancel := context.WithCancel(context.Background())
		u.cancelFn = cancel
		go u.runUpdate(ctx)
	}

	return nil
}

// UpdateProgress updates download byte counters and recalculates percentage.
// Called from the progressFn callback during archive download.
func (u *Updater) UpdateProgress(downloaded, total int64) {
	u.mu.Lock()
	defer u.mu.Unlock()

	u.bytesDownloaded = downloaded
	u.bytesTotal = total
	if total > 0 {
		u.progress = int(float64(downloaded) / float64(total) * 100)
	}
}

// Cancel aborts an in-progress download. Only valid from DOWNLOADING state.
// Cancels the orchestration goroutine's context if running.
func (u *Updater) Cancel() error {
	u.mu.Lock()
	defer u.mu.Unlock()

	if u.state != StateDownloading {
		return fmt.Errorf("cannot cancel from state %s", u.state)
	}

	if u.cancelFn != nil {
		u.cancelFn()
	}
	u.state = StateCancelled
	u.logger.Info("[Updater.Cancel] update cancelled", "version", u.latestVersion)
	return nil
}

// Fail records a failure from any active state.
func (u *Updater) Fail(reason string) {
	u.mu.Lock()
	defer u.mu.Unlock()

	u.state = StateFailed
	u.lastError = &reason
	u.progress = 0
	u.logger.Error("[Updater.Fail] update failed",
		"version", u.latestVersion, "reason", reason)

	if u.notifier != nil {
		if err := u.notifier.NotifyUpdateFailed(u.latestVersion, reason); err != nil {
			u.logger.Error("[Updater.Fail] notification failed", "err", err)
		}
	}
}

// Complete marks the update as successfully installed.
func (u *Updater) Complete() {
	u.mu.Lock()
	defer u.mu.Unlock()

	u.state = StateComplete
	u.progress = 100
	u.logger.Info("[Updater.Complete] update installed",
		"version", u.latestVersion)

	if u.notifier != nil {
		if err := u.notifier.NotifyUpdateComplete(u.latestVersion); err != nil {
			u.logger.Error("[Updater.Complete] notification failed", "err", err)
		}
	}
}

// runUpdate is the orchestration goroutine spawned by Start().
// Executes: download → install → complete/fail. Respects context cancellation.
func (u *Updater) runUpdate(ctx context.Context) {
	// Snapshot platform info under lock
	u.mu.RLock()
	plat := u.resolvedPlatform
	u.mu.RUnlock()

	// Create temp dir for download + extraction
	tmpDir, err := os.MkdirTemp("", "stonkagents-update-*")
	if err != nil {
		u.Fail(fmt.Sprintf("create temp dir: %v", err))
		return
	}
	defer os.RemoveAll(tmpDir)

	// Download archive with SHA256 verification
	archivePath, err := u.downloadArchive(ctx, plat.URL, plat.SHA256, tmpDir, func(downloaded, total int64) {
		u.UpdateProgress(downloaded, total)
	})
	if err != nil {
		if ctx.Err() != nil {
			return // cancelled; state already set by Cancel()
		}
		u.Fail(fmt.Sprintf("download: %v", err))
		return
	}

	// Transition to INSTALLING
	u.mu.Lock()
	if u.state != StateDownloading {
		u.mu.Unlock()
		return // cancelled between download and install
	}
	u.state = StateInstalling
	u.mu.Unlock()

	// Ensure backup dir exists
	os.MkdirAll(u.backupDir, 0755)

	// Create extract dir
	extractDir := filepath.Join(tmpDir, "extract")
	os.MkdirAll(extractDir, 0755)

	// Platform-specific install
	if err := u.installFn(ctx, archivePath, extractDir, u.binDir, u.backupDir, u.healthURL); err != nil {
		if ctx.Err() != nil {
			return // cancelled
		}
		u.Fail(fmt.Sprintf("install: %v", err))
		return
	}

	// Clean stale backups (7-day retention)
	cleanupStaleBackups(u.backupDir, 7*24*time.Hour)

	u.Complete()
}
