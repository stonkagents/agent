// Package: internal/daemon
// Feature: F-010 (Go Core Daemon)
// Story: US-010-01 (HTTP Server with Health Endpoint)
// Purpose: HTTP server - Built with TDD

package daemon

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
	"github.com/stonkagents/agent/internal/config"
	"github.com/stonkagents/agent/internal/daemon/agentchat"
	"github.com/stonkagents/agent/internal/daemon/download"
	"github.com/stonkagents/agent/internal/daemon/history"
	"github.com/stonkagents/agent/internal/daemon/stats"
	"github.com/stonkagents/agent/internal/daemon/storage"
	"github.com/stonkagents/agent/internal/daemon/tracker"
	"github.com/stonkagents/agent/internal/daemon/upload"
	"github.com/stonkagents/agent/internal/installenv"
	"github.com/stonkagents/agent/internal/logger"
)

// defaultTrackerHeartbeatInterval is used when config does not set tracker_heartbeat_interval_seconds.
const defaultTrackerHeartbeatInterval = 2 * time.Minute

// trackerAPIKeyFilename is the file name under DataDir for persisting the tracker API key (0600).
const trackerAPIKeyFilename = "tracker_api_key"

// trackerAccountIDFilename persists the F-013 account_id returned by challenge-response registration.
const trackerAccountIDFilename = "tracker_account_id"

// defaultControllerURL is the controller's localhost endpoint for this
// environment (STONKAGENTS_ENV from daemon.env; port 7840 for production).
func defaultControllerURL() string { return installenv.Current().ControllerURL() }

// ServerWriteTimeout must exceed handler context timeouts (12s) with margin for
// JSON marshaling and response write. Handlers needing longer use SetWriteDeadline.
const ServerWriteTimeout = 18 * time.Second

// Server represents the HTTP server
type Server struct {
	config *Config
	// configMu guards the config fields the setup surface rewrites at runtime
	// (UploadCapMbps, DownloadCapMbps, CORSAllowedOrigins, DisplayName) against
	// concurrent handler reads. Everything else in config is fixed after startup.
	configMu             sync.RWMutex
	httpServer           *http.Server
	startTime            time.Time
	shutdownSignal       chan os.Signal
	logger               *logger.Logger
	p2pHost              *P2PHost
	downloadManager      *download.Manager
	uploadManager        *upload.Manager
	chunkStore           *storage.SQLiteChunkStore
	trackerClient        *tracker.Client
	trackerHeartbeatStop chan struct{} // closed to stop tracker heartbeat goroutine

	// Background work (auto-seed, tracker reports) that must not outlive the stores it
	// uses: goBackground tracks it, stopping is closed by Shutdown before the stores are
	// closed, and Shutdown waits (bounded) for the WaitGroup so a late StoreChunk never
	// hits a closed SQLite handle.
	bg                 sync.WaitGroup
	stopping           chan struct{}
	stopOnce           sync.Once
	transferStats      *stats.Repository
	historyRepo        *history.Repository
	agentChatRepo      *agentchat.Repository // agent-to-agent chat session history (optional)
	trackerAPIKey      string                // peer API key from tracker (first register); used by portal proxy
	trackerAPIKeyMu    sync.RWMutex
	trackerAccountID   string // F-013 account_id from challenge-response registration
	trackerAccountIDMu sync.RWMutex

	// F-025: Update relay — daemon notifies controller when tracker reports a newer version
	controllerURL       string // e.g. "http://127.0.0.1:7840"
	lastNotifiedVersion string // debounce: skip POST if version unchanged

	// F-025: Cached update info from heartbeat — exposed in /status response
	// Protected by updateMu (written by heartbeat goroutine, read by HTTP handlers).
	updateMu            sync.RWMutex
	cachedLatestVersion string
	cachedReleaseNotes  string

	// Online peer count shown on /status. The tracker round trip behind it runs in
	// the background: the handler answers from this cache at once and refreshes it
	// when it is older than peerCountTTL (one refresh at a time), so a slow or
	// unreachable tracker never delays the portal's health poll, which gives up
	// after one second and then reads the agent as offline.
	peerCountMu         sync.Mutex
	peerCount           int
	peerCountAt         time.Time
	peerCountRefreshing bool

	// Live CORS allowlist (PERF-2) — extended by POST /api/v1/setup/origin.
	corsConfig *CORSConfig
	corsOnce   sync.Once

	// Setup surface state (RUN-1): rate limiter, pending data_dir, tracker registration outcome.
	setup     *setupState
	setupOnce sync.Once

	// Agent Autopilot (board watcher): live policy, suggestion store, run state.
	autopilot     *autopilotState
	autopilotOnce sync.Once
}

// HealthResponse represents the health endpoint response
type HealthResponse struct {
	Status        string `json:"status"`
	Version       string `json:"version"`
	UptimeSeconds int64  `json:"uptime_seconds"`
}

// NewServer creates a new Server instance with default config
func NewServer() *Server {
	return &Server{
		config: &Config{
			Host: "127.0.0.1",
			Port: 7841,
		},
		startTime:      time.Now(),
		shutdownSignal: make(chan os.Signal, 1),
		stopping:       make(chan struct{}),
	}
}

// NewServerWithConfig creates a new Server with custom config
func NewServerWithConfig(config *Config) *Server {
	return &Server{
		config:         config,
		startTime:      time.Now(),
		shutdownSignal: make(chan os.Signal, 1),
		stopping:       make(chan struct{}),
		controllerURL:  defaultControllerURL(),
	}
}

// NewServerWithLogger creates a new Server with config and logger
func NewServerWithLogger(config *Config, log *logger.Logger) *Server {
	return &Server{
		config:         config,
		startTime:      time.Now(),
		shutdownSignal: make(chan os.Signal, 1),
		stopping:       make(chan struct{}),
		logger:         log,
		controllerURL:  defaultControllerURL(),
	}
}

// GetLogger returns the server's logger instance
func (s *Server) GetLogger() *logger.Logger {
	return s.logger
}

// SetUpdateInfo stores the latest version and release notes from tracker heartbeat.
// Thread-safe: called from heartbeat goroutine.
func (s *Server) SetUpdateInfo(version, releaseNotes string) {
	s.updateMu.Lock()
	s.cachedLatestVersion = version
	s.cachedReleaseNotes = releaseNotes
	s.updateMu.Unlock()
}

// GetUpdateInfo returns the cached latest version and release notes.
// Thread-safe: called from HTTP handler goroutines.
func (s *Server) GetUpdateInfo() (version, releaseNotes string) {
	s.updateMu.RLock()
	version = s.cachedLatestVersion
	releaseNotes = s.cachedReleaseNotes
	s.updateMu.RUnlock()
	return
}

// InitializeManagers initializes download/upload managers and storage
func (s *Server) InitializeManagers() error {
	// Ensure DataDir is set
	if s.config.DataDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("failed to get home directory: %w", err)
		}
		s.config.DataDir = filepath.Join(home, config.DefaultDataDirName())
	}

	// Create data directories
	downloadsDir := filepath.Join(s.config.DataDir, "downloads")
	chunksDB := filepath.Join(s.config.DataDir, "chunks.db")

	if err := os.MkdirAll(downloadsDir, 0755); err != nil {
		return fmt.Errorf("failed to create downloads directory: %w", err)
	}

	// Initialize chunk store (SQLite)
	chunkStore, err := storage.NewSQLiteChunkStore(chunksDB)
	if err != nil {
		return fmt.Errorf("failed to initialize chunk store: %w", err)
	}
	s.chunkStore = chunkStore

	// Initialize download manager
	s.downloadManager = download.NewManager(downloadsDir, 3) // max 3 concurrent downloads

	// Restore incomplete downloads from previous session
	if err := s.downloadManager.LoadIncomplete(); err != nil {
		if s.logger != nil {
			s.logger.Warn("Server", "InitializeManagers", fmt.Sprintf("Failed to load incomplete downloads: %v", err), nil)
		}
		// Non-fatal: continue without restored downloads
	}

	// Initialize upload manager
	s.uploadManager = upload.NewManager(10) // max 10 concurrent uploads

	// Bandwidth caps from config.yaml (setup surface writes them; 0 = unlimited)
	s.applyBandwidthCaps(s.config.UploadCapMbps, s.config.DownloadCapMbps)

	// Initialize SQLite-backed transfer stats (US-029-24: AC-32/AC-33)
	statsDBPath := filepath.Join(s.config.DataDir, "stats.db")
	statsRepo, err := stats.NewRepository(statsDBPath)
	if err != nil {
		if s.logger != nil {
			s.logger.Warn("Server", "InitializeManagers", fmt.Sprintf("Failed to open stats DB: %v; stats will not persist", err), nil)
		}
	} else {
		if loadErr := statsRepo.LoadStats(); loadErr != nil && s.logger != nil {
			s.logger.Warn("Server", "InitializeManagers", fmt.Sprintf("Failed to load persisted stats: %v", loadErr), nil)
		}
		statsRepo.StartTicker()
	}
	s.transferStats = statsRepo

	// Initialize history repository (US-029-05: AC-9)
	historyDBPath := filepath.Join(s.config.DataDir, "history.db")
	historyRepo, err := history.NewRepository(historyDBPath)
	if err != nil {
		if s.logger != nil {
			s.logger.Warn("Server", "InitializeManagers", fmt.Sprintf("Failed to open history DB: %v; history will not persist", err), nil)
		}
	}
	s.historyRepo = historyRepo

	// Agent chat session persistence (optional; same DataDir)
	agentChatDBPath := filepath.Join(s.config.DataDir, "agent_chat.db")
	agentChatRepo, err := agentchat.NewRepository(agentChatDBPath)
	if err != nil {
		if s.logger != nil {
			s.logger.Warn("Server", "InitializeManagers", fmt.Sprintf("Failed to open agent chat DB: %v; session history will not persist", err), nil)
		}
	} else {
		s.agentChatRepo = agentChatRepo
	}

	// Set download completion callback to report to tracker + write history (AC-10)
	s.downloadManager.SetCompletionCallback(func(cid, filename string, totalBytes, totalSize int64) {
		if s.transferStats != nil {
			s.transferStats.RecordDownload(totalBytes)
		}
		// Write to history (AC-10: completed downloads)
		if s.historyRepo != nil {
			now := time.Now()
			rec := &history.TransferRecord{
				CID: cid, Filename: filename, FileType: filepath.Ext(filename),
				Direction: "download", TotalSize: totalSize, State: "completed",
				StartedAt: now, CompletedAt: now,
			}
			if insertErr := s.historyRepo.Insert(rec); insertErr != nil && s.logger != nil {
				s.logger.Error("Server", "DownloadComplete", fmt.Sprintf("Failed to write history: %v", insertErr), nil)
			}
		}
		if s.trackerClient != nil && s.p2pHost != nil {
			peerID := s.p2pHost.ID().String()
			s.goBackground(func() {
				if err := s.trackerClient.ReportDownloadComplete(peerID, cid); err != nil && s.logger != nil {
					s.logger.Error("Daemon", "DownloadComplete", fmt.Sprintf("Failed to report download completion: %v", err), nil)
				}
			})
		}

		// Auto-seed: re-chunk assembled file and announce to swarm so every downloader becomes a seeder
		s.goBackground(func() {
			if s.isStopping() {
				return
			}
			absPath, fname, err := s.findAssembledDownloadPath(cid)
			if err != nil {
				if s.logger != nil {
					s.logger.Error("Server", "AutoSeed", fmt.Sprintf("Cannot find assembled file for auto-seed: %v", err), nil)
				}
				return
			}
			if err := s.seedLibraryFromDiskFile(absPath, cid, fname); err != nil {
				if s.logger != nil {
					s.logger.Error("Server", "AutoSeed", fmt.Sprintf("Auto-seed failed: %v", err), nil)
				}
			} else if s.logger != nil {
				s.logger.Info("Server", "AutoSeed", "Download auto-seeded to swarm", map[string]interface{}{"cid": cid})
			}
		})
	})

	// Set download failure callback to write history (AC-10: failed downloads)
	s.downloadManager.SetFailureCallback(func(cid, filename string, totalSize int64, errorMessage string) {
		if s.historyRepo != nil {
			now := time.Now()
			rec := &history.TransferRecord{
				CID: cid, Filename: filename, FileType: filepath.Ext(filename),
				Direction: "download", TotalSize: totalSize, State: "failed",
				ErrorMessage: errorMessage, StartedAt: now, CompletedAt: now,
			}
			if insertErr := s.historyRepo.Insert(rec); insertErr != nil && s.logger != nil {
				s.logger.Error("Server", "DownloadFailed", fmt.Sprintf("Failed to write history : %v", insertErr), nil)
			}
		}
	})

	// Initialize tracker client using config value (Audit F2: Configurable Tracker URL)
	trackerURL := s.config.TrackerURL
	if trackerURL == "" {
		trackerURL = config.DefaultTrackerURL // Fallback to default if not set
	}
	peerID := s.config.PeerID
	if peerID == "" {
		peerID = "default-peer-id" // Use actual peer ID from P2P host if available
		if s.p2pHost != nil {
			peerID = s.p2pHost.ID().String()
		}
	}
	s.trackerClient = tracker.NewClient(trackerURL, peerID)

	if s.logger != nil {
		s.logger.Info("Server", "InitializeManagers", "Managers initialized successfully", map[string]interface{}{
			"downloads_dir": downloadsDir,
			"chunks_db":     chunksDB,
			"tracker_url":   trackerURL,
		})
	}

	return nil
}

// loadTrackerAPIKey reads the tracker API key from DataDir/tracker_api_key if the file exists.
// Call before first register or at startup so the portal proxy has the key after restart.
func (s *Server) loadTrackerAPIKey() {
	if s.config == nil || s.config.DataDir == "" {
		return
	}
	path := filepath.Join(s.config.DataDir, trackerAPIKeyFilename)
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	key := strings.TrimSpace(string(data))
	if key == "" {
		return
	}
	s.trackerAPIKeyMu.Lock()
	s.trackerAPIKey = key
	s.trackerAPIKeyMu.Unlock()
	if s.logger != nil {
		s.logger.Info("Server", "loadTrackerAPIKey", "Tracker API key loaded from file", nil)
	}
}

// saveTrackerAPIKey writes the key to DataDir/tracker_api_key with 0600 permissions.
func (s *Server) saveTrackerAPIKey(key string) error {
	if s.config == nil || s.config.DataDir == "" || key == "" {
		return nil
	}
	if err := os.MkdirAll(s.config.DataDir, 0700); err != nil {
		return err
	}
	path := filepath.Join(s.config.DataDir, trackerAPIKeyFilename)
	if err := os.WriteFile(path, []byte(key), 0600); err != nil {
		return err
	}
	s.trackerAPIKeyMu.Lock()
	s.trackerAPIKey = key
	s.trackerAPIKeyMu.Unlock()
	if s.logger != nil {
		s.logger.Info("Server", "saveTrackerAPIKey", "Tracker API key stored", nil)
	}
	return nil
}

// getTrackerAPIKey returns the in-memory tracker API key (for portal proxy). Empty if not set.
func (s *Server) getTrackerAPIKey() string {
	s.trackerAPIKeyMu.RLock()
	defer s.trackerAPIKeyMu.RUnlock()
	return s.trackerAPIKey
}

// loadTrackerAccountID reads the persisted account_id from DataDir/tracker_account_id.
func (s *Server) loadTrackerAccountID() {
	if s.config == nil || s.config.DataDir == "" {
		return
	}
	path := filepath.Join(s.config.DataDir, trackerAccountIDFilename)
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	id := strings.TrimSpace(string(data))
	if id == "" {
		return
	}
	s.trackerAccountIDMu.Lock()
	s.trackerAccountID = id
	s.trackerAccountIDMu.Unlock()
}

// saveTrackerAccountID writes the account_id to DataDir/tracker_account_id with 0600 permissions.
func (s *Server) saveTrackerAccountID(id string) error {
	if s.config == nil || s.config.DataDir == "" || id == "" {
		return nil
	}
	if err := os.MkdirAll(s.config.DataDir, 0700); err != nil {
		return err
	}
	path := filepath.Join(s.config.DataDir, trackerAccountIDFilename)
	if err := os.WriteFile(path, []byte(id), 0600); err != nil {
		return err
	}
	s.trackerAccountIDMu.Lock()
	s.trackerAccountID = id
	s.trackerAccountIDMu.Unlock()
	return nil
}

// getTrackerAccountID returns the F-013 account_id. Empty if registered via legacy flow.
func (s *Server) getTrackerAccountID() string {
	s.trackerAccountIDMu.RLock()
	defer s.trackerAccountIDMu.RUnlock()
	return s.trackerAccountID
}

// Start starts the HTTP server
func (s *Server) Start() error {
	// Load persisted tracker API key and account_id so portal proxy works after restart
	s.loadTrackerAPIKey()
	s.loadTrackerAccountID()

	// Log startup if logger available
	if s.logger != nil {
		s.logger.Info("Server", "Start", "Starting HTTP server", map[string]interface{}{
			"address": s.config.Address(),
		})
	}

	// Create HTTP mux
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/readiness", s.handleReadiness)
	mux.HandleFunc("/api/v1/installer/peer-key", s.handleInstallerPeerKey)
	s.registerSetupRoutes(mux) // RUN-1: GET /api/v1/setup/status, POST /api/v1/setup/{id} (loopback only)
	mux.HandleFunc("/api/v1/share", s.handleShare)
	mux.HandleFunc("/api/v1/search", s.handleSearch)
	mux.HandleFunc("/api/v1/ask", s.handleAsk)
	mux.HandleFunc("/api/v1/agent/chat", s.handleAgentChat)
	mux.HandleFunc("/api/v1/agent/chat/sessions", s.handleAgentChatSessions)
	mux.Handle("/api/v1/agent/chat/history/", http.HandlerFunc(s.handleAgentChatHistoryPrefix))
	mux.HandleFunc("/api/v1/download", s.handleDownload)
	mux.HandleFunc("/api/v1/live-agent/download", s.handleLiveAgentDownload)
	mux.HandleFunc("/api/v1/status", s.handleStatus)
	mux.HandleFunc("/api/v1/node/stats", s.handleNodeStats)
	mux.HandleFunc("/api/v1/downloads/status", s.handleDownloadsStatus)
	mux.HandleFunc("/api/v1/downloads/retry", s.handleRetryDownload)
	mux.HandleFunc("/api/v1/downloads/pause", s.handlePauseDownload)
	mux.HandleFunc("/api/v1/downloads/resume", s.handleResumeDownload)
	mux.HandleFunc("POST /api/v1/downloads/{cid}/pause", s.handlePauseDownloadByPath)
	mux.HandleFunc("POST /api/v1/downloads/{cid}/resume", s.handleResumeDownloadByPath)
	mux.HandleFunc("POST /api/v1/downloads/{cid}/cancel", s.handleCancelDownloadByPath)
	mux.HandleFunc("POST /api/v1/downloads/{cid}/retry", s.handleRetryDownloadByPath)
	mux.HandleFunc("POST /api/v1/downloads/{cid}/seed", s.handleSeedDownloadByPath)
	mux.HandleFunc("/api/v1/downloads/pause-all", s.handlePauseAll)
	mux.HandleFunc("/api/v1/downloads/resume-all", s.handleResumeAll)
	mux.HandleFunc("/api/v1/downloads/clear", s.handleClearCompleted)
	mux.HandleFunc("/api/v1/uploads/status", s.handleUploadsStatus)
	mux.HandleFunc("/api/v1/transfers/history", s.handleTransferHistory)
	mux.HandleFunc("/api/v1/library", s.handleLibrary)
	mux.HandleFunc("GET /api/v1/files/{cid}", s.handleExportFileByCID)
	mux.HandleFunc("/api/v1/connections", s.handleConnections)
	mux.HandleFunc("/api/v1/wallet/link", s.handleWalletLink)
	// F-013 proxy: daemon /api/v1/{credits,social,purchase,account}/* → tracker /api/v1/tracker/{...}/* with X-API-Key
	mux.HandleFunc("/api/v1/credits/", s.handleF013Proxy)
	mux.HandleFunc("/api/v1/social/", s.handleF013Proxy)
	mux.HandleFunc("/api/v1/purchase/", s.handleF013Proxy)
	mux.HandleFunc("/api/v1/account/", s.handleF013Proxy)
	// Portal proxy (v1): daemon /api/v1/portal/* → tracker /api/* with X-API-Key (peers/trust, peers/block, board/posts, etc.)
	mux.Handle("/api/v1/controller/", s.controllerProxy()) // F-025: proxy update ops to controller
	mux.Handle("/api/v1/portal/", http.HandlerFunc(s.handlePortalProxyV1))
	// Portal proxy (legacy): forward /api/* to tracker with X-API-Key (path as-is)
	mux.Handle("/api/", http.HandlerFunc(s.handlePortalProxy))

	// SECURITY: Add CORS middleware for browser clients (live allowlist: config.yaml
	// cors_allowed_origins + CORS_ALLOWED_ORIGINS + POST /api/v1/setup/origin)
	corsHandler := CORSMiddleware(s.corsPolicy())(mux)

	// Add correlation ID middleware (audit C2)
	handler := correlationIDMiddleware(corsHandler)

	// Create HTTP server with security limits (DoS protection)
	s.httpServer = &http.Server{
		Addr:           s.config.Address(),
		Handler:        handler,            // Wrap with CORS + correlation ID middleware
		MaxHeaderBytes: 1 << 20,            // 1 MB max headers (prevents DoS)
		ReadTimeout:    10 * time.Second,   // Prevent slow client attacks
		WriteTimeout:   ServerWriteTimeout, // Must exceed handler context timeouts (12s) with margin
		IdleTimeout:    120 * time.Second,  // Close idle connections
	}

	// Start server in goroutine
	go func() {
		if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			if s.logger != nil {
				s.logger.Error("Server", "Start", "HTTP server error", map[string]interface{}{
					"error": err.Error(),
				})
			}
		}
	}()

	// Give server time to start
	time.Sleep(100 * time.Millisecond)

	// Owner's daemon: poll tracker for pending token chat requests and respond with local LLM
	go func() {
		trackerBase := ""
		if s.config != nil {
			trackerBase = strings.TrimSpace(s.config.TrackerURL)
		}
		apiKey := s.getTrackerAPIKey()
		gatewayURL := ""
		if s.config != nil {
			gatewayURL = strings.TrimSpace(s.config.GatewayURL)
		}
		if trackerBase != "" && apiKey != "" && gatewayURL != "" {
			if s.logger != nil {
				s.logger.Info("Server", "Start", "Agent-chat relay poll started (every 15s); owner will respond to token chat requests", nil)
			}
		} else if s.logger != nil {
			s.logger.Info("Server", "Start", "Agent-chat relay poll not started: tracker_url, API key, and gateway_url required for token chat owner responses", nil)
		}
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			s.RunAgentChatRelayPollCycle(context.Background())
		}
	}()

	// Agent Autopilot: board watcher (idle while autopilot.mode is off).
	s.startAutopilotLoop()

	if s.logger != nil {
		s.logger.Info("Server", "Start", "HTTP server started successfully", nil)
	}

	return nil
}

// Shutdown gracefully shuts down the server
// goBackground runs fn on a tracked goroutine. Work started after Shutdown began is
// dropped so nothing new touches stores that are about to close.
func (s *Server) goBackground(fn func()) {
	if s.isStopping() {
		return
	}
	s.bg.Add(1)
	go func() {
		defer s.bg.Done()
		fn()
	}()
}

// isStopping reports whether Shutdown has begun. Long background loops (auto-seed
// chunking) poll it between units of work so they stop promptly.
func (s *Server) isStopping() bool {
	if s.stopping == nil {
		return false
	}
	select {
	case <-s.stopping:
		return true
	default:
		return false
	}
}

// backgroundDrainTimeout bounds how long Shutdown waits for background work.
const backgroundDrainTimeout = 5 * time.Second

func (s *Server) Shutdown() error {
	// Signal background work first and drain it (bounded) before any store is closed.
	s.stopOnce.Do(func() {
		if s.stopping != nil {
			close(s.stopping)
		}
	})
	drained := make(chan struct{})
	go func() { s.bg.Wait(); close(drained) }()
	select {
	case <-drained:
	case <-time.After(backgroundDrainTimeout):
		if s.logger != nil {
			s.logger.Error("Server", "Shutdown", "background work did not finish before the drain timeout", nil)
		}
	}

	// Close the libp2p host before the stores: no peer can request a chunk from a
	// store that is about to close. Idempotent; tests that already called StopP2P
	// are unaffected.
	if err := s.StopP2P(); err != nil && s.logger != nil {
		s.logger.Error("Server", "Shutdown", fmt.Sprintf("StopP2P: %v", err), nil)
	}

	// Stop tracker heartbeat so we don't re-register during shutdown
	if s.trackerHeartbeatStop != nil {
		close(s.trackerHeartbeatStop)
		s.trackerHeartbeatStop = nil
	}

	// Stop download worker before HTTP server to prevent in-flight downloads
	if s.downloadManager != nil {
		s.downloadManager.StopWorker()
	}
	// Save and close transfer stats DB (US-029-24: AC-33 graceful shutdown)
	if s.transferStats != nil {
		s.transferStats.StopAndSave()
		s.transferStats = nil
	}

	// Close history DB (US-029-05)
	if s.historyRepo != nil {
		_ = s.historyRepo.Close()
		s.historyRepo = nil
	}

	// Close agent chat DB
	if s.agentChatRepo != nil {
		_ = s.agentChatRepo.Close()
		s.agentChatRepo = nil
	}

	// Close chunk store so SQLite releases the DB file (required for temp dir cleanup on Windows)
	if s.chunkStore != nil {
		_ = s.chunkStore.Close()
		s.chunkStore = nil
	}

	if s.httpServer == nil {
		return nil
	}

	if s.logger != nil {
		s.logger.Info("Server", "Shutdown", "Shutting down HTTP server", nil)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := s.httpServer.Shutdown(ctx)

	if s.logger != nil {
		if err != nil {
			s.logger.Error("Server", "Shutdown", "Error during shutdown", map[string]interface{}{
				"error": err.Error(),
			})
		} else {
			s.logger.Info("Server", "Shutdown", "HTTP server stopped", nil)
		}
	}

	return err
}

// WaitForShutdown blocks until a shutdown signal is received
func (s *Server) WaitForShutdown() {
	// Register for SIGTERM and SIGINT signals
	signal.Notify(s.shutdownSignal, syscall.SIGTERM, syscall.SIGINT)

	// Block until signal received
	<-s.shutdownSignal

	// Gracefully shutdown the server
	s.Shutdown()
}

// TriggerShutdown sends a shutdown signal (for testing)
func (s *Server) TriggerShutdown() {
	s.shutdownSignal <- syscall.SIGTERM
}

// StartP2P initializes and starts the P2P networking.
// Fetches relay info from the tracker to configure AutoRelay for NAT traversal.
// Falls back to basic relay mode if tracker relay is unavailable.
func (s *Server) StartP2P() error {
	if s.p2pHost != nil {
		return nil // Already started
	}

	// Try to fetch relay info from tracker for NAT traversal
	var relayAddrInfo []peer.AddrInfo
	if s.trackerClient != nil {
		relayInfo, err := s.trackerClient.GetRelayInfo()
		if err != nil {
			if s.logger != nil {
				s.logger.Info("Server", "StartP2P", "Tracker relay unavailable, using basic relay mode", map[string]interface{}{
					"error": err.Error(),
				})
			}
		} else {
			// Parse relay PeerID and multiaddrs into peer.AddrInfo
			relayPeerID, err := peer.Decode(relayInfo.PeerID)
			if err != nil {
				if s.logger != nil {
					s.logger.Error("Server", "StartP2P", "Invalid relay PeerID, using basic relay mode", map[string]interface{}{
						"error":   err.Error(),
						"peer_id": relayInfo.PeerID,
					})
				}
			} else {
				maddrs := make([]multiaddr.Multiaddr, 0, len(relayInfo.Multiaddrs))
				for _, addrStr := range relayInfo.Multiaddrs {
					fullAddr, err := multiaddr.NewMultiaddr(addrStr)
					if err != nil {
						if s.logger != nil {
							s.logger.Debug("Server", "StartP2P", "Skipping invalid relay multiaddr", map[string]interface{}{
								"addr":  addrStr,
								"error": err.Error(),
							})
						}
						continue
					}
					// Use AddrInfoFromP2pAddr to correctly split transport addr from peer ID.
					// If the multiaddr includes /p2p/<id>, strip it for the Addrs slice.
					addrInfo, err := peer.AddrInfoFromP2pAddr(fullAddr)
					if err == nil && addrInfo.ID == relayPeerID {
						maddrs = append(maddrs, addrInfo.Addrs...)
					} else {
						// Multiaddr doesn't include /p2p/ or has a different peer — use as-is
						maddrs = append(maddrs, fullAddr)
					}
				}
				if len(maddrs) > 0 {
					relayAddrInfo = []peer.AddrInfo{{ID: relayPeerID, Addrs: maddrs}}
					if s.logger != nil {
						s.logger.Info("Server", "StartP2P", "Tracker relay info fetched", map[string]interface{}{
							"relay_peer_id": relayPeerID.String(),
							"relay_addrs":   len(maddrs),
						})
					}
				}
			}
		}
	}

	var p2pHost *P2PHost
	var err error
	if len(relayAddrInfo) > 0 {
		p2pHost, err = NewP2PHost(s.config, s.logger, relayAddrInfo[0])
	} else {
		p2pHost, err = NewP2PHost(s.config, s.logger)
	}
	if err != nil {
		return err
	}

	s.p2pHost = p2pHost
	s.config.PeerID = p2pHost.ID().String()
	if s.config.PublicKey == "" {
		if pk, err := p2pHost.PublicKeyBase64(); err == nil {
			s.config.PublicKey = pk
		}
	}
	if s.trackerClient != nil {
		s.trackerClient.SetPeerID(s.config.PeerID)
	}
	// The libp2p id is final now: give the agent its placeholder name if it has none,
	// before the first registration carries display_name.
	s.ensureDefaultDisplayName()

	if s.logger != nil {
		s.logger.Info("Server", "StartP2P", "P2P networking started", map[string]interface{}{
			"peer_id":   p2pHost.ID().String(),
			"has_relay": len(relayAddrInfo) > 0,
		})
	}

	return nil
}

// RegisterWithTracker registers this peer with the tracker and refreshes presence.
// TD-016: Tries challenge-response first, falls back to legacy /register.
func (s *Server) RegisterWithTracker() (err error) {
	defer func() { s.recordTrackerRegistration(err) }()
	if s.trackerClient == nil {
		return fmt.Errorf("tracker client not initialized")
	}
	if s.p2pHost == nil {
		return fmt.Errorf("P2P host not initialized")
	}

	peerID := s.p2pHost.ID().String()
	addrs := s.p2pHost.AnnounceAddrsRefreshed()

	// TD-016: Try challenge-response registration first
	if s.config != nil && s.config.PrivateKey != "" {
		apiKey, err := s.registerWithChallengeResponse(peerID, addrs)
		if err == nil {
			if apiKey != "" {
				if err := s.saveTrackerAPIKey(apiKey); err != nil && s.logger != nil {
					s.logger.Error("Daemon", "RegisterWithTracker", fmt.Sprintf("Failed to save tracker API key: %v", err), nil)
				}
			}
			s.reportTransferStats(peerID)
			if s.logger != nil {
				s.logger.Info("Daemon", "RegisterWithTracker", "Registered with tracker (challenge-response)", map[string]interface{}{
					"peer_id": peerID,
					"addrs":   addrs,
				})
			}
			return nil
		}
		// Log fallback but continue to legacy
		if s.logger != nil {
			s.logger.Warn("Daemon", "RegisterWithTracker", fmt.Sprintf("Challenge-response failed, falling back to legacy: %v", err), nil)
		}
	}

	// Legacy fallback: direct register (no challenge, no signature)
	publicKey := s.config.PublicKey
	apiKey, err := s.trackerClient.Register(peerID, publicKey, addrs)
	if err != nil {
		return err
	}
	if apiKey != "" {
		if err := s.saveTrackerAPIKey(apiKey); err != nil && s.logger != nil {
			s.logger.Error("Daemon", "RegisterWithTracker", fmt.Sprintf("Failed to save tracker API key: %v", err), nil)
		}
	}
	s.reportTransferStats(peerID)
	if s.logger != nil {
		s.logger.Info("Daemon", "RegisterWithTracker", "Registered with tracker (legacy)", map[string]interface{}{
			"peer_id": peerID,
			"addrs":   addrs,
		})
	}

	return nil
}

// registerWithChallengeResponse performs the 2-step challenge-response registration.
// TD-016: POST /challenge → sign nonce → POST /register/identity
func (s *Server) registerWithChallengeResponse(peerID string, addrs []string) (string, error) {
	challengeResp, err := s.trackerClient.Challenge(peerID)
	if err != nil {
		return "", fmt.Errorf("challenge: %w", err)
	}

	pubkeyB64, sigB64, err := tracker.SignChallenge(s.config.PrivateKey, challengeResp.Nonce)
	if err != nil {
		return "", fmt.Errorf("sign challenge: %w", err)
	}

	version := ""
	if s.config != nil {
		version = s.config.Version
	}
	resp, err := s.trackerClient.RegisterIdentity(peerID, pubkeyB64, sigB64, addrs, version, s.displayName())
	if err != nil {
		return "", fmt.Errorf("register identity: %w", err)
	}

	// Store account_id for F-013 capabilities (credits, social, purchase)
	if resp.AccountID != "" {
		if err := s.saveTrackerAccountID(resp.AccountID); err != nil && s.logger != nil {
			s.logger.Error("Daemon", "registerWithChallengeResponse", fmt.Sprintf("Failed to save account_id: %v", err), nil)
		}
	}

	return resp.APIKey, nil
}

// reportTransferStats sends transfer stats to the tracker (best-effort).
func (s *Server) reportTransferStats(peerID string) {
	if s.transferStats == nil {
		return
	}
	snap := s.transferStats.GetStats()
	// Combine speeds for backward-compatible ReportStats (tracker expects single avgSpeed)
	combinedSpeed := snap.UploadSpeedBPS + snap.DownloadSpeedBPS
	if err := s.trackerClient.ReportStats(peerID, snap.TotalUpload, snap.TotalDownload, &combinedSpeed); err != nil && s.logger != nil {
		s.logger.Error("Daemon", "reportTransferStats", fmt.Sprintf("Failed to report stats: %v", err), nil)
	}
}

// doHeartbeat sends an authenticated heartbeat to the tracker, falling back to
// full re-registration if the heartbeat endpoint is unavailable (TD-016).
func (s *Server) doHeartbeat() error {
	apiKey := s.getTrackerAPIKey()
	if apiKey != "" && s.trackerClient != nil && s.p2pHost != nil {
		addrs := s.p2pHost.AnnounceAddrsRefreshed()
		version := ""
		if s.config != nil {
			version = s.config.Version
		}
		resp, err := s.trackerClient.Heartbeat(apiKey, addrs, version, s.displayName(), s.autopilotHeartbeatCategories())
		if err == nil {
			s.recordTrackerRegistration(nil)
			s.reportTransferStats(s.p2pHost.ID().String())
			// F-025: Cache update info for /status and relay to controller
			if resp != nil && resp.LatestVersion != "" {
				s.SetUpdateInfo(resp.LatestVersion, resp.ReleaseNotes)
				s.notifyControllerUpdate(resp.LatestVersion, resp.ReleaseNotes)
			}
			if resp != nil {
				s.adoptTrackerDisplayName(resp.DisplayName)
			}
			return nil
		}
		if s.logger != nil {
			s.logger.Warn("Daemon", "doHeartbeat", fmt.Sprintf("Heartbeat failed, falling back to register: %v", err), nil)
		}
	}
	// Fallback: full re-registration
	return s.RegisterWithTracker()
}

// StartTrackerHeartbeat starts a goroutine that re-registers with the tracker periodically
// so the daemon stays "online" (tracker presence TTL is 5 minutes; heartbeat every 2 min).
// Call after RegisterWithTracker(). Stop by closing s.trackerHeartbeatStop (e.g. in Shutdown).
func (s *Server) StartTrackerHeartbeat() {
	if s.trackerClient == nil || s.p2pHost == nil {
		return
	}
	if s.trackerHeartbeatStop != nil {
		return // already running
	}
	s.trackerHeartbeatStop = make(chan struct{})
	go func() {
		// Early re-register after 10s to pick up relay/external addrs discovered by AutoNAT
		earlyTimer := time.NewTimer(10 * time.Second)
		select {
		case <-s.trackerHeartbeatStop:
			earlyTimer.Stop()
			return
		case <-earlyTimer.C:
			if err := s.RegisterWithTracker(); err != nil && s.logger != nil {
				s.logger.Error("Daemon", "TrackerHeartbeat", fmt.Sprintf("Early re-register failed: %v", err), nil)
			} else if s.logger != nil {
				s.logger.Info("Daemon", "TrackerHeartbeat", "Early re-register completed (relay/external addr discovery)", nil)
			}
		}

		interval := defaultTrackerHeartbeatInterval
		if s.config != nil && s.config.TrackerHeartbeatInterval > 0 {
			interval = s.config.TrackerHeartbeatInterval
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-s.trackerHeartbeatStop:
				return
			case <-ticker.C:
				// TD-016: Use authenticated heartbeat when API key available, fallback to re-register
				if err := s.doHeartbeat(); err != nil && s.logger != nil {
					s.logger.Error("Daemon", "TrackerHeartbeat", fmt.Sprintf("Heartbeat/re-register failed: %v", err), nil)
				}
			}
		}
	}()
	if s.logger != nil {
		interval := defaultTrackerHeartbeatInterval
		if s.config != nil && s.config.TrackerHeartbeatInterval > 0 {
			interval = s.config.TrackerHeartbeatInterval
		}
		s.logger.Info("Daemon", "StartTrackerHeartbeat", "Tracker heartbeat started", map[string]interface{}{
			"interval_seconds": int(interval.Seconds()),
		})
	}
}

// GetPeerID returns the server's PeerID
func (s *Server) GetPeerID() string {
	if s.p2pHost == nil {
		return ""
	}
	return s.p2pHost.ID().String()
}

// StopP2P shuts down the P2P networking
func (s *Server) StopP2P() error {
	if s.p2pHost == nil {
		return nil
	}

	err := s.p2pHost.Close()
	s.p2pHost = nil

	if s.logger != nil {
		s.logger.Info("Server", "StopP2P", "P2P networking stopped", nil)
	}

	return err
}

// SubscribeTopic subscribes to a GossipSub topic
func (s *Server) SubscribeTopic(topic string) error {
	if s.p2pHost == nil {
		return fmt.Errorf("P2P host not initialized")
	}

	_, err := s.p2pHost.SubscribeTopic(topic)
	return err
}

// GetSubscriptions returns the list of subscribed topics
func (s *Server) GetSubscriptions() []string {
	if s.p2pHost == nil {
		return []string{}
	}

	return s.p2pHost.GetSubscriptions()
}

// ConnectToPeer connects to a specific peer
func (s *Server) ConnectToPeer(peerID peer.ID, addrs []string) error {
	if s.p2pHost == nil {
		return fmt.Errorf("P2P host not initialized")
	}

	return s.p2pHost.ConnectToPeer(peerID, addrs)
}

// contextKey is a custom type for context keys to avoid collisions.
type contextKey string

// correlationIDKey is the context key for the correlation ID.
const correlationIDKey contextKey = "correlationID"

// generateUUIDv4 generates a UUID v4 using crypto/rand.
func generateUUIDv4() string {
	var uuid [16]byte
	_, _ = rand.Read(uuid[:])
	// Set version 4 (bits 12-15 of time_hi_and_version)
	uuid[6] = (uuid[6] & 0x0f) | 0x40
	// Set variant bits (10xx for RFC 4122)
	uuid[8] = (uuid[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		uuid[0:4], uuid[4:6], uuid[6:8], uuid[8:10], uuid[10:16])
}

// correlationIDMiddleware ensures every request has a correlation ID.
// If the incoming request has an X-Request-ID header, it is preserved.
// Otherwise, a new UUID v4 is generated. The correlation ID is set on
// the response header and stored in the request context.
func correlationIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cid := r.Header.Get("X-Request-ID")
		if cid == "" {
			cid = generateUUIDv4()
		}
		w.Header().Set("X-Request-ID", cid)
		ctx := context.WithValue(r.Context(), correlationIDKey, cid)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// handleHealth handles GET /health requests
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	// Calculate uptime
	uptime := int64(time.Since(s.startTime).Seconds())

	// Get version from config, default to "dev" if not set
	version := "dev"
	if s.config != nil && s.config.Version != "" {
		version = s.config.Version
	}

	// Build response
	response := HealthResponse{
		Status:        "healthy",
		Version:       version,
		UptimeSeconds: uptime,
	}

	// Send JSON response
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(response)
}

// ReadinessResponse represents the readiness endpoint response
// Audit: C3 (Readiness Endpoint)
type ReadinessResponse struct {
	Status     string            `json:"status"`
	Components map[string]string `json:"components,omitempty"`
}

// handleReadiness handles GET /readiness requests.
// Returns 200 when all critical components (p2pHost, downloadManager, chunkStore)
// are initialized; returns 503 with component status when any are missing.
// Audit: C3 (Readiness Endpoint)
func (s *Server) handleReadiness(w http.ResponseWriter, r *http.Request) {
	components := map[string]string{
		"p2p_host":         componentStatus(s.p2pHost != nil),
		"download_manager": componentStatus(s.downloadManager != nil),
		"chunk_store":      componentStatus(s.chunkStore != nil),
	}

	allReady := s.p2pHost != nil && s.downloadManager != nil && s.chunkStore != nil

	w.Header().Set("Content-Type", "application/json")

	if allReady {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(ReadinessResponse{
			Status: "ready",
		})
	} else {
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(ReadinessResponse{
			Status:     "not_ready",
			Components: components,
		})
	}
}

// componentStatus returns "ready" or "not_ready" based on initialization state.
func componentStatus(initialized bool) string {
	if initialized {
		return "ready"
	}
	return "not_ready"
}
