// Package: internal/daemon
// Feature: F-010 (Go Core Daemon)
// Story: US-010-03 (REST API Endpoints)
// Purpose: REST API handlers - Built with TDD

package daemon

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/stonkagents/agent/internal/config"
	"github.com/stonkagents/agent/internal/daemon/agentchat"
	"github.com/stonkagents/agent/internal/daemon/chunking"
	"github.com/stonkagents/agent/internal/daemon/download"
	"github.com/stonkagents/agent/internal/daemon/history"
	"github.com/stonkagents/agent/internal/daemon/stats"
	"github.com/stonkagents/agent/internal/daemon/storage"
	"github.com/stonkagents/agent/internal/daemon/tracker"
	"github.com/stonkagents/agent/internal/daemon/upload"
	"github.com/stonkagents/agent/internal/installenv"
	"github.com/stonkagents/agent/internal/sharing"
	"github.com/stonkagents/agent/pkg/chatthread"
	"github.com/stonkagents/agent/pkg/protocol"
)

// proxyAllowedHeaders lists headers forwarded from tracker response to frontend client.
// TD-023: Rate limit headers must reach the frontend for proper UX (429 retry, budget display).
var proxyAllowedHeaders = []string{
	"Content-Type",
	"Cache-Control",
	"X-RateLimit-Limit",
	"X-RateLimit-Remaining",
	"X-RateLimit-Reset",
	"Retry-After",
	"X-Request-Id",
}

// handleInstallerPeerKey handles GET /api/v1/installer/peer-key.
// Localhost only (127.0.0.1, ::1). Returns peer API key and tracker_url for MSI post-install so StonkAgents can onboard with the same peer profile. 503 if daemon not yet registered.
func (s *Server) handleInstallerPeerKey(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.sendErrorResponse(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "GET only", nil)
		return
	}
	if !isLoopbackRequest(r) {
		s.sendErrorResponse(w, http.StatusForbidden, "FORBIDDEN", "Only localhost requests allowed", nil)
		return
	}
	apiKey := s.getTrackerAPIKey()
	if apiKey == "" {
		s.sendErrorResponse(w, http.StatusServiceUnavailable, "NOT_REGISTERED", "Daemon not yet registered with tracker", nil)
		return
	}
	trackerURL := ""
	if s.config != nil {
		trackerURL = strings.TrimSpace(s.config.TrackerURL)
	}
	s.sendJSONResponse(w, http.StatusOK, map[string]string{
		"api_key":     apiKey,
		"tracker_url": trackerURL,
	})
}

// ShareRequest represents a request to share an asset
type ShareRequest struct {
	CID      string `json:"cid"`
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
	Type     string `json:"type"`
}

// ShareResponse represents the response from share endpoint
type ShareResponse struct {
	CID     string `json:"cid"`
	Message string `json:"message"`
}

// handleShare handles POST /api/v1/share requests
// Accepts multipart/form-data file upload
func (s *Server) handleShare(w http.ResponseWriter, r *http.Request) {
	// Extend write deadline: chunking + tracker Announce can take time on slow/cross-network links
	if rc := http.NewResponseController(w); rc != nil {
		_ = rc.SetWriteDeadline(time.Now().Add(45 * time.Second))
	}
	// Parse multipart form
	if err := r.ParseMultipartForm(32 << 20); err != nil { // 32 MB max memory
		s.sendErrorResponse(w, http.StatusBadRequest, "INVALID_REQUEST", "Failed to parse multipart form", nil)
		return
	}

	// Get uploaded file
	file, header, err := r.FormFile("file")
	if err != nil {
		s.sendErrorResponse(w, http.StatusBadRequest, "INVALID_REQUEST", "No file uploaded", nil)
		return
	}
	defer file.Close()

	// SECURITY: Sanitize filename to prevent path traversal attacks
	// Use only the base filename (strips directory components)
	filename := filepath.Base(header.Filename)

	// SECURITY: Reject filenames with path traversal attempts
	if filename == "." || filename == ".." || filename == "" {
		s.sendErrorResponse(w, http.StatusBadRequest, "INVALID_FILENAME", "Invalid filename", nil)
		return
	}

	// SECURITY: Reject filenames containing null bytes
	if len(filename) != len([]byte(filename)) ||
		len(filename) > 255 ||
		strings.Contains(filename, "\x00") {
		s.sendErrorResponse(w, http.StatusBadRequest, "INVALID_FILENAME", "Invalid filename", nil)
		return
	}

	// Product rule: agents share knowledge, so only plain-text files travel the network.
	// Extension first (cheap, before anything touches disk); content sniff after the upload lands.
	if !sharing.AllowedExtension(filename) {
		s.sendUnsupportedFileType(w, sharing.ErrExtension)
		return
	}

	// Save uploaded file to temp location with sanitized filename
	tmpDir := filepath.Join(s.config.DataDir, "tmp")
	os.MkdirAll(tmpDir, 0755)
	tmpFilePath := filepath.Join(tmpDir, filename)

	// SECURITY: Verify the resolved path is still within tmpDir (defense in depth)
	absPath, err := filepath.Abs(tmpFilePath)
	absTmpDir, err2 := filepath.Abs(tmpDir)
	if err != nil || err2 != nil || !strings.HasPrefix(absPath, filepath.Clean(absTmpDir)+string(filepath.Separator)) {
		s.sendErrorResponse(w, http.StatusBadRequest, "INVALID_PATH", "Path traversal detected", nil)
		return
	}

	outFile, err := os.Create(tmpFilePath)
	if err != nil {
		s.sendErrorResponse(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to save uploaded file", nil)
		return
	}
	defer outFile.Close()
	defer os.Remove(tmpFilePath) // Clean up temp file

	totalSize, err := io.Copy(outFile, file)
	if err != nil {
		s.sendErrorResponse(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to write uploaded file", nil)
		return
	}
	outFile.Close()

	// Content sniff: UTF-8 with no NUL bytes in the first 8 KB, or the file is refused
	if err := sharing.CheckFile(filename, tmpFilePath); err != nil {
		if sharing.IsRefusal(err) {
			s.sendUnsupportedFileType(w, err)
			return
		}
		s.sendErrorResponse(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to inspect uploaded file", nil)
		return
	}

	// Chunk the file
	chunker := chunking.NewChunker(262144) // 256 KB chunks
	chunks, err := chunker.SplitFile(tmpFilePath)
	if err != nil {
		s.sendErrorResponse(w, http.StatusInternalServerError, "INTERNAL_ERROR", fmt.Sprintf("Failed to chunk file: %v", err), nil)
		return
	}

	// Calculate file CID from chunk CIDs
	fileCID := chunker.CalculateFileCID(chunks)

	// Duplicate detection: check if this CID already exists in the store
	forceUpload := r.FormValue("force") == "true"
	if !forceUpload {
		if existing, err := s.chunkStore.GetFile(fileCID); err == nil && existing != nil {
			s.sendErrorResponse(w, http.StatusConflict, "DUPLICATE_CONTENT", "File already shared", map[string]interface{}{
				"cid":      existing.CID,
				"filename": existing.Filename,
				"size":     existing.TotalSize,
			})
			return
		}
	}

	// Store file metadata
	err = s.chunkStore.StoreFile(fileCID, filename, totalSize, len(chunks), nil)
	if err != nil {
		s.sendErrorResponse(w, http.StatusInternalServerError, "INTERNAL_ERROR", fmt.Sprintf("Failed to store file metadata: %v", err), nil)
		return
	}

	// Read and store each chunk
	srcFile, err := os.Open(tmpFilePath)
	if err != nil {
		s.sendErrorResponse(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to read chunks", nil)
		return
	}
	defer srcFile.Close()

	for _, chunk := range chunks {
		// Read chunk data
		chunkData := make([]byte, chunk.Size)
		_, err := srcFile.ReadAt(chunkData, chunk.Offset)
		if err != nil {
			s.sendErrorResponse(w, http.StatusInternalServerError, "INTERNAL_ERROR", fmt.Sprintf("Failed to read chunk %d", chunk.Index), nil)
			return
		}

		// Store chunk
		err = s.chunkStore.StoreChunk(fileCID, chunk.Index, chunk.CID, chunkData)
		if err != nil {
			s.sendErrorResponse(w, http.StatusInternalServerError, "INTERNAL_ERROR", fmt.Sprintf("Failed to store chunk %d", chunk.Index), nil)
			return
		}
	}

	// Announce to tracker (advertise that we have all chunks)
	chunkIndices := make([]int, len(chunks))
	for i := range chunks {
		chunkIndices[i] = i
	}

	mimeType := detectMimeType(tmpFilePath)
	manifestType := detectManifestType(filename)
	err = s.trackerClient.Announce(fileCID, filename, mimeType, manifestType, totalSize, chunkIndices)
	if err != nil {
		// Log error but don't fail the request (tracker is optional for MVP)
		if s.logger != nil {
			s.logger.Error("API", "handleShare", "Failed to announce to tracker", map[string]interface{}{
				"error": err.Error(),
				"cid":   fileCID,
			})
		}
	}

	// Return response
	response := ShareResponse{
		CID:     fileCID,
		Message: "Asset shared successfully",
	}

	s.sendJSONResponse(w, http.StatusCreated, response)

	// DHT content routing: announce we provide this CID so peers can find us without tracker (BitTorrent-style)
	if s.p2pHost != nil {
		go func(cid string) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_ = s.p2pHost.ProvideContent(ctx, cid)
		}(fileCID)
	}

	if s.logger != nil {
		s.logger.Info("API", "handleShare", "Asset shared", map[string]interface{}{
			"cid":      fileCID,
			"filename": filename,
			"size":     totalSize,
			"chunks":   len(chunks),
		})
	}
}

// seedLibraryFromDiskFile re-chunks a file on disk, verifies CID, persists chunks for P2P seeding, announces to tracker + DHT.
// Used after a completed download (assembled file under downloads/assets/{cid}/).
func (s *Server) seedLibraryFromDiskFile(absPath, expectedCID, displayFilename string) error {
	if s.chunkStore == nil {
		return fmt.Errorf("chunk store not initialized")
	}
	// Same plain-text rule as POST /api/v1/share: a downloaded binary is not re-seeded
	if err := sharing.CheckFile(displayFilename, absPath); err != nil {
		return err
	}
	chunker := chunking.NewChunker(262144)
	chunks, err := chunker.SplitFile(absPath)
	if err != nil {
		return fmt.Errorf("chunk file: %w", err)
	}
	fileCID := chunker.CalculateFileCID(chunks)
	if fileCID != expectedCID {
		return fmt.Errorf("CID mismatch: expected %s, computed %s", expectedCID, fileCID)
	}

	fi, err := os.Stat(absPath)
	if err != nil {
		return fmt.Errorf("stat assembled file: %w", err)
	}
	totalSize := fi.Size()

	if err := s.chunkStore.StoreFile(fileCID, displayFilename, totalSize, len(chunks), nil); err != nil {
		return fmt.Errorf("store file metadata: %w", err)
	}

	srcFile, err := os.Open(absPath)
	if err != nil {
		return fmt.Errorf("open assembled file: %w", err)
	}
	defer srcFile.Close()

	for _, ch := range chunks {
		if s.isStopping() {
			return fmt.Errorf("daemon shutting down; seeding of %s aborted", fileCID)
		}
		chunkData := make([]byte, ch.Size)
		if _, err := srcFile.ReadAt(chunkData, ch.Offset); err != nil {
			return fmt.Errorf("read chunk %d: %w", ch.Index, err)
		}
		if err := s.chunkStore.StoreChunk(fileCID, ch.Index, ch.CID, chunkData); err != nil {
			return fmt.Errorf("store chunk %d: %w", ch.Index, err)
		}
	}

	chunkIndices := make([]int, len(chunks))
	for i := range chunks {
		chunkIndices[i] = i
	}
	mimeType := detectMimeType(absPath)
	manifestType := detectManifestType(displayFilename)
	if s.trackerClient != nil {
		if err := s.trackerClient.Announce(fileCID, displayFilename, mimeType, manifestType, totalSize, chunkIndices); err != nil && s.logger != nil {
			s.logger.Error("API", "seedLibraryFromDiskFile", "Failed to announce to tracker", map[string]interface{}{
				"error": err.Error(),
				"cid":   fileCID,
			})
		}
	}

	if s.p2pHost != nil {
		go func(cid string) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_ = s.p2pHost.ProvideContent(ctx, cid)
		}(fileCID)
	}

	if s.logger != nil {
		s.logger.Info("API", "seedLibraryFromDiskFile", "Seeding started from assembled download", map[string]interface{}{
			"cid": fileCID, "filename": displayFilename, "size": totalSize, "chunks": len(chunks),
		})
	}
	return nil
}

// findAssembledDownloadPath returns the absolute path and filename for a completed download under downloads/assets/{cid}/.
func (s *Server) findAssembledDownloadPath(cid string) (absPath string, filename string, err error) {
	if s.config == nil || s.config.DataDir == "" {
		return "", "", fmt.Errorf("data dir not configured")
	}
	assetsDir := filepath.Join(s.config.DataDir, "downloads", "assets", cid)
	ents, err := os.ReadDir(assetsDir)
	if err != nil {
		return "", "", fmt.Errorf("no assembled file for cid: %w", err)
	}
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		return filepath.Join(assetsDir, name), name, nil
	}
	return "", "", fmt.Errorf("no file in assets directory")
}

// handleSeedDownloadByPath handles POST /api/v1/downloads/{cid}/seed — promote assembled download to library + swarm announce.
func (s *Server) handleSeedDownloadByPath(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.sendErrorResponse(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "POST only", nil)
		return
	}
	cid := r.PathValue("cid")
	if err := download.ValidateCID(cid); err != nil {
		s.sendErrorResponse(w, http.StatusBadRequest, "VALIDATION_ERROR", "Invalid CID format", nil)
		return
	}
	if s.chunkStore == nil {
		s.sendErrorResponse(w, http.StatusServiceUnavailable, "NOT_READY", "Storage not initialized", nil)
		return
	}
	if existing, err := s.chunkStore.GetFile(cid); err == nil && existing != nil {
		s.sendJSONResponse(w, http.StatusOK, map[string]string{
			"cid": cid, "status": "already_seeding", "message": "Already in library",
		})
		return
	}

	absPath, filename, err := s.findAssembledDownloadPath(cid)
	if err != nil {
		s.sendErrorResponse(w, http.StatusNotFound, "NOT_FOUND", "Assembled file not found; complete a download first", nil)
		return
	}

	if rc := http.NewResponseController(w); rc != nil {
		_ = rc.SetWriteDeadline(time.Now().Add(45 * time.Second))
	}
	if err := s.seedLibraryFromDiskFile(absPath, cid, filename); err != nil {
		if s.logger != nil {
			s.logger.Error("API", "handleSeedDownloadByPath", err.Error(), map[string]interface{}{"cid": cid})
		}
		if sharing.IsRefusal(err) {
			s.sendUnsupportedFileType(w, err)
			return
		}
		s.sendErrorResponse(w, http.StatusBadRequest, "SEED_FAILED", err.Error(), nil)
		return
	}

	s.sendJSONResponse(w, http.StatusCreated, map[string]string{
		"cid": cid, "status": "seeding", "message": "Now seeding to the swarm",
	})
}

// sendUnsupportedFileType answers 415 UNSUPPORTED_FILE_TYPE with the plain-text rule and why the file failed it.
func (s *Server) sendUnsupportedFileType(w http.ResponseWriter, reason error) {
	s.sendErrorResponse(w, http.StatusUnsupportedMediaType, sharing.ErrorCode, sharing.RuleMessage, map[string]interface{}{
		"reason":             reason.Error(),
		"allowed_extensions": sharing.AllowedExtensions(),
	})
}

// detectMimeType returns a best-effort MIME type for a file.
func detectMimeType(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return "application/octet-stream"
	}
	defer file.Close()

	buf := make([]byte, 512)
	n, _ := file.Read(buf)
	if n == 0 {
		return "application/octet-stream"
	}
	return http.DetectContentType(buf[:n])
}

// detectManifestType returns the manifest type based on file extension.
// Supports: vec, traj, claw-skill, claw-prompt, claw-memory, claw-workflow, claw-context, claw-tool.
// Unknown extensions fall back to "raw".
func detectManifestType(filename string) string {
	lower := strings.ToLower(filename)
	switch {
	case strings.HasSuffix(lower, ".npy"),
		strings.HasSuffix(lower, ".pt"),
		strings.HasSuffix(lower, ".bin"),
		strings.HasSuffix(lower, ".vec"):
		return "vec"
	case strings.HasSuffix(lower, ".traj"):
		return "traj"
	case strings.HasSuffix(lower, ".claw-skill"):
		return "claw-skill"
	case strings.HasSuffix(lower, ".claw-prompt"):
		return "claw-prompt"
	case strings.HasSuffix(lower, ".claw-memory"):
		return "claw-memory"
	case strings.HasSuffix(lower, ".claw-workflow"):
		return "claw-workflow"
	case strings.HasSuffix(lower, ".claw-context"):
		return "claw-context"
	case strings.HasSuffix(lower, ".claw-tool"):
		return "claw-tool"
	default:
		return "raw"
	}
}

// sendJSONResponse sends a JSON response
func (s *Server) sendJSONResponse(w http.ResponseWriter, statusCode int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(data)
}

// ErrorResponse represents a consistent error response
type ErrorResponse struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail contains error details
type ErrorDetail struct {
	Code    string      `json:"code"`
	Message string      `json:"message"`
	Details interface{} `json:"details,omitempty"`
}

// sendErrorResponse sends a consistent error response
func (s *Server) sendErrorResponse(w http.ResponseWriter, statusCode int, code string, message string, details interface{}) {
	response := ErrorResponse{
		Error: ErrorDetail{
			Code:    code,
			Message: message,
			Details: details,
		},
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(response)
}

// askNotConfiguredDiagnostics explains 503 ASK_NOT_CONFIGURED (e.g. daemon loaded a different config than the user edited).
func (s *Server) askNotConfiguredDiagnostics() map[string]interface{} {
	cfgPath := ""
	if p, err := config.ConfigPath(); err == nil {
		cfgPath = p
	}
	out := map[string]interface{}{
		"config_file_resolved": cfgPath,
		"hint":                 "The daemon reads the config path from its service environment, else HOME/.stonkagents/config.yaml; this is not necessarily the install directory.",
	}
	if s.config != nil {
		out["ask_use_tracker"] = s.config.AskUseTracker
		out["tracker_url_set"] = strings.TrimSpace(s.config.TrackerURL) != ""
		out["gateway_url_set"] = strings.TrimSpace(s.config.GatewayURL) != ""
	}
	out["tracker_api_key_loaded"] = s.getTrackerAPIKey() != ""
	return out
}

// SearchResponse represents search results
// Audit C1: Aligned with SDK expected shape (data, total, page, pageSize)
type SearchResponse struct {
	Data     []SearchResult `json:"data"`
	Total    int            `json:"total"`
	Page     int            `json:"page"`
	PageSize int            `json:"pageSize"`
}

// SearchResult represents a single search result
type SearchResult struct {
	CID      string `json:"cid"`
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
	Type     string `json:"type"`
}

// handleSearch handles GET /api/v1/search requests
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	// Get query parameters
	query := r.URL.Query().Get("q")

	// Parse pagination parameters with sensible defaults
	page := 1
	pageSize := 20
	if p := r.URL.Query().Get("page"); p != "" {
		if parsed, err := strconv.Atoi(p); err == nil && parsed > 0 {
			page = parsed
		}
	}
	if ps := r.URL.Query().Get("pageSize"); ps != "" {
		if parsed, err := strconv.Atoi(ps); err == nil && parsed > 0 {
			pageSize = parsed
		}
	}

	// MVP: Return empty results
	// Production: Would query tracker for matching assets
	response := SearchResponse{
		Data:     []SearchResult{},
		Total:    0,
		Page:     page,
		PageSize: pageSize,
	}

	s.sendJSONResponse(w, http.StatusOK, response)

	if s.logger != nil {
		s.logger.Info("API", "handleSearch", "Search performed", map[string]interface{}{
			"query": query,
		})
	}
}

// AskRequest is the body for POST /api/v1/ask (stateless prompt → gateway → response).
type AskRequest struct {
	Prompt string `json:"prompt"`
}

// AskResponse is the response for POST /api/v1/ask.
type AskResponse struct {
	Response string `json:"response"`
}

// AgentChatMessage is one message in an agent conversation (role + content; optional agent_id for LLM context).
type AgentChatMessage struct {
	Role    string `json:"role"`    // "system", "user", or "assistant"
	Content string `json:"content"` // required
	AgentID string `json:"agent_id,omitempty"`
}

// AgentChatRequest is the body for POST /api/v1/agent/chat. Used for (1) main agent chat and (2) token chat.
// Token chat: owner = token creator; holder = buyer. Only holders chat with the owner's LLM (owner cannot chat with their own).
// UserID = chatter (holder wallet in token chat). ContextID = token contract (which owner's LLM); empty for main agent chat.
type AgentChatRequest struct {
	SessionID    string             `json:"session_id,omitempty"`
	UserID       string             `json:"user_id,omitempty"`
	ContextID    string             `json:"context_id,omitempty"`
	Messages     []AgentChatMessage `json:"messages"`
	SystemPrompt string             `json:"system_prompt,omitempty"`
	Model        string             `json:"model,omitempty"`
}

// AgentChatResponse is the response for POST /api/v1/agent/chat. SessionID and CreditsDeducted are set when session persistence is enabled.
type AgentChatResponse struct {
	Response        string `json:"response"`
	SessionID       string `json:"session_id,omitempty"`
	CreditsDeducted int    `json:"credits_deducted,omitempty"` // credits used for this turn (e.g. 10 when tracker path)
}

// AgentChatHistoryResponse is the response for GET /api/v1/agent/chat/history/{session_id}.
type AgentChatHistoryResponse struct {
	Session  *agentchat.Session   `json:"session"`
	Messages []*agentchat.Message `json:"messages"`
}

// AgentChatSessionsResponse is the response for GET /api/v1/agent/chat/sessions.
type AgentChatSessionsResponse struct {
	Sessions []*agentchat.Session `json:"sessions"`
}

// openAICompletionResponse is the shape returned by gateway POST /v1/chat/completions (stream: false).
type openAICompletionResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// agentChatTrackerFailureAllowsGateway is true when gateway_url may be used after tracker POST /agents/completions fails.
func agentChatTrackerFailureAllowsGateway(status int, code string) bool {
	if status == http.StatusUnauthorized {
		return true
	}
	if status == http.StatusBadGateway && (code == "TRACKER_UNREACHABLE" || code == "TRACKER_READ_FAILED") {
		return true
	}
	return false
}

// runAsk runs the ask flow (tracker then gateway) for a single prompt. Returns (content, 0, "", "") on success, or ("", status, code, msg) on error.
// Used by handleAsk and by owner token-chat path (callAskViaDaemon) so logic is not duplicated.
func (s *Server) runAsk(ctx context.Context, prompt string) (content string, status int, code, msg string) {
	if s.config != nil && s.config.AskUseTracker && strings.TrimSpace(s.config.TrackerURL) != "" {
		apiKey := s.getTrackerAPIKey()
		if apiKey == "" {
			return "", http.StatusServiceUnavailable, "NOT_REGISTERED",
				"ask_use_tracker is enabled but the daemon has no tracker API key; register with the tracker or disable ask_use_tracker to use gateway_url."
		}
		content, status, code, msg := s.handleAskViaTracker(ctx, prompt, apiKey)
		if status == 0 {
			return content, 0, "", ""
		}
		if agentChatTrackerFailureAllowsGateway(status, code) && s.config != nil && strings.TrimSpace(s.config.GatewayURL) != "" {
			if s.logger != nil {
				s.logger.Warn("Server", "runAsk", "Tracker completions failed; falling back to gateway", map[string]interface{}{
					"tracker_code": code, "http_status": status,
				})
			}
		} else {
			return "", status, code, msg
		}
	}

	// Local gateway: used when ask_use_tracker is off, or as fallback after allowed tracker failures above.
	if s.config == nil || s.config.GatewayURL == "" {
		return "", http.StatusServiceUnavailable, "ASK_NOT_CONFIGURED",
			"No gateway_url in the loaded config and no tracker API key for ask_use_tracker; see error.details for what the daemon sees."
	}

	gatewayURL := s.config.GatewayURL + "/v1/chat/completions"
	body := map[string]interface{}{
		"messages": []map[string]string{{"role": "user", "content": prompt}},
		"stream":   false,
	}
	bodyBytes, _ := json.Marshal(body)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, gatewayURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", http.StatusInternalServerError, "GATEWAY_REQUEST_FAILED", "Failed to create request"
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if s.config.GatewayToken != "" {
		httpReq.Header.Set("Authorization", "Bearer "+s.config.GatewayToken)
	}
	// 5 minutes for reasoning-class models (gpt-5.x, o-series).
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(httpReq)
	if err != nil {
		return "", http.StatusBadGateway, "GATEWAY_UNREACHABLE", "Upstream service unavailable"
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", http.StatusBadGateway, "GATEWAY_READ_FAILED", "Failed to read gateway response"
	}
	var gw openAICompletionResponse
	if err := json.Unmarshal(respBody, &gw); err != nil {
		return "", http.StatusBadGateway, "GATEWAY_INVALID_RESPONSE", "Invalid JSON from gateway"
	}
	if gw.Error != nil && gw.Error.Message != "" {
		return "", http.StatusBadGateway, "GATEWAY_ERROR", gw.Error.Message
	}
	if resp.StatusCode != http.StatusOK {
		return "", http.StatusBadGateway, "GATEWAY_ERROR", fmt.Sprintf("gateway returned %d", resp.StatusCode)
	}
	content = ""
	if len(gw.Choices) > 0 {
		content = strings.TrimSpace(gw.Choices[0].Message.Content)
	}
	if content == "" {
		content = "No response from gateway."
	}
	return content, 0, "", ""
}

// handleAsk handles POST /api/v1/ask. Delegates to runAsk (tracker then gateway).
func (s *Server) handleAsk(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.sendErrorResponse(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "POST required", nil)
		return
	}
	var req AskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.sendErrorResponse(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid JSON body", nil)
		return
	}
	req.Prompt = strings.TrimSpace(req.Prompt)
	if req.Prompt == "" {
		s.sendErrorResponse(w, http.StatusBadRequest, "MISSING_PROMPT", "prompt is required", nil)
		return
	}
	content, status, code, msg := s.runAsk(r.Context(), req.Prompt)
	if status == 0 {
		s.sendJSONResponse(w, http.StatusOK, AskResponse{Response: content})
		return
	}
	details := interface{}(nil)
	if code == "ASK_NOT_CONFIGURED" {
		details = s.askNotConfiguredDiagnostics()
	}
	s.sendErrorResponse(w, status, code, msg, details)
}

// maxAgentChatMessages caps conversation length to avoid abuse.
const maxAgentChatMessages = 50

// agentChatCreditsPerCompletion matches tracker's cost per agents/completions call (for persistence/audit).
const agentChatCreditsPerCompletion = 10

// buildLLMMessages converts AgentChatRequest into OpenAI-style messages (role + content). Optional agent_id is prefixed to content for context.
// agentChatHistoryCap is the maximum number of conversation turns (user + assistant
// messages, excluding the system prompt) sent to the upstream LLM. Beyond this,
// the oldest turns are silently dropped so input token cost stays bounded —
// preserves margin on long conversations without summarisation.
const agentChatHistoryCap = 10

func buildLLMMessages(req AgentChatRequest) []map[string]string {
	var out []map[string]string
	if req.SystemPrompt != "" {
		out = append(out, map[string]string{"role": "system", "content": strings.TrimSpace(req.SystemPrompt)})
	}
	// Convert all messages first, then trim to the last N (excluding system prompt).
	convo := make([]map[string]string, 0, len(req.Messages))
	for _, m := range req.Messages {
		content := strings.TrimSpace(m.Content)
		if content == "" {
			continue
		}
		role := strings.ToLower(strings.TrimSpace(m.Role))
		if role != "system" && role != "user" && role != "assistant" {
			role = "user"
		}
		if m.AgentID != "" {
			content = "[" + strings.TrimSpace(m.AgentID) + "]: " + content
		}
		convo = append(convo, map[string]string{"role": role, "content": content})
	}
	// Sliding window — keep most recent agentChatHistoryCap turns.
	if len(convo) > agentChatHistoryCap {
		convo = convo[len(convo)-agentChatHistoryCap:]
	}
	out = append(out, convo...)
	return out
}

// dbMessagesToLLM converts persisted agentchat messages to OpenAI-style slice for the completion request.
func dbMessagesToLLM(msgs []*agentchat.Message) []map[string]string {
	out := make([]map[string]string, 0, len(msgs))
	for _, m := range msgs {
		content := m.Content
		if m.AgentID != "" {
			content = "[" + m.AgentID + "]: " + content
		}
		out = append(out, map[string]string{"role": m.Role, "content": content})
	}
	return out
}

// handleAgentChat handles POST /api/v1/agent/chat. Persists session history when agentChatRepo is set; deducts credits via tracker when configured.
func (s *Server) handleAgentChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.sendErrorResponse(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "POST required", nil)
		return
	}

	limitedBody := io.LimitReader(r.Body, 256*1024) // 256 KB max request body
	var req AgentChatRequest
	if err := json.NewDecoder(limitedBody).Decode(&req); err != nil {
		s.sendErrorResponse(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid JSON body", nil)
		return
	}

	if len(req.Messages) == 0 {
		s.sendErrorResponse(w, http.StatusBadRequest, "MISSING_MESSAGES", "messages array is required and must not be empty", nil)
		return
	}
	if len(req.Messages) > maxAgentChatMessages {
		s.sendErrorResponse(w, http.StatusBadRequest, "TOO_MANY_MESSAGES",
			fmt.Sprintf("messages count must not exceed %d", maxAgentChatMessages), nil)
		return
	}

	// Resolve history identity:
	// - Personal + tracker API key: stable session id only; history read/written on tracker Postgres.
	// - Token chat: user_id + context_id → SQLite EnsureUserTokenSession when local DB exists.
	// - Explicit session_id: load SQLite session when local DB exists.
	req.UserID = strings.TrimSpace(req.UserID)
	req.ContextID = strings.TrimSpace(req.ContextID)
	req.SessionID = strings.TrimSpace(req.SessionID)

	personalTracker := req.ContextID == "" && req.UserID != "" && s.useTrackerForPersonalChatHistory()

	var sessionID string
	if personalTracker {
		sessionID = agentchat.StablePersonalSessionID(req.UserID)
	} else if s.agentChatRepo != nil {
		if req.ContextID != "" && req.UserID != "" {
			systemPrompt := strings.TrimSpace(req.SystemPrompt)
			if systemPrompt == "" {
				systemPrompt = defaultMultiAgentSystemPrompt
			}
			mappedID, err := s.agentChatRepo.EnsureUserTokenSession(systemPrompt, req.UserID, req.ContextID)
			if err != nil {
				if s.logger != nil {
					s.logger.Error("Server", "handleAgentChat", fmt.Sprintf("Failed to ensure mapped session: %v", err), nil)
				}
				s.sendErrorResponse(w, http.StatusInternalServerError, "SESSION_ERROR", "Failed to initialize chat history", nil)
				return
			}
			sessionID = mappedID
		} else if req.SessionID != "" {
			sess, err := s.agentChatRepo.GetSession(req.SessionID)
			if err != nil {
				if s.logger != nil {
					s.logger.Error("Server", "handleAgentChat", fmt.Sprintf("Failed to get session: %v", err), nil)
				}
				s.sendErrorResponse(w, http.StatusInternalServerError, "SESSION_ERROR", "Failed to load session", nil)
				return
			}
			if sess == nil {
				s.sendErrorResponse(w, http.StatusNotFound, "SESSION_NOT_FOUND", "session_id not found", nil)
				return
			}
			if req.UserID != "" && sess.UserID != req.UserID {
				s.sendErrorResponse(w, http.StatusForbidden, "FORBIDDEN", "session does not belong to this user", nil)
				return
			}
			if req.ContextID != "" && sess.ContextID != req.ContextID {
				s.sendErrorResponse(w, http.StatusForbidden, "FORBIDDEN", "session does not belong to this token", nil)
				return
			}
			sessionID = sess.ID
		} else if req.UserID != "" && req.ContextID == "" {
			systemPrompt := strings.TrimSpace(req.SystemPrompt)
			if systemPrompt == "" {
				systemPrompt = defaultMultiAgentSystemPrompt
			}
			mappedID, err := s.agentChatRepo.EnsureUserTokenSession(systemPrompt, req.UserID, "")
			if err != nil {
				if s.logger != nil {
					s.logger.Error("Server", "handleAgentChat", fmt.Sprintf("Failed to ensure personal chat session: %v", err), nil)
				}
				s.sendErrorResponse(w, http.StatusInternalServerError, "SESSION_ERROR", "Failed to initialize chat history", nil)
				return
			}
			sessionID = mappedID
		}
	}

	var messages []map[string]string
	if personalTracker {
		sys := strings.TrimSpace(req.SystemPrompt)
		if sys == "" {
			sys = defaultMultiAgentSystemPrompt
		}
		if sys != "" {
			messages = append(messages, map[string]string{"role": "system", "content": sys})
		}
		hist, st, code, msg := s.getChatHistoryFromTracker(r.Context(), req.UserID, chatthread.PersonalContextTokenAddress)
		if st != 0 {
			s.sendErrorResponse(w, st, code, msg, nil)
			return
		}
		if raw, ok := hist["messages"]; ok {
			messages = append(messages, trackerHistoryMessagesToLLM(raw)...)
		}
		for _, m := range buildLLMMessages(req) {
			messages = append(messages, m)
		}
	} else if s.agentChatRepo != nil && sessionID != "" {
		sess, _ := s.agentChatRepo.GetSession(sessionID)
		if sess != nil && strings.TrimSpace(sess.SystemPrompt) != "" {
			messages = append(messages, map[string]string{"role": "system", "content": strings.TrimSpace(sess.SystemPrompt)})
		}
		existing, err := s.agentChatRepo.ListMessages(sessionID)
		if err != nil {
			if s.logger != nil {
				s.logger.Error("Server", "handleAgentChat", fmt.Sprintf("Failed to list messages: %v", err), nil)
			}
			s.sendErrorResponse(w, http.StatusInternalServerError, "SESSION_ERROR", "Failed to load session history", nil)
			return
		}
		messages = append(messages, dbMessagesToLLM(existing)...)
		for _, m := range buildLLMMessages(req) {
			messages = append(messages, m)
		}
	} else {
		messages = buildLLMMessages(req)
		if len(messages) > 0 && (messages[0]["role"] != "system") && strings.TrimSpace(req.SystemPrompt) == "" {
			messages = append([]map[string]string{{"role": "system", "content": defaultMultiAgentSystemPrompt}}, messages...)
		}
	}
	if len(messages) == 0 {
		s.sendErrorResponse(w, http.StatusBadRequest, "MISSING_CONTENT", "all messages have empty content", nil)
		return
	}

	var content string
	creditsDeducted := 0

	// Token chat: try direct P2P when both online (holder → owner libp2p); else fallback to tracker relay.
	if req.ContextID != "" {
		if s.config == nil || strings.TrimSpace(s.config.TrackerURL) == "" {
			s.sendErrorResponse(w, http.StatusServiceUnavailable, "TRACKER_REQUIRED", "Token chat requires tracker_url for relay", nil)
			return
		}
		apiKey := s.getTrackerAPIKey()
		if apiKey == "" {
			s.sendErrorResponse(w, http.StatusServiceUnavailable, "NOT_REGISTERED", "Daemon must be registered with tracker for token chat", nil)
			return
		}
		ownerWallet, status, code, msg := s.fetchTokenOwnerWallet(r.Context(), req.ContextID, apiKey)
		if status != 0 {
			s.sendErrorResponse(w, status, code, msg, nil)
			return
		}
		if ownerWallet != "" && strings.EqualFold(strings.TrimSpace(ownerWallet), req.UserID) {
			s.sendErrorResponse(w, http.StatusForbidden, "OWNER_SELF_CHAT_NOT_ALLOWED", "owner cannot chat with own token agent", nil)
			return
		}

		content, status, code, msg := s.handleAgentChatViaDirectP2P(r.Context(), &req, messages, apiKey)
		if status == 0 {
			sessionID = s.ensureAgentChatSession(sessionID, &req)
			s.persistAgentChatTurn(sessionID, &req, content, creditsDeducted)
			s.sendJSONResponse(w, http.StatusOK, AgentChatResponse{Response: content, SessionID: sessionID, CreditsDeducted: creditsDeducted})
			return
		}
		// Fallback: relay via tracker (owner polls pending when back online)
		content, creditsDeducted, status, code, msg = s.handleAgentChatViaRelay(r.Context(), &req, messages, apiKey)
		if status == 0 {
			sessionID = s.ensureAgentChatSession(sessionID, &req)
			s.persistAgentChatTurn(sessionID, &req, content, creditsDeducted)
			s.sendJSONResponse(w, http.StatusOK, AgentChatResponse{Response: content, SessionID: sessionID, CreditsDeducted: creditsDeducted})
			return
		}
		s.sendErrorResponse(w, status, code, msg, nil)
		return
	}

	// Main agent chat: tracker when ask_use_tracker + tracker URL; local gateway only if tracker is not required or after an allowed tracker failure.
	if s.config != nil && s.config.AskUseTracker && strings.TrimSpace(s.config.TrackerURL) != "" {
		apiKey := s.getTrackerAPIKey()
		if apiKey == "" {
			s.sendErrorResponse(w, http.StatusServiceUnavailable, "NOT_REGISTERED",
				"ask_use_tracker is enabled but the daemon has no tracker API key; register with the tracker or disable ask_use_tracker to use gateway_url.", nil)
			return
		}
		c, deducted, st, cd, m := s.handleAgentChatViaTracker(r.Context(), messages, apiKey, req.Model)
		if st == 0 {
			content = c
			creditsDeducted = deducted
			sessionID = s.ensureAgentChatSession(sessionID, &req)
			s.persistAgentChatTurn(sessionID, &req, content, creditsDeducted)
			s.sendJSONResponse(w, http.StatusOK, AgentChatResponse{Response: content, SessionID: sessionID, CreditsDeducted: creditsDeducted})
			return
		}
		if agentChatTrackerFailureAllowsGateway(st, cd) && strings.TrimSpace(s.config.GatewayURL) != "" {
			if s.logger != nil {
				s.logger.Warn("Server", "handleAgentChat", "Tracker completions failed; falling back to gateway", map[string]interface{}{
					"tracker_code": cd, "http_status": st,
				})
			}
		} else {
			s.sendErrorResponse(w, st, cd, m, nil)
			return
		}
	}

	if s.config == nil || s.config.GatewayURL == "" {
		s.sendErrorResponse(w, http.StatusServiceUnavailable, "ASK_NOT_CONFIGURED",
			"No gateway_url in the loaded config and no tracker API key for ask_use_tracker; see error.details for what the daemon sees.",
			s.askNotConfiguredDiagnostics())
		return
	}

	gatewayURL := s.config.GatewayURL + "/v1/chat/completions"
	body := map[string]interface{}{
		"messages": messages,
		"stream":   false,
	}
	if strings.TrimSpace(req.Model) != "" {
		body["model"] = strings.TrimSpace(req.Model)
	}
	bodyBytes, _ := json.Marshal(body)
	httpReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, gatewayURL, bytes.NewReader(bodyBytes))
	if err != nil {
		if s.logger != nil {
			s.logger.Error("Server", "handleAgentChat", fmt.Sprintf("Failed to create gateway request: %v", err), nil)
		}
		s.sendErrorResponse(w, http.StatusInternalServerError, "GATEWAY_REQUEST_FAILED", "Gateway request failed", nil)
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if s.config.GatewayToken != "" {
		httpReq.Header.Set("Authorization", "Bearer "+s.config.GatewayToken)
	}

	// 5 minutes for reasoning-class models (gpt-5.x, o-series).
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(httpReq)
	if err != nil {
		if s.logger != nil {
			s.logger.Error("Server", "handleAgentChat", fmt.Sprintf("Gateway request failed: %v", err), nil)
		}
		s.sendErrorResponse(w, http.StatusBadGateway, "GATEWAY_UNREACHABLE", "Upstream service unavailable", nil)
		return
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		if s.logger != nil {
			s.logger.Error("Server", "handleAgentChat", fmt.Sprintf("Failed to read gateway response: %v", err), nil)
		}
		s.sendErrorResponse(w, http.StatusBadGateway, "GATEWAY_READ_FAILED", "Failed to read gateway response", nil)
		return
	}
	var gw openAICompletionResponse
	if err := json.Unmarshal(respBody, &gw); err != nil {
		s.sendErrorResponse(w, http.StatusBadGateway, "GATEWAY_INVALID_RESPONSE", "Invalid JSON from gateway", nil)
		return
	}
	if gw.Error != nil && gw.Error.Message != "" {
		s.sendErrorResponse(w, http.StatusBadGateway, "GATEWAY_ERROR", gw.Error.Message, nil)
		return
	}
	if resp.StatusCode != http.StatusOK {
		s.sendErrorResponse(w, http.StatusBadGateway, "GATEWAY_ERROR",
			fmt.Sprintf("gateway returned %d", resp.StatusCode), nil)
		return
	}
	content = ""
	if len(gw.Choices) > 0 {
		content = strings.TrimSpace(gw.Choices[0].Message.Content)
	}
	if content == "" {
		content = "No response from gateway."
	}
	sessionID = s.ensureAgentChatSession(sessionID, &req)
	s.persistAgentChatTurn(sessionID, &req, content, creditsDeducted)
	s.sendJSONResponse(w, http.StatusOK, AgentChatResponse{Response: content, SessionID: sessionID, CreditsDeducted: creditsDeducted})
}

// defaultMultiAgentSystemPrompt is used when creating a new session and the client did not send a system_prompt. Helps the LLM address each agent's questions in multi-agent conversations.
const defaultMultiAgentSystemPrompt = "You are in a multi-agent conversation. Messages may be prefixed with [agent_id] to show who spoke. Answer the latest message; your reply is for all agents in the session. Be concise and direct."

// ensureAgentChatSession creates a new session when persistence is enabled and we don't have one yet (after successful LLM call).
func (s *Server) ensureAgentChatSession(sessionID string, req *AgentChatRequest) string {
	if s.agentChatRepo == nil || sessionID != "" {
		return sessionID
	}
	if strings.TrimSpace(req.ContextID) != "" && strings.TrimSpace(req.UserID) != "" {
		systemPrompt := strings.TrimSpace(req.SystemPrompt)
		if systemPrompt == "" {
			systemPrompt = defaultMultiAgentSystemPrompt
		}
		id, err := s.agentChatRepo.EnsureUserTokenSession(systemPrompt, req.UserID, req.ContextID)
		if err != nil {
			if s.logger != nil {
				s.logger.Error("Server", "ensureAgentChatSession", fmt.Sprintf("Failed to ensure mapped session: %v", err), nil)
			}
			return ""
		}
		return id
	}
	systemPrompt := strings.TrimSpace(req.SystemPrompt)
	if systemPrompt == "" {
		systemPrompt = defaultMultiAgentSystemPrompt
	}
	id, err := s.agentChatRepo.CreateSession(systemPrompt, req.UserID, req.ContextID)
	if err != nil {
		if s.logger != nil {
			s.logger.Error("Server", "ensureAgentChatSession", fmt.Sprintf("Failed to create session: %v", err), nil)
		}
		return ""
	}
	return id
}

// persistAgentChatTurn appends request messages and the assistant response to the session when agentChatRepo is set.
func (s *Server) persistAgentChatTurn(sessionID string, req *AgentChatRequest, assistantContent string, creditsForTurn int) {
	var toAppend []*agentchat.Message
	for _, m := range req.Messages {
		content := strings.TrimSpace(m.Content)
		if content == "" {
			continue
		}
		role := strings.ToLower(strings.TrimSpace(m.Role))
		if role != "system" && role != "user" && role != "assistant" {
			role = "user"
		}
		toAppend = append(toAppend, &agentchat.Message{Role: role, Content: content, AgentID: strings.TrimSpace(m.AgentID), CreditsDeducted: 0})
	}
	toAppend = append(toAppend, &agentchat.Message{Role: "assistant", Content: assistantContent, CreditsDeducted: creditsForTurn})

	isToken := strings.TrimSpace(req.ContextID) != ""
	isPersonal := !isToken
	personalTrackerOK := false

	if isToken {
		if err := s.appendTokenChatHistoryToTracker(req, toAppend); err != nil && s.logger != nil {
			s.logger.Error("Server", "persistAgentChatTurn", fmt.Sprintf("Failed to append token chat history to tracker: %v", err), nil)
		}
	}
	if isPersonal && strings.TrimSpace(req.UserID) != "" && s.useTrackerForPersonalChatHistory() {
		if err := s.appendChatHistoryToTracker(strings.TrimSpace(req.UserID), chatthread.PersonalContextTokenAddress, toAppend); err != nil {
			if s.logger != nil {
				s.logger.Warn("Server", "persistAgentChatTurn", "Failed to append personal chat history to tracker: "+err.Error(), nil)
			}
		} else {
			personalTrackerOK = true
		}
	}

	if s.agentChatRepo == nil || sessionID == "" {
		return
	}
	if personalTrackerOK {
		return
	}
	if err := s.agentChatRepo.AppendMessages(sessionID, toAppend); err != nil && s.logger != nil {
		s.logger.Error("Server", "handleAgentChat", fmt.Sprintf("Failed to persist agent chat: %v", err), nil)
	}
}

const agentChatHistoryPathPrefix = "/api/v1/agent/chat/history/"

// handleAgentChatHistoryPrefix handles GET /api/v1/agent/chat/history/{session_id}. Returns session and message history for testing and inspection.
// Registered as a prefix so it takes precedence over the /api/ catch-all proxy.
func (s *Server) handleAgentChatHistoryPrefix(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.sendErrorResponse(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "GET required", nil)
		return
	}
	sessionID := strings.TrimPrefix(r.URL.Path, agentChatHistoryPathPrefix)
	if idx := strings.Index(sessionID, "/"); idx >= 0 {
		sessionID = sessionID[:idx]
	}
	sessionID = strings.Trim(sessionID, "/ ")
	if sessionID == "" {
		s.sendErrorResponse(w, http.StatusBadRequest, "MISSING_SESSION_ID", "session_id path segment required", nil)
		return
	}
	userID := strings.TrimSpace(r.URL.Query().Get("user_id"))
	if userID == "" {
		s.sendErrorResponse(w, http.StatusBadRequest, "MISSING_USER_ID", "user_id query parameter is required (chatter wallet for ownership)", nil)
		return
	}
	tokenAddress := strings.TrimSpace(r.URL.Query().Get("token_address"))
	if tokenAddress != "" {
		history, status, code, msg := s.getChatHistoryFromTracker(r.Context(), userID, tokenAddress)
		if status != 0 {
			s.sendErrorResponse(w, status, code, msg, nil)
			return
		}
		s.sendJSONResponse(w, http.StatusOK, history)
		return
	}
	stablePersonal := agentchat.StablePersonalSessionID(userID)
	if sessionID == stablePersonal && s.useTrackerForPersonalChatHistory() {
		hist, status, code, msg := s.getChatHistoryFromTracker(r.Context(), userID, chatthread.PersonalContextTokenAddress)
		if status != 0 {
			s.sendErrorResponse(w, status, code, msg, nil)
			return
		}
		msgs := trackerHistoryMessagesToAgentChatMessages(hist["messages"])
		now := time.Now().UTC()
		sess := &agentchat.Session{
			ID: stablePersonal, UserID: userID, ContextID: "", SystemPrompt: "",
			CreatedAt: now, UpdatedAt: now,
		}
		s.sendJSONResponse(w, http.StatusOK, AgentChatHistoryResponse{Session: sess, Messages: msgs})
		return
	}
	if s.agentChatRepo == nil {
		s.sendErrorResponse(w, http.StatusServiceUnavailable, "NOT_AVAILABLE", "Agent chat history not persisted", nil)
		return
	}
	sess, err := s.agentChatRepo.GetSession(sessionID)
	if err != nil {
		if s.logger != nil {
			s.logger.Error("Server", "handleAgentChatHistoryPrefix", fmt.Sprintf("GetSession: %v", err), nil)
		}
		s.sendErrorResponse(w, http.StatusInternalServerError, "SESSION_ERROR", "Failed to load session", nil)
		return
	}
	if sess == nil {
		s.sendErrorResponse(w, http.StatusNotFound, "SESSION_NOT_FOUND", "session not found", nil)
		return
	}
	if sess.UserID != userID {
		s.sendErrorResponse(w, http.StatusForbidden, "FORBIDDEN", "session does not belong to this user", nil)
		return
	}
	msgs, err := s.agentChatRepo.ListMessages(sessionID)
	if err != nil {
		if s.logger != nil {
			s.logger.Error("Server", "handleAgentChatHistoryPrefix", fmt.Sprintf("ListMessages: %v", err), nil)
		}
		s.sendErrorResponse(w, http.StatusInternalServerError, "SESSION_ERROR", "Failed to load messages", nil)
		return
	}
	s.sendJSONResponse(w, http.StatusOK, AgentChatHistoryResponse{Session: sess, Messages: msgs})
}

func (s *Server) useTrackerForPersonalChatHistory() bool {
	return s.config != nil && strings.TrimSpace(s.config.TrackerURL) != "" && s.getTrackerAPIKey() != ""
}

// appendChatHistoryToTracker POSTs messages to tracker (use chatthread.PersonalContextTokenAddress for personal agent chat).
func (s *Server) appendChatHistoryToTracker(userID, tokenAddress string, messages []*agentchat.Message) error {
	if s.config == nil || strings.TrimSpace(s.config.TrackerURL) == "" {
		return fmt.Errorf("tracker_url not configured")
	}
	apiKey := s.getTrackerAPIKey()
	if apiKey == "" {
		s.loadTrackerAPIKey()
		apiKey = s.getTrackerAPIKey()
	}
	if apiKey == "" {
		return fmt.Errorf("tracker API key not available (not registered or tracker_api_key file missing)")
	}
	type msg struct {
		Role            string `json:"role"`
		Content         string `json:"content"`
		AgentID         string `json:"agent_id,omitempty"`
		CreditsDeducted int    `json:"credits_deducted,omitempty"`
	}
	payload := struct {
		UserID       string `json:"user_id"`
		TokenAddress string `json:"token_address"`
		Messages     []msg  `json:"messages"`
	}{
		UserID:       strings.TrimSpace(userID),
		TokenAddress: strings.TrimSpace(tokenAddress),
		Messages:     make([]msg, 0, len(messages)),
	}
	for _, m := range messages {
		payload.Messages = append(payload.Messages, msg{
			Role:            strings.TrimSpace(m.Role),
			Content:         strings.TrimSpace(m.Content),
			AgentID:         strings.TrimSpace(m.AgentID),
			CreditsDeducted: m.CreditsDeducted,
		})
	}
	body, _ := json.Marshal(payload)
	url := strings.TrimSuffix(strings.TrimSpace(s.config.TrackerURL), "/") + "/api/v1/agent/chat/history/append"
	httpReq, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-API-Key", apiKey)
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024))
		return fmt.Errorf("tracker history append failed: %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func (s *Server) appendTokenChatHistoryToTracker(req *AgentChatRequest, messages []*agentchat.Message) error {
	return s.appendChatHistoryToTracker(strings.TrimSpace(req.UserID), strings.TrimSpace(req.ContextID), messages)
}

func (s *Server) getChatHistoryFromTracker(ctx context.Context, userID, tokenAddress string) (map[string]interface{}, int, string, string) {
	if s.config == nil || strings.TrimSpace(s.config.TrackerURL) == "" {
		return nil, http.StatusServiceUnavailable, "TRACKER_REQUIRED", "tracker_url required for token chat history"
	}
	apiKey := s.getTrackerAPIKey()
	if apiKey == "" {
		return nil, http.StatusServiceUnavailable, "NOT_REGISTERED", "Daemon must be registered with tracker"
	}
	base := strings.TrimSuffix(strings.TrimSpace(s.config.TrackerURL), "/")
	historyURL := base + "/api/v1/agent/chat/history?user_id=" + url.QueryEscape(strings.TrimSpace(userID)) + "&token_address=" + url.QueryEscape(strings.TrimSpace(tokenAddress))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, historyURL, nil)
	if err != nil {
		return nil, http.StatusInternalServerError, "GATEWAY_REQUEST_FAILED", "Failed to create request"
	}
	req.Header.Set("X-API-Key", apiKey)
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return nil, http.StatusBadGateway, "GATEWAY_UNREACHABLE", "Tracker unreachable"
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		var errEnvelope struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(body, &errEnvelope)
		return nil, resp.StatusCode, errEnvelope.Error.Code, errEnvelope.Error.Message
	}
	var out map[string]interface{}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, http.StatusBadGateway, "INVALID_RESPONSE", "Invalid JSON from tracker"
	}
	return out, 0, "", ""
}

// fetchPersonalSessionsFromTracker returns tracker-backed personal agent sessions (0 or 1 row).
func (s *Server) fetchPersonalSessionsFromTracker(ctx context.Context, userID string) ([]*agentchat.Session, error) {
	if s.config == nil || strings.TrimSpace(s.config.TrackerURL) == "" {
		return nil, fmt.Errorf("tracker_url not configured")
	}
	apiKey := s.getTrackerAPIKey()
	if apiKey == "" {
		s.loadTrackerAPIKey()
		apiKey = s.getTrackerAPIKey()
	}
	if apiKey == "" {
		return nil, fmt.Errorf("no tracker API key")
	}
	base := strings.TrimSuffix(strings.TrimSpace(s.config.TrackerURL), "/")
	u := base + "/api/v1/agent/chat/personal/sessions?user_id=" + url.QueryEscape(strings.TrimSpace(userID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-API-Key", apiKey)
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tracker personal sessions: status %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var env struct {
		Sessions []struct {
			ID           string `json:"id"`
			UserID       string `json:"user_id"`
			ContextID    string `json:"context_id"`
			SystemPrompt string `json:"system_prompt"`
			CreatedAt    string `json:"created_at"`
			UpdatedAt    string `json:"updated_at"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, err
	}
	out := make([]*agentchat.Session, 0, len(env.Sessions))
	for _, row := range env.Sessions {
		ca, _ := time.Parse(time.RFC3339, row.CreatedAt)
		ua, _ := time.Parse(time.RFC3339, row.UpdatedAt)
		out = append(out, &agentchat.Session{
			ID: row.ID, UserID: row.UserID, ContextID: row.ContextID, SystemPrompt: row.SystemPrompt,
			CreatedAt: ca, UpdatedAt: ua,
		})
	}
	return out, nil
}

func trackerHistoryMessagesToLLM(raw interface{}) []map[string]string {
	arr, ok := raw.([]interface{})
	if !ok {
		return nil
	}
	out := make([]map[string]string, 0, len(arr))
	for _, it := range arr {
		m, ok := it.(map[string]interface{})
		if !ok {
			continue
		}
		role, _ := m["role"].(string)
		content, _ := m["content"].(string)
		role = strings.ToLower(strings.TrimSpace(role))
		content = strings.TrimSpace(content)
		if content == "" {
			continue
		}
		if role != "user" && role != "assistant" && role != "system" {
			role = "user"
		}
		agentID, _ := m["agent_id"].(string)
		agentID = strings.TrimSpace(agentID)
		if agentID != "" {
			content = "[" + agentID + "]: " + content
		}
		out = append(out, map[string]string{"role": role, "content": content})
	}
	return out
}

func trackerHistoryMessagesToAgentChatMessages(raw interface{}) []*agentchat.Message {
	arr, ok := raw.([]interface{})
	if !ok {
		return nil
	}
	out := make([]*agentchat.Message, 0, len(arr))
	for _, it := range arr {
		m, ok := it.(map[string]interface{})
		if !ok {
			continue
		}
		role, _ := m["role"].(string)
		content, _ := m["content"].(string)
		role = strings.TrimSpace(strings.ToLower(role))
		content = strings.TrimSpace(content)
		if content == "" {
			continue
		}
		if role != "user" && role != "assistant" && role != "system" {
			role = "user"
		}
		seq := 0
		if v, ok := m["seq"].(float64); ok {
			seq = int(v)
		}
		agentID, _ := m["agent_id"].(string)
		credits := 0
		if v, ok := m["credits_deducted"].(float64); ok {
			credits = int(v)
		}
		created := time.Time{}
		if ts, ok := m["created_at"].(string); ok {
			created, _ = time.Parse(time.RFC3339, ts)
		}
		out = append(out, &agentchat.Message{
			Role: role, Content: content, AgentID: strings.TrimSpace(agentID),
			Seq: seq, CreditsDeducted: credits, CreatedAt: created,
		})
	}
	return out
}

// handleAgentChatSessions handles GET /api/v1/agent/chat/sessions. Requires query param user_id (the chatter, e.g. holder wallet); optional context_id (token contract for "conversations with this token's owner LLM").
// Merges tracker personal sessions (Postgres) with local SQLite when both are available.
func (s *Server) handleAgentChatSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.sendErrorResponse(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "GET required", nil)
		return
	}
	userID := strings.TrimSpace(r.URL.Query().Get("user_id"))
	contextID := strings.TrimSpace(r.URL.Query().Get("context_id"))
	if userID == "" {
		s.sendErrorResponse(w, http.StatusBadRequest, "MISSING_USER_ID", "user_id query parameter is required", nil)
		return
	}

	trackerPersonalOK := contextID == "" && s.useTrackerForPersonalChatHistory()
	var fromTracker []*agentchat.Session
	if trackerPersonalOK {
		tr, err := s.fetchPersonalSessionsFromTracker(r.Context(), userID)
		if err != nil {
			if s.logger != nil {
				s.logger.Warn("Server", "handleAgentChatSessions", "fetchPersonalSessionsFromTracker: "+err.Error(), nil)
			}
		} else {
			fromTracker = tr
		}
	}

	if s.agentChatRepo == nil {
		if len(fromTracker) == 0 && !trackerPersonalOK {
			s.sendErrorResponse(w, http.StatusServiceUnavailable, "NOT_AVAILABLE", "Agent chat session persistence is not available (no local DB and tracker personal sessions unavailable). Check daemon logs and registration.", nil)
			return
		}
		if len(fromTracker) == 0 && trackerPersonalOK {
			s.sendJSONResponse(w, http.StatusOK, AgentChatSessionsResponse{Sessions: []*agentchat.Session{}})
			return
		}
		s.sendJSONResponse(w, http.StatusOK, AgentChatSessionsResponse{Sessions: fromTracker})
		return
	}

	if err := s.agentChatRepo.BackfillSessionUserIDs(userID, contextID); err != nil && s.logger != nil {
		s.logger.Warn("Server", "handleAgentChatSessions", "BackfillSessionUserIDs: "+err.Error(), nil)
	}
	local, err := s.agentChatRepo.ListSessions(userID, contextID)
	if err != nil {
		if s.logger != nil {
			s.logger.Error("Server", "handleAgentChatSessions", fmt.Sprintf("ListSessions: %v", err), nil)
		}
		s.sendErrorResponse(w, http.StatusInternalServerError, "SESSION_ERROR", "Failed to list sessions", nil)
		return
	}
	if local == nil {
		local = []*agentchat.Session{}
	}

	seen := make(map[string]struct{})
	var merged []*agentchat.Session
	for _, sess := range fromTracker {
		if sess == nil {
			continue
		}
		seen[sess.ID] = struct{}{}
		merged = append(merged, sess)
	}
	stablePersonal := agentchat.StablePersonalSessionID(userID)
	for _, sess := range local {
		if sess == nil {
			continue
		}
		if _, dup := seen[sess.ID]; dup {
			continue
		}
		if contextID == "" && sess.ID == stablePersonal && len(fromTracker) > 0 {
			continue
		}
		merged = append(merged, sess)
	}
	sort.Slice(merged, func(i, j int) bool {
		return merged[i].UpdatedAt.After(merged[j].UpdatedAt)
	})
	s.sendJSONResponse(w, http.StatusOK, AgentChatSessionsResponse{Sessions: merged})
}

// handleAgentChatViaRelay forwards token chat to tracker; owner's daemon (elsewhere) uses its local LLM and posts response. Holder's daemon polls for result.
func (s *Server) handleAgentChatViaRelay(ctx context.Context, req *AgentChatRequest, messages []map[string]string, apiKey string) (content string, creditsDeducted int, status int, code, msg string) {
	trackerBase := strings.TrimSuffix(strings.TrimSpace(s.config.TrackerURL), "/")
	forwardURL := trackerBase + "/api/v1/agent/chat/forward"
	// Build payload: messages as {role, content}
	relayMessages := make([]map[string]string, 0, len(messages))
	for _, m := range messages {
		relayMessages = append(relayMessages, map[string]string{"role": m["role"], "content": m["content"]})
	}
	forwardBody := map[string]interface{}{
		"token_contract_address": req.ContextID,
		"user_id":                req.UserID,
		"messages":               relayMessages,
		"session_id":             req.SessionID,
	}
	bodyBytes, _ := json.Marshal(forwardBody)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, forwardURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", 0, http.StatusInternalServerError, "GATEWAY_REQUEST_FAILED", "Failed to create request"
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-API-Key", apiKey)
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return "", 0, http.StatusBadGateway, "GATEWAY_UNREACHABLE", "Tracker unreachable"
	}
	respBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var errEnvelope struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(respBody, &errEnvelope)
		code, msg := errEnvelope.Error.Code, errEnvelope.Error.Message
		if msg == "" {
			msg = fmt.Sprintf("tracker returned %d", resp.StatusCode)
		}
		return "", 0, resp.StatusCode, code, msg
	}
	var forwardResp struct {
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(respBody, &forwardResp); err != nil || forwardResp.RequestID == "" {
		return "", 0, http.StatusBadGateway, "INVALID_RESPONSE", "Tracker did not return request_id"
	}
	requestID := forwardResp.RequestID
	resultURL := trackerBase + "/api/v1/agent/chat/result/" + requestID
	pollInterval := 2 * time.Second
	// 5 minutes — must accommodate reasoning models that take 90–180s upstream.
	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		reqResult, err := http.NewRequestWithContext(ctx, http.MethodGet, resultURL, nil)
		if err != nil {
			return "", 0, http.StatusInternalServerError, "GATEWAY_REQUEST_FAILED", "Failed to create request"
		}
		reqResult.Header.Set("X-API-Key", apiKey)
		resResult, err := client.Do(reqResult)
		if err != nil {
			return "", 0, http.StatusBadGateway, "GATEWAY_UNREACHABLE", "Tracker unreachable"
		}
		bodyResult, _ := io.ReadAll(resResult.Body)
		resResult.Body.Close()
		if resResult.StatusCode == http.StatusOK {
			var resultResp struct {
				Status          string `json:"status"`
				Response        string `json:"response"`
				CreditsDeducted int    `json:"credits_deducted,omitempty"`
			}
			if json.Unmarshal(bodyResult, &resultResp) == nil && resultResp.Status == "completed" {
				content = strings.TrimSpace(resultResp.Response)
				if content == "" {
					content = "No response from owner."
				}
				return content, resultResp.CreditsDeducted, 0, "", ""
			}
		}
		if resResult.StatusCode != http.StatusAccepted && resResult.StatusCode != http.StatusOK {
			var errEnvelope struct {
				Error struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			_ = json.Unmarshal(bodyResult, &errEnvelope)
			return "", 0, resResult.StatusCode, errEnvelope.Error.Code, errEnvelope.Error.Message
		}
		time.Sleep(pollInterval)
	}
	return "", 0, http.StatusGatewayTimeout, "TIMEOUT", "Owner did not respond in time"
}

func (s *Server) fetchTokenOwnerWallet(ctx context.Context, tokenContractAddress, apiKey string) (wallet string, status int, code, msg string) {
	trackerBase := strings.TrimSuffix(strings.TrimSpace(s.config.TrackerURL), "/")
	ownerURL := trackerBase + "/api/v1/agent/chat/owner?token_contract_address=" + strings.TrimSpace(tokenContractAddress)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, ownerURL, nil)
	if err != nil {
		return "", http.StatusInternalServerError, "GATEWAY_REQUEST_FAILED", "Failed to create request"
	}
	httpReq.Header.Set("X-API-Key", apiKey)
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return "", http.StatusBadGateway, "GATEWAY_UNREACHABLE", "Tracker unreachable"
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var errEnvelope struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(body, &errEnvelope)
		return "", resp.StatusCode, errEnvelope.Error.Code, errEnvelope.Error.Message
	}
	var ownerInfo struct {
		OwnerWallet string `json:"owner_wallet_address"`
	}
	if err := json.Unmarshal(body, &ownerInfo); err != nil {
		return "", http.StatusBadGateway, "INVALID_RESPONSE", "Invalid owner response"
	}
	return strings.TrimSpace(ownerInfo.OwnerWallet), 0, "", ""
}

// handleAgentChatViaDirectP2P tries direct libp2p token chat (holder → owner). Returns (content, 0, "", "") on success; on failure returns ("", status, code, msg) so caller can fall back to relay.
func (s *Server) handleAgentChatViaDirectP2P(ctx context.Context, req *AgentChatRequest, messages []map[string]string, apiKey string) (content string, status int, code, msg string) {
	trackerBase := strings.TrimSuffix(strings.TrimSpace(s.config.TrackerURL), "/")
	ownerURL := trackerBase + "/api/v1/agent/chat/owner?token_contract_address=" + strings.TrimSpace(req.ContextID)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, ownerURL, nil)
	if err != nil {
		return "", http.StatusInternalServerError, "GATEWAY_REQUEST_FAILED", "Failed to create request"
	}
	httpReq.Header.Set("X-API-Key", apiKey)
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return "", http.StatusBadGateway, "GATEWAY_UNREACHABLE", "Tracker unreachable"
	}
	respBody, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return "", http.StatusBadGateway, "GATEWAY_READ_FAILED", "Failed to read response"
	}
	if resp.StatusCode != http.StatusOK {
		var errEnvelope struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(respBody, &errEnvelope)
		return "", resp.StatusCode, errEnvelope.Error.Code, errEnvelope.Error.Message
	}
	var ownerInfo struct {
		OwnerPeerID string   `json:"owner_peer_id"`
		Multiaddrs  []string `json:"multiaddrs"`
		Online      bool     `json:"online"`
	}
	if err := json.Unmarshal(respBody, &ownerInfo); err != nil || ownerInfo.OwnerPeerID == "" {
		return "", http.StatusBadGateway, "INVALID_RESPONSE", "Tracker did not return owner info"
	}
	if !ownerInfo.Online || len(ownerInfo.Multiaddrs) == 0 {
		return "", http.StatusServiceUnavailable, "OWNER_OFFLINE", "Owner offline; will try relay"
	}
	peerID, err := peer.Decode(ownerInfo.OwnerPeerID)
	if err != nil {
		return "", http.StatusBadGateway, "INVALID_PEER_ID", "Invalid owner peer_id"
	}
	if s.p2pHost == nil {
		return "", http.StatusServiceUnavailable, "P2P_NOT_READY", "P2P not ready"
	}
	if err := s.p2pHost.ConnectToPeer(peerID, ownerInfo.Multiaddrs); err != nil {
		return "", http.StatusBadGateway, "CONNECT_FAILED", "Could not connect to owner"
	}
	stream, err := s.p2pHost.Host().NewStream(ctx, peerID, protocol.ProtocolIDAgentChat)
	if err != nil {
		return "", http.StatusBadGateway, "STREAM_FAILED", "Could not open stream to owner"
	}
	defer stream.Close()
	relayMessages := make([]map[string]string, 0, len(messages))
	for _, m := range messages {
		relayMessages = append(relayMessages, map[string]string{"role": m["role"], "content": m["content"]})
	}
	streamReq := agentChatStreamRequest{
		TokenContractAddress: req.ContextID,
		UserID:               req.UserID,
		Messages:             relayMessages,
		SessionID:            req.SessionID,
	}
	if err := json.NewEncoder(stream).Encode(streamReq); err != nil {
		return "", http.StatusBadGateway, "WRITE_FAILED", "Failed to send request to owner"
	}
	var streamResp agentChatStreamResponse
	if err := json.NewDecoder(io.LimitReader(stream, 256*1024)).Decode(&streamResp); err != nil {
		return "", http.StatusBadGateway, "READ_FAILED", "Failed to read response from owner"
	}
	content = strings.TrimSpace(streamResp.Response)
	if content == "" {
		content = "No response from owner."
	}
	return content, 0, "", ""
}

// callLocalGatewayForMessages calls the daemon's configured gateway with the given messages and returns the assistant content or an error.
func (s *Server) callLocalGatewayForMessages(ctx context.Context, messages []map[string]string) (string, error) {
	if s.config == nil || strings.TrimSpace(s.config.GatewayURL) == "" {
		return "", fmt.Errorf("gateway_url not configured")
	}
	gatewayURL := strings.TrimSuffix(s.config.GatewayURL, "/") + "/v1/chat/completions"
	bodyBytes, _ := json.Marshal(map[string]interface{}{"messages": messages, "stream": false})
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, gatewayURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if s.config.GatewayToken != "" {
		httpReq.Header.Set("Authorization", "Bearer "+s.config.GatewayToken)
	}
	// 5 minutes for reasoning-class models (gpt-5.x, o-series).
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	content := extractLLMContent(respBody)
	return content, nil
}

// callAskViaDaemon runs the same ask flow as /api/v1/ask (tracker then gateway) for formatted messages. Used by owner's stream handler and relay poll so token chat reuses runAsk without duplication.
func (s *Server) callAskViaDaemon(ctx context.Context, messages []map[string]string) (string, error) {
	prompt := formatMessagesAsPrompt(messages)
	if prompt == "" {
		return "", fmt.Errorf("empty prompt")
	}
	content, status, code, msg := s.runAsk(ctx, prompt)
	if status != 0 {
		return "", fmt.Errorf("%s: %s", code, msg)
	}
	return content, nil
}

// isLocalDaemonOwnerOfToken verifies this daemon owns the given token contract according to tracker owner lookup.
func (s *Server) isLocalDaemonOwnerOfToken(ctx context.Context, tokenContractAddress, apiKey string) (bool, error) {
	if s.config == nil || strings.TrimSpace(s.config.TrackerURL) == "" {
		return false, fmt.Errorf("tracker_url not configured")
	}
	if strings.TrimSpace(tokenContractAddress) == "" {
		return false, fmt.Errorf("token_contract_address required")
	}
	if strings.TrimSpace(apiKey) == "" {
		return false, fmt.Errorf("api key missing")
	}
	if s.p2pHost == nil {
		return false, fmt.Errorf("p2p host not initialized")
	}
	trackerBase := strings.TrimSuffix(strings.TrimSpace(s.config.TrackerURL), "/")
	ownerURL := trackerBase + "/api/v1/agent/chat/owner?token_contract_address=" + strings.TrimSpace(tokenContractAddress)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ownerURL, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("X-API-Key", apiKey)
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("owner lookup returned %d", resp.StatusCode)
	}
	var ownerInfo struct {
		OwnerPeerID string `json:"owner_peer_id"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(&ownerInfo); err != nil {
		return false, err
	}
	localPeerID := s.p2pHost.ID().String()
	return strings.TrimSpace(ownerInfo.OwnerPeerID) == localPeerID, nil
}

// formatMessagesAsPrompt turns a list of role/content messages into a single prompt string for /api/v1/ask.
func formatMessagesAsPrompt(messages []map[string]string) string {
	var b strings.Builder
	for _, m := range messages {
		role := m["role"]
		if role == "" {
			role = "user"
		}
		content := strings.TrimSpace(m["content"])
		if content == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(strings.ToUpper(role[:1]) + role[1:] + ": " + content)
	}
	return b.String()
}

// runAgentChatRelayPollCycle runs one cycle for the owner's daemon: fetch pending token chat requests from tracker, call local gateway, post response.
// Call periodically (e.g. every 15s) when tracker URL, API key, and gateway URL are set.
func (s *Server) RunAgentChatRelayPollCycle(ctx context.Context) {
	if s.config == nil {
		return
	}
	trackerBase := strings.TrimSuffix(strings.TrimSpace(s.config.TrackerURL), "/")
	apiKey := s.getTrackerAPIKey()
	if trackerBase == "" || apiKey == "" || strings.TrimSpace(s.config.GatewayURL) == "" {
		return
	}
	if s.logger != nil {
		s.logger.Debug("Server", "RunAgentChatRelayPollCycle", "Polling tracker for pending token chat requests", nil)
	}
	pendingURL := trackerBase + "/api/v1/agent/chat/pending"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, pendingURL, nil)
	if err != nil {
		return
	}
	httpReq.Header.Set("X-API-Key", apiKey)
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		if s.logger != nil {
			s.logger.Warn("Server", "RunAgentChatRelayPollCycle", "Failed to fetch pending: "+err.Error(), nil)
		}
		return
	}
	respBody, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusOK {
		return
	}
	var pendingResp struct {
		Pending []struct {
			RequestID            string `json:"request_id"`
			TokenContractAddress string `json:"token_contract_address"`
			HolderUserID         string `json:"holder_user_id"`
			Messages             []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
			SessionID string `json:"session_id"`
		} `json:"pending"`
	}
	if json.Unmarshal(respBody, &pendingResp) != nil {
		return
	}
	n := len(pendingResp.Pending)
	if s.logger != nil {
		if n == 0 {
			s.logger.Debug("Server", "RunAgentChatRelayPollCycle", "Got 0 pending token chat requests", nil)
		} else {
			s.logger.Info("Server", "RunAgentChatRelayPollCycle", "Got pending token chat requests", map[string]interface{}{
				"pending_count": n,
			})
		}
	}
	if n == 0 {
		return
	}
	for _, item := range pendingResp.Pending {
		allowed, err := s.isLocalDaemonOwnerOfToken(ctx, item.TokenContractAddress, apiKey)
		if err != nil {
			if s.logger != nil {
				s.logger.Warn("Server", "RunAgentChatRelayPollCycle", "Owner verification failed: "+err.Error(), map[string]interface{}{
					"request_id": item.RequestID,
				})
			}
			continue
		}
		if !allowed {
			if s.logger != nil {
				s.logger.Warn("Server", "RunAgentChatRelayPollCycle", "Skipping pending request for non-owner daemon", map[string]interface{}{
					"request_id": item.RequestID,
				})
			}
			continue
		}
		messages := make([]map[string]string, 0, len(item.Messages)+1)
		messages = append(messages, map[string]string{"role": "system", "content": "You are the token owner's agent. A holder is chatting with you about your token. Be helpful and concise."})
		for _, m := range item.Messages {
			messages = append(messages, map[string]string{"role": m.Role, "content": m.Content})
		}
		content, err := s.callAskViaDaemon(ctx, messages)
		if err != nil {
			if s.logger != nil {
				s.logger.Warn("Server", "RunAgentChatRelayPollCycle", "Ask request failed: "+err.Error(), nil)
			}
			continue
		}
		if content == "" {
			content = "No response."
		}
		responseURL := trackerBase + "/api/v1/agent/chat/response"
		respBodyBytes, _ := json.Marshal(map[string]string{"request_id": item.RequestID, "response": content})
		reqResp, _ := http.NewRequestWithContext(ctx, http.MethodPost, responseURL, bytes.NewReader(respBodyBytes))
		reqResp.Header.Set("Content-Type", "application/json")
		reqResp.Header.Set("X-API-Key", apiKey)
		if resResp, err := client.Do(reqResp); err == nil {
			resResp.Body.Close()
			if s.logger != nil {
				s.logger.Info("Server", "RunAgentChatRelayPollCycle", "Responded to token chat request", map[string]interface{}{
					"request_id":             item.RequestID,
					"token_contract_address": item.TokenContractAddress,
				})
			}
		}
	}
}

// agentChatStreamRequest is the JSON payload sent over libp2p agent-chat stream (holder → owner).
type agentChatStreamRequest struct {
	TokenContractAddress string              `json:"token_contract_address"`
	UserID               string              `json:"user_id"`
	Messages             []map[string]string `json:"messages"`
	SessionID            string              `json:"session_id"`
}

// agentChatStreamResponse is the JSON payload sent back over the stream (owner → holder).
type agentChatStreamResponse struct {
	Response string `json:"response"`
}

// HandleAgentChatStream is the libp2p stream handler for direct token chat (owner's daemon). Called when a holder opens a stream with ProtocolIDAgentChat.
func (s *Server) HandleAgentChatStream(stream network.Stream) {
	defer stream.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	var req agentChatStreamRequest
	if err := json.NewDecoder(io.LimitReader(stream, 256*1024)).Decode(&req); err != nil {
		if s.logger != nil {
			s.logger.Warn("Server", "HandleAgentChatStream", "Failed to decode request: "+err.Error(), nil)
		}
		return
	}
	apiKey := s.getTrackerAPIKey()
	allowed, err := s.isLocalDaemonOwnerOfToken(ctx, req.TokenContractAddress, apiKey)
	if err != nil || !allowed {
		if s.logger != nil {
			errMsg := "token ownership verification failed"
			if err != nil {
				errMsg = err.Error()
			}
			s.logger.Warn("Server", "HandleAgentChatStream", "Rejecting stream request: "+errMsg, nil)
		}
		_ = json.NewEncoder(stream).Encode(agentChatStreamResponse{Response: "Unauthorized token chat request."})
		return
	}
	messages := make([]map[string]string, 0, len(req.Messages)+1)
	messages = append(messages, map[string]string{"role": "system", "content": "You are the token owner's agent. A holder is chatting with you about your token. Be helpful and concise."})
	for _, m := range req.Messages {
		role := m["role"]
		if role == "" {
			role = "user"
		}
		if c := m["content"]; c != "" {
			messages = append(messages, map[string]string{"role": role, "content": c})
		}
	}
	if len(messages) <= 1 {
		_ = json.NewEncoder(stream).Encode(agentChatStreamResponse{Response: "No messages received."})
		return
	}
	content, err := s.callAskViaDaemon(ctx, messages)
	if err != nil {
		if s.logger != nil {
			s.logger.Warn("Server", "HandleAgentChatStream", "Ask error: "+err.Error(), nil)
		}
		content = "Sorry, I couldn't generate a response."
	}
	if content == "" {
		content = "No response."
	}
	_ = json.NewEncoder(stream).Encode(agentChatStreamResponse{Response: content})
}

// handleAgentChatViaTracker proxies messages to tracker POST /api/v1/agents/completions.
// Returns (content, creditsDeducted, 0, "", "") on success, or ("", 0, status, code, message) on error
// (402 = insufficient credits, 401 = invalid API key). creditsDeducted comes from the tracker's
// X-Credits-Deducted response header (varies by model).
func (s *Server) handleAgentChatViaTracker(ctx context.Context, messages []map[string]string, apiKey, model string) (content string, creditsDeducted int, status int, code, msg string) {
	return s.handleAgentChatViaTrackerWithHeaders(ctx, messages, apiKey, model, nil)
}

// handleAgentChatViaTrackerWithHeaders is handleAgentChatViaTracker with extra
// request headers (the autopilot marks its drafts with X-StonkAgents-Auto: 1 so
// the tracker books them apart from the owner's chat).
func (s *Server) handleAgentChatViaTrackerWithHeaders(ctx context.Context, messages []map[string]string, apiKey, model string, extra http.Header) (content string, creditsDeducted int, status int, code, msg string) {
	trackerBase := strings.TrimSuffix(strings.TrimSpace(s.config.TrackerURL), "/")
	url := trackerBase + "/api/v1/agents/completions"
	body := map[string]interface{}{
		"messages": messages,
		"stream":   false,
	}
	if strings.TrimSpace(model) != "" {
		body["model"] = strings.TrimSpace(model)
	}
	bodyBytes, _ := json.Marshal(body)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", 0, http.StatusInternalServerError, "TRACKER_REQUEST_FAILED", "Failed to create tracker request"
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-API-Key", apiKey)
	for k, vs := range extra {
		for _, v := range vs {
			httpReq.Header.Add(k, v)
		}
	}

	// 5 minutes: detailed-mode reasoning models can take 90–180s. Daemon's
	// timeout must be ≥ tracker's agentProxyTimeout so we don't cancel a
	// successful upstream response just before it returns.
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(httpReq)
	if err != nil {
		return "", 0, http.StatusBadGateway, "TRACKER_UNREACHABLE", "Tracker unreachable or network error"
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", 0, http.StatusBadGateway, "TRACKER_READ_FAILED", "Failed to read tracker response"
	}

	if resp.StatusCode == http.StatusPaymentRequired {
		var errEnvelope struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(respBody, &errEnvelope)
		if errEnvelope.Error.Message != "" {
			msg = errEnvelope.Error.Message
		} else {
			msg = "Not enough credits"
		}
		return "", 0, http.StatusPaymentRequired, "INSUFFICIENT_CREDITS", msg
	}

	if resp.StatusCode == http.StatusUnauthorized {
		var errEnvelope struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(respBody, &errEnvelope)
		msg = "Tracker rejected API key (invalid or daemon not registered). Register the daemon with the tracker or set gateway_url for agent chat."
		if errEnvelope.Error.Message != "" {
			msg = errEnvelope.Error.Message
		}
		return "", 0, http.StatusUnauthorized, "UNAUTHORIZED", msg
	}

	if resp.StatusCode != http.StatusOK {
		return "", 0, resp.StatusCode, "TRACKER_ERROR", trackerErrorMessage(resp.StatusCode, respBody)
	}

	content = extractLLMContent(respBody)
	if content == "" {
		content = "No response from agent."
	}

	// Read actual credits deducted from tracker response header.
	// Falls back to the legacy default if the header is missing (e.g. older tracker).
	creditsDeducted = agentChatCreditsPerCompletion
	if h := resp.Header.Get("X-Credits-Deducted"); h != "" {
		if n, err := strconv.Atoi(h); err == nil && n > 0 {
			creditsDeducted = n
		}
	}
	return content, creditsDeducted, 0, "", ""
}

// handleAskViaTracker proxies the prompt to tracker POST /api/v1/agents/completions with peer API key. Returns (content, 0, "", "") on success, or ("", status, code, message) on error (402 = insufficient credits).
func (s *Server) handleAskViaTracker(ctx context.Context, prompt, apiKey string) (content string, status int, code, msg string) {
	trackerBase := strings.TrimSuffix(strings.TrimSpace(s.config.TrackerURL), "/")
	url := trackerBase + "/api/v1/agents/completions"
	body := map[string]interface{}{
		"messages": []map[string]string{{"role": "user", "content": prompt}},
		"stream":   false,
	}
	bodyBytes, _ := json.Marshal(body)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", http.StatusInternalServerError, "TRACKER_REQUEST_FAILED", "Failed to create tracker request"
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-API-Key", apiKey)

	// 5 minutes — see handleAgentChatViaTracker for rationale.
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(httpReq)
	if err != nil {
		return "", http.StatusBadGateway, "TRACKER_UNREACHABLE", "Tracker unreachable or network error"
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", http.StatusBadGateway, "TRACKER_READ_FAILED", "Failed to read tracker response"
	}

	if resp.StatusCode == http.StatusPaymentRequired {
		var errEnvelope struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(respBody, &errEnvelope)
		if errEnvelope.Error.Message != "" {
			msg = errEnvelope.Error.Message
		} else {
			msg = "Not enough credits"
		}
		return "", http.StatusPaymentRequired, "INSUFFICIENT_CREDITS", msg
	}

	if resp.StatusCode == http.StatusUnauthorized {
		var errEnvelope struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(respBody, &errEnvelope)
		msg = "Tracker rejected API key (invalid or daemon not registered). Register the daemon with the tracker or set gateway_url for ask."
		if errEnvelope.Error.Message != "" {
			msg = errEnvelope.Error.Message
		}
		return "", http.StatusUnauthorized, "UNAUTHORIZED", msg
	}

	if resp.StatusCode != http.StatusOK {
		return "", resp.StatusCode, "TRACKER_ERROR", trackerErrorMessage(resp.StatusCode, respBody)
	}

	content = extractLLMContent(respBody)
	if content == "" {
		content = "No response from agent."
	}
	return content, 0, "", ""
}

// trackerErrorMessage builds the message for a non-OK tracker completions response. The tracker
// forwards upstream LLM error bodies verbatim (e.g. OpenAI 429 insufficient_quota), so include
// the provider's message/code when present instead of the bare status.
func trackerErrorMessage(status int, body []byte) string {
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &envelope)
	msg := fmt.Sprintf("tracker returned %d", status)
	detail := strings.TrimSpace(envelope.Error.Message)
	if detail == "" {
		return msg
	}
	tag := envelope.Error.Code
	if tag == "" {
		tag = envelope.Error.Type
	}
	if tag != "" {
		return fmt.Sprintf("%s (%s): %s", msg, tag, detail)
	}
	return msg + ": " + detail
}

// extractLLMContent parses common LLM response shapes (OpenAI, Anthropic) and returns the first text content.
func extractLLMContent(body []byte) string {
	// OpenAI-style: { "choices": [ { "message": { "content": "..." } } ] }
	var openAI struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &openAI); err == nil && len(openAI.Choices) > 0 && openAI.Choices[0].Message.Content != "" {
		return strings.TrimSpace(openAI.Choices[0].Message.Content)
	}
	// Anthropic-style: { "content": [ { "type": "text", "text": "..." } ] }
	var anthropic struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(body, &anthropic); err == nil {
		for _, c := range anthropic.Content {
			if c.Type == "text" && c.Text != "" {
				return strings.TrimSpace(c.Text)
			}
		}
	}
	return ""
}

// DownloadRequest represents a download request
type DownloadRequest struct {
	CID string `json:"cid"`
}

// DownloadResponse represents download status
type DownloadResponse struct {
	CID     string `json:"cid"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

// WalletLinkRequest is the body for POST /api/v1/wallet/link (link Solana wallet to current peer).
type WalletLinkRequest struct {
	WalletAddress string `json:"wallet_address"`
}

// handleWalletLink handles POST /api/v1/wallet/link. Links the given Solana wallet to the daemon's peer on the tracker.
func (s *Server) handleWalletLink(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.sendErrorResponse(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "POST required", nil)
		return
	}
	limitedBody := io.LimitReader(r.Body, 1024)
	var req WalletLinkRequest
	if err := json.NewDecoder(limitedBody).Decode(&req); err != nil {
		s.sendErrorResponse(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid JSON body", nil)
		return
	}
	walletAddress := strings.TrimSpace(req.WalletAddress)
	if walletAddress == "" {
		s.sendErrorResponse(w, http.StatusBadRequest, "VALIDATION_ERROR", "wallet_address is required", nil)
		return
	}
	apiKey := s.getTrackerAPIKey()
	if apiKey == "" {
		if s.logger != nil {
			s.logger.Warn("API", "handleWalletLink", "Wallet link refused: no tracker API key. Check daemon logs for 'Failed to register with tracker' and ensure tracker is reachable.", nil)
		}
		s.sendErrorResponse(w, http.StatusServiceUnavailable, "PORTAL_PROXY_UNAVAILABLE",
			"Tracker API key not available. Daemon must register with the tracker first. Check daemon logs for 'Failed to register with tracker' and ensure the tracker URL in config is reachable.", nil)
		return
	}
	if s.trackerClient == nil {
		s.sendErrorResponse(w, http.StatusServiceUnavailable, "TRACKER_UNAVAILABLE", "Tracker client not configured", nil)
		return
	}
	if err := s.trackerClient.UpdateWallet(apiKey, walletAddress); err != nil {
		if s.logger != nil {
			s.logger.Warn("API", "handleWalletLink", "Tracker PATCH peers/me failed", map[string]interface{}{"error": err.Error()})
		}
		msg := fmt.Sprintf("Failed to link wallet: %v", err)
		if errors.Is(err, tracker.ErrCircuitOpen) {
			msg += ". The daemon will retry the tracker automatically after about 30 seconds, or you can restart the daemon to reset immediately."
		}
		s.sendErrorResponse(w, http.StatusBadGateway, "TRACKER_ERROR", msg, nil)
		return
	}
	s.sendJSONResponse(w, http.StatusOK, map[string]interface{}{"success": true, "wallet_address": walletAddress})
}

// headerLiveAgentKey is the HTTP header stonkagents-replicator and other live agents use to authenticate.
const headerLiveAgentKey = "X-Live-Agent-Key"

func liveAgentSecretFromRequest(r *http.Request) string {
	if v := strings.TrimSpace(r.Header.Get(headerLiveAgentKey)); v != "" {
		return v
	}
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	const prefix = "Bearer "
	if len(auth) > len(prefix) && strings.EqualFold(auth[:len(prefix)], prefix) {
		return strings.TrimSpace(auth[len(prefix):])
	}
	return ""
}

// liveAgentAuthOK returns true when no secret is configured (dev) or the request presents a matching secret.
func liveAgentAuthOK(configuredSecret string, r *http.Request) bool {
	if configuredSecret == "" {
		return true
	}
	got := liveAgentSecretFromRequest(r)
	if len(got) != len(configuredSecret) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(configuredSecret)) == 1
}

// handleDownload handles POST /api/v1/download requests
func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	s.handleDownloadWithManager(w, r, s.downloadManager, "handleDownload")
}

// handleLiveAgentDownload handles POST /api/v1/live-agent/download — replication / guardian pulls (same downloads dir as user downloads).
// Optional STONKAGENTS_LIVE_AGENT_DOWNLOAD_API_KEY: when set, requires X-Live-Agent-Key or Authorization: Bearer.
func (s *Server) handleLiveAgentDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.sendErrorResponse(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "POST only", nil)
		return
	}
	if s.config != nil && !liveAgentAuthOK(s.config.LiveAgentDownloadAPIKey, r) {
		s.sendErrorResponse(w, http.StatusUnauthorized, "UNAUTHORIZED", "Invalid or missing live agent credentials", nil)
		return
	}
	s.handleDownloadWithManager(w, r, s.downloadManager, "handleLiveAgentDownload")
}

func (s *Server) handleDownloadWithManager(w http.ResponseWriter, r *http.Request, dm *download.Manager, logLabel string) {
	// Extend write deadline: this handler calls the tracker (GetAssetMetadata) which can take
	// up to 30s on slow/cross-network links. Default server WriteTimeout (10s) would close the
	// connection before we respond. BitTorrent-style: client queues download and polls status.
	if rc := http.NewResponseController(w); rc != nil {
		_ = rc.SetWriteDeadline(time.Now().Add(45 * time.Second))
	}

	// Parse request body with size limit (10MB max per OWASP recommendations)
	limitedBody := io.LimitReader(r.Body, 10<<20) // 10 MB max body size
	var req DownloadRequest
	if err := json.NewDecoder(limitedBody).Decode(&req); err != nil {
		s.sendErrorResponse(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request body", nil)
		return
	}

	// Validate CID
	if req.CID == "" {
		s.sendErrorResponse(w, http.StatusBadRequest, "MISSING_CID", "CID is required", nil)
		return
	}

	if dm == nil {
		s.sendErrorResponse(w, http.StatusServiceUnavailable, "NOT_READY", "Transfer system initializing", nil)
		return
	}

	// Guard: tracker client must be initialized
	if s.trackerClient == nil {
		s.sendErrorResponse(w, http.StatusServiceUnavailable, "NOT_READY", "Tracker client not initialized", nil)
		return
	}

	// SECURITY: Get file metadata from tracker
	// This provides total_chunks, total_size, filename for P2P download
	metadata, err := s.trackerClient.GetAssetMetadata(req.CID)
	if err != nil {
		s.sendErrorResponse(w, http.StatusNotFound, "CID_NOT_FOUND", fmt.Sprintf("File not found in tracker: %v", err), nil)
		return
	}

	// Validate metadata
	if metadata.TotalChunks <= 0 {
		s.sendErrorResponse(w, http.StatusBadRequest, "INVALID_METADATA", "Invalid file metadata from tracker", nil)
		return
	}

	// SECURITY: Cap chunk count to prevent OOM from malicious tracker response
	const maxTotalChunks = 100_000 // ~100GB at 1MB chunks
	if metadata.TotalChunks > maxTotalChunks {
		s.sendErrorResponse(w, http.StatusBadRequest, "INVALID_METADATA",
			fmt.Sprintf("File too large: %d chunks exceeds maximum %d", metadata.TotalChunks, maxTotalChunks), nil)
		return
	}

	// Extract metadata fields
	filename := metadata.Filename
	totalSize := metadata.TotalSize
	totalChunks := metadata.TotalChunks

	err = dm.QueueDownload(req.CID, filename, totalSize, totalChunks)
	if err != nil {
		s.sendErrorResponse(w, http.StatusInternalServerError, "QUEUE_FAILED", fmt.Sprintf("Failed to queue download: %v", err), nil)
		return
	}

	response := DownloadResponse{
		CID:     req.CID,
		Status:  "queued",
		Message: "Download queued successfully",
	}

	s.sendJSONResponse(w, http.StatusAccepted, response)

	if s.logger != nil {
		s.logger.Info("API", logLabel, "Download queued", map[string]interface{}{
			"cid": req.CID,
		})
	}
}

// StatusResponse represents daemon status
type StatusResponse struct {
	Daemon             DaemonStatus       `json:"daemon"`
	Network            NetworkStatus      `json:"network"`
	TrackerRegistered  bool               `json:"tracker_registered"`   // true if daemon has tracker API key (required for wallet/link)
	TrackerCircuitOpen bool               `json:"tracker_circuit_open"` // true when circuit breaker is open (tracker was unreachable; retry in ~30s or restart daemon)
	TrackerURL         string             `json:"tracker_url"`          // configured tracker base URL, so the portal can spot an agent of another environment
	Environment        string             `json:"environment"`          // dev | stg | prd from STONKAGENTS_ENV (installenv.Current), prd when unset
	AccountID          string             `json:"account_id,omitempty"` // F-013 account_id; empty if legacy registration (no credits)
	SharedAssets       int                `json:"shared_assets"`
	Downloads          DownloadStatusInfo `json:"downloads"`
	Transfer           TransferStatsInfo  `json:"transfer"`         // F-032: upload/download speeds and totals
	Health             HealthIndicators   `json:"health"`           // F-032: NAT, DHT, mDNS, relay status
	Update             *UpdateInfo        `json:"update,omitempty"` // F-025: non-nil when a newer version is available
}

// TransferStatsInfo represents transfer speed and volume statistics (F-032)
type TransferStatsInfo struct {
	UploadSpeedBPS       int64   `json:"upload_speed_bps"`
	DownloadSpeedBPS     int64   `json:"download_speed_bps"`
	ShareRatio           float64 `json:"share_ratio"`
	TotalUploadedBytes   int64   `json:"total_uploaded_bytes"`
	TotalDownloadedBytes int64   `json:"total_downloaded_bytes"`
}

// HealthIndicators represents P2P health status (F-032)
type HealthIndicators struct {
	NATStatus      string `json:"nat_status"`
	DHTReady       bool   `json:"dht_ready"`
	MDNSReady      bool   `json:"mdns_ready"`
	RelayConnected bool   `json:"relay_connected"`
}

// UpdateInfo represents available update information from the tracker heartbeat.
type UpdateInfo struct {
	Available     bool   `json:"available"`
	LatestVersion string `json:"latest_version"`
	ReleaseNotes  string `json:"release_notes,omitempty"`
}

// DaemonStatus represents daemon-specific status
type DaemonStatus struct {
	Version string `json:"version"`
	Uptime  int64  `json:"uptime_seconds"`
	PeerID  string `json:"peer_id"`
}

// NetworkStatus represents network status
type NetworkStatus struct {
	Connected bool `json:"connected"`
	Peers     int  `json:"peers"`
}

// DownloadStatusInfo represents download statistics
type DownloadStatusInfo struct {
	Active    int `json:"active"`
	Queued    int `json:"queued"`
	Paused    int `json:"paused"`
	Completed int `json:"completed"`
	Failed    int `json:"failed"`
}

// p2pHealthIndicators reports NAT/DHT/mDNS/relay state (F-032); shared by
// /api/v1/status and the setup surface's p2p check.
func (s *Server) p2pHealthIndicators() HealthIndicators {
	healthInfo := HealthIndicators{NATStatus: "unknown"}
	if s.p2pHost != nil {
		healthInfo.DHTReady = s.p2pHost.dht != nil
		healthInfo.MDNSReady = s.p2pHost.mdnsService != nil
		healthInfo.RelayConnected = s.p2pHost.relayInfo != nil
	}
	return healthInfo
}

// handleStatus handles GET /api/v1/status requests
// peerCountTTL is how long /status reuses the last online peer count before
// asking the tracker again in the background.
const peerCountTTL = 30 * time.Second

// onlinePeerCount returns the cached online peer count and, when the cache is
// stale, starts one background refresh. The first call after startup answers 0
// while the tracker is asked; nothing here waits on the network.
func (s *Server) onlinePeerCount() int {
	s.peerCountMu.Lock()
	count := s.peerCount
	stale := time.Since(s.peerCountAt) >= peerCountTTL
	start := stale && !s.peerCountRefreshing
	if start {
		s.peerCountRefreshing = true
	}
	s.peerCountMu.Unlock()
	if start {
		s.goBackground(s.refreshOnlinePeerCount)
	}
	return count
}

// refreshOnlinePeerCount asks the tracker once and records the answer. A failed
// call keeps the last count and is retried after the TTL, not at once.
func (s *Server) refreshOnlinePeerCount() {
	count, err := s.trackerClient.GetOnlinePeerCount()
	s.peerCountMu.Lock()
	defer s.peerCountMu.Unlock()
	s.peerCountRefreshing = false
	s.peerCountAt = time.Now()
	if err == nil {
		s.peerCount = count
	}
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	// MVP: Return basic status
	// Production: Would include actual network stats, peer count, etc.
	uptime := int64(time.Since(s.startTime).Seconds())
	peerID := s.config.PeerID
	if s.p2pHost != nil {
		peerID = s.p2pHost.ID().String()
	}
	connectedPeers := 0
	connected := false
	if s.p2pHost != nil {
		connected = true
	}
	// Online AT peer count from the tracker (not DHT network peers), never awaited here.
	trackerCircuitOpen := false
	if s.trackerClient != nil {
		connectedPeers = s.onlinePeerCount()
		if s.trackerClient.CircuitState() == tracker.StateOpen {
			trackerCircuitOpen = true
		}
	}
	var dlCounts download.DownloadCounts
	if s.downloadManager != nil {
		dlCounts = s.downloadManager.GetCounts()
	}

	version := "dev"
	trackerURL := ""
	if s.config != nil {
		if s.config.Version != "" {
			version = s.config.Version
		}
		trackerURL = strings.TrimSuffix(strings.TrimSpace(s.config.TrackerURL), "/")
	}

	// F-032: Transfer stats
	var transferInfo TransferStatsInfo
	if s.transferStats != nil {
		snap := s.transferStats.GetStats()
		transferInfo = TransferStatsInfo{
			UploadSpeedBPS:       snap.UploadSpeedBPS,
			DownloadSpeedBPS:     snap.DownloadSpeedBPS,
			TotalUploadedBytes:   snap.TotalUpload,
			TotalDownloadedBytes: snap.TotalDownload,
		}
		if snap.TotalDownload > 0 {
			transferInfo.ShareRatio = float64(snap.TotalUpload) / float64(snap.TotalDownload)
		}
	}

	// F-032: Health indicators
	healthInfo := s.p2pHealthIndicators()

	response := StatusResponse{
		Daemon: DaemonStatus{
			Version: version,
			Uptime:  uptime,
			PeerID:  peerID,
		},
		Network: NetworkStatus{
			Connected: connected,
			Peers:     connectedPeers,
		},
		TrackerRegistered:  s.getTrackerAPIKey() != "",
		TrackerCircuitOpen: trackerCircuitOpen,
		TrackerURL:         trackerURL,
		Environment:        installenv.Current().Env,
		AccountID:          s.getTrackerAccountID(),
		SharedAssets:       0,
		Downloads: DownloadStatusInfo{
			Active:    dlCounts.Active,
			Queued:    dlCounts.Queued,
			Paused:    dlCounts.Paused,
			Completed: dlCounts.Completed,
			Failed:    dlCounts.Failed,
		},
		Transfer: transferInfo,
		Health:   healthInfo,
	}

	// F-025: Include update info if a newer version was reported by tracker heartbeat
	latestVer, releaseNotes := s.GetUpdateInfo()
	if latestVer != "" {
		response.Update = &UpdateInfo{
			Available:     true,
			LatestVersion: latestVer,
			ReleaseNotes:  releaseNotes,
		}
	}

	s.sendJSONResponse(w, http.StatusOK, response)

	if s.logger != nil {
		s.logger.Info("API", "handleStatus", "Status requested", nil)
	}
}

// NodeStatsResponse is the response shape for GET /api/v1/node/stats (F-027, US-027-05).
type NodeStatsResponse struct {
	UploadSpeedBPS     int64 `json:"upload_speed_bps"`
	DownloadSpeedBPS   int64 `json:"download_speed_bps"`
	TotalUploadBytes   int64 `json:"total_upload_bytes"`
	TotalDownloadBytes int64 `json:"total_download_bytes"`
	ActivePeers        int   `json:"active_peers"`
	SharedAssets       int   `json:"shared_assets"`
	UptimeSeconds      int64 `json:"uptime_seconds"`
}

// handleNodeStats handles GET /api/v1/node/stats — transfer speeds and node metrics.
func (s *Server) handleNodeStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.sendErrorResponse(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "GET required", nil)
		return
	}

	var snap stats.TransferStatsSnapshot
	if s.transferStats != nil {
		snap = s.transferStats.GetStats()
	}

	var activePeers int
	if s.p2pHost != nil {
		activePeers = len(s.p2pHost.ConnectedPeers())
	}

	resp := NodeStatsResponse{
		UploadSpeedBPS:     snap.UploadSpeedBPS,
		DownloadSpeedBPS:   snap.DownloadSpeedBPS,
		TotalUploadBytes:   snap.TotalUpload,
		TotalDownloadBytes: snap.TotalDownload,
		ActivePeers:        activePeers,
		SharedAssets:       0, // TODO(US-027-05): Wire to share manager when available
		UptimeSeconds:      int64(time.Since(s.startTime).Seconds()),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// handleDownloadsStatus handles GET /api/v1/downloads/status
// Returns all active downloads for REST polling (BitTorrent architecture)
func (s *Server) handleDownloadsStatus(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("cid") != "" {
		s.handleDownloadStatusByCID(w, r)
		return
	}

	// Get visible downloads (queued + active + paused) from download manager
	var visibleDownloads interface{}
	var counts interface{}
	if s.downloadManager != nil {
		visibleDownloads = s.downloadManager.GetVisibleDownloads()
		counts = s.downloadManager.GetCounts()
	} else {
		visibleDownloads = []interface{}{}
		counts = map[string]int{"queued": 0, "active": 0, "paused": 0, "completed": 0, "failed": 0}
	}

	// Recent terminal transfers from history (portal merges into transfer list)
	var recent interface{}
	if s.historyRepo != nil {
		recentRecords, _ := s.historyRepo.Recent(25)
		recent = recentRecords
	}
	if recent == nil {
		recent = []interface{}{}
	}

	response := map[string]interface{}{
		"downloads": visibleDownloads,
		"counts":    counts,
		"recent":    recent,
	}
	if s.transferStats != nil {
		response["stats"] = s.transferStats.GetStats()
	}

	s.sendJSONResponse(w, http.StatusOK, response)
}

// handleDownloadStatusByCID handles GET /api/v1/downloads/status?cid=xxx
// Returns specific download status by CID
func (s *Server) handleDownloadStatusByCID(w http.ResponseWriter, r *http.Request) {
	cid := strings.TrimSpace(r.URL.Query().Get("cid"))
	cid = strings.TrimSuffix(cid, "/")
	if cid == "" {
		s.sendErrorResponse(w, http.StatusBadRequest, "MISSING_CID", "Missing cid query parameter", nil)
		return
	}

	if s.downloadManager == nil {
		s.sendErrorResponse(w, http.StatusServiceUnavailable, "NOT_READY", "Download manager not initialized", nil)
		return
	}

	status := s.downloadManager.GetStatus(cid)
	if status == nil {
		s.sendErrorResponse(w, http.StatusNotFound, "NOT_FOUND", "Download not found", nil)
		return
	}

	s.sendJSONResponse(w, http.StatusOK, status)
}

// handleUploadsStatus handles GET /api/v1/uploads/status
// Returns enriched upload status with per-file detail for REST polling (BitTorrent architecture)
func (s *Server) handleUploadsStatus(w http.ResponseWriter, r *http.Request) {
	// Get enriched upload details from upload manager
	var uploads interface{}
	activeCount := 0
	queuedCount := 0
	if s.uploadManager != nil {
		uploads = s.uploadManager.GetDetailedUploads()
		activeCount = s.uploadManager.GetActiveCount()
		queuedCount = s.uploadManager.GetPendingCount()
	} else {
		uploads = []upload.UploadStatusDetail{} // Empty typed slice for consistent JSON
	}

	response := map[string]interface{}{
		"uploads": uploads,
		"active":  activeCount,
		"queued":  queuedCount,
	}

	s.sendJSONResponse(w, http.StatusOK, response)
}

// handleRetryDownload handles POST /api/v1/downloads/retry
// AC-8: Validates CID format, then calls RetryDownload to reset StateFailed→StateQueued.
func (s *Server) handleRetryDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.sendErrorResponse(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "POST only", nil)
		return
	}

	var req struct {
		CID string `json:"cid"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		s.sendErrorResponse(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid JSON body", nil)
		return
	}
	if req.CID == "" {
		s.sendErrorResponse(w, http.StatusBadRequest, "VALIDATION_ERROR", "cid is required", nil)
		return
	}

	if err := download.ValidateCID(req.CID); err != nil {
		s.sendErrorResponse(w, http.StatusBadRequest, "VALIDATION_ERROR", "Invalid CID format", nil)
		return
	}

	if s.downloadManager == nil {
		s.sendErrorResponse(w, http.StatusServiceUnavailable, "NOT_READY", "Download manager not initialized", nil)
		return
	}

	if err := s.downloadManager.RetryDownload(req.CID); err != nil {
		s.sendErrorResponse(w, http.StatusBadRequest, "RETRY_FAILED", "Download cannot be retried in current state", nil)
		return
	}

	s.sendJSONResponse(w, http.StatusOK, map[string]string{
		"cid":    req.CID,
		"status": "queued",
	})
}

// handlePauseDownloadByPath handles POST /api/v1/downloads/{cid}/pause (AC-28)
func (s *Server) handlePauseDownloadByPath(w http.ResponseWriter, r *http.Request) {
	cid := r.PathValue("cid")
	if err := download.ValidateCID(cid); err != nil {
		s.sendErrorResponse(w, http.StatusBadRequest, "VALIDATION_ERROR", "Invalid CID format", nil)
		return
	}
	if s.downloadManager == nil {
		s.sendErrorResponse(w, http.StatusServiceUnavailable, "NOT_READY", "Transfer system initializing", nil)
		return
	}
	if err := s.downloadManager.PauseDownload(cid); err != nil {
		s.sendErrorResponse(w, http.StatusBadRequest, "PAUSE_FAILED", "Download cannot be paused in current state", nil)
		return
	}
	s.sendJSONResponse(w, http.StatusOK, map[string]string{"cid": cid, "status": "paused"})
}

// handleResumeDownloadByPath handles POST /api/v1/downloads/{cid}/resume (AC-28)
func (s *Server) handleResumeDownloadByPath(w http.ResponseWriter, r *http.Request) {
	cid := r.PathValue("cid")
	if err := download.ValidateCID(cid); err != nil {
		s.sendErrorResponse(w, http.StatusBadRequest, "VALIDATION_ERROR", "Invalid CID format", nil)
		return
	}
	if s.downloadManager == nil {
		s.sendErrorResponse(w, http.StatusServiceUnavailable, "NOT_READY", "Transfer system initializing", nil)
		return
	}
	if err := s.downloadManager.ResumeDownload(cid); err != nil {
		s.sendErrorResponse(w, http.StatusBadRequest, "RESUME_FAILED", "Download cannot be resumed in current state", nil)
		return
	}
	s.sendJSONResponse(w, http.StatusOK, map[string]string{"cid": cid, "status": "active"})
}

// handleCancelDownloadByPath handles POST /api/v1/downloads/{cid}/cancel (AC-28)
func (s *Server) handleCancelDownloadByPath(w http.ResponseWriter, r *http.Request) {
	cid := r.PathValue("cid")
	if err := download.ValidateCID(cid); err != nil {
		s.sendErrorResponse(w, http.StatusBadRequest, "VALIDATION_ERROR", "Invalid CID format", nil)
		return
	}
	if s.downloadManager == nil {
		s.sendErrorResponse(w, http.StatusServiceUnavailable, "NOT_READY", "Transfer system initializing", nil)
		return
	}
	if err := s.downloadManager.CancelDownload(cid); err != nil {
		s.sendErrorResponse(w, http.StatusBadRequest, "CANCEL_FAILED", "Download cannot be cancelled", nil)
		return
	}
	s.sendJSONResponse(w, http.StatusOK, map[string]string{"cid": cid, "status": "cancelled"})
}

// handlePauseDownload handles POST /api/v1/downloads/pause with JSON body.
// AC: Validates CID, calls PauseDownload, returns safe error messages (H5).
func (s *Server) handlePauseDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.sendErrorResponse(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "POST only", nil)
		return
	}
	var req struct {
		CID string `json:"cid"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil || req.CID == "" {
		s.sendErrorResponse(w, http.StatusBadRequest, "VALIDATION_ERROR", "cid is required", nil)
		return
	}
	if s.downloadManager == nil {
		s.sendErrorResponse(w, http.StatusServiceUnavailable, "NOT_READY", "Transfer system initializing", nil)
		return
	}
	if err := download.ValidateCID(req.CID); err != nil {
		s.sendErrorResponse(w, http.StatusBadRequest, "VALIDATION_ERROR", "Invalid CID format", nil)
		return
	}
	if err := s.downloadManager.PauseDownload(req.CID); err != nil {
		if s.logger != nil {
			s.logger.Error("Server", "handlePauseDownload", fmt.Sprintf("PauseDownload failed for CID %s: %v", req.CID, err), nil)
		}
		s.sendErrorResponse(w, http.StatusBadRequest, "PAUSE_FAILED", "Download cannot be paused in current state", nil)
		return
	}
	s.sendJSONResponse(w, http.StatusOK, map[string]string{"cid": req.CID, "status": "paused"})
}

// handleResumeDownload handles POST /api/v1/downloads/resume with JSON body.
// AC: Validates CID, calls ResumeDownload, returns safe error messages (H5).
func (s *Server) handleResumeDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.sendErrorResponse(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "POST only", nil)
		return
	}
	var req struct {
		CID string `json:"cid"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil || req.CID == "" {
		s.sendErrorResponse(w, http.StatusBadRequest, "VALIDATION_ERROR", "cid is required", nil)
		return
	}
	if s.downloadManager == nil {
		s.sendErrorResponse(w, http.StatusServiceUnavailable, "NOT_READY", "Transfer system initializing", nil)
		return
	}
	if err := download.ValidateCID(req.CID); err != nil {
		s.sendErrorResponse(w, http.StatusBadRequest, "VALIDATION_ERROR", "Invalid CID format", nil)
		return
	}
	if err := s.downloadManager.ResumeDownload(req.CID); err != nil {
		if s.logger != nil {
			s.logger.Error("Server", "handleResumeDownload", fmt.Sprintf("ResumeDownload failed for CID %s: %v", req.CID, err), nil)
		}
		s.sendErrorResponse(w, http.StatusBadRequest, "RESUME_FAILED", "Download cannot be resumed in current state", nil)
		return
	}
	s.sendJSONResponse(w, http.StatusOK, map[string]string{"cid": req.CID, "status": "active"})
}

// handlePauseAll handles POST /api/v1/downloads/pause-all (F-029).
// Pauses all active downloads. Returns count of paused downloads.
func (s *Server) handlePauseAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.sendErrorResponse(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "POST only", nil)
		return
	}
	if s.downloadManager == nil {
		s.sendErrorResponse(w, http.StatusServiceUnavailable, "NOT_READY", "Transfer system initializing", nil)
		return
	}
	count := s.downloadManager.PauseAll()
	s.sendJSONResponse(w, http.StatusOK, map[string]interface{}{"paused": count})
}

// handleResumeAll handles POST /api/v1/downloads/resume-all (F-029).
// Resumes all paused downloads. Returns count of resumed downloads.
func (s *Server) handleResumeAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.sendErrorResponse(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "POST only", nil)
		return
	}
	if s.downloadManager == nil {
		s.sendErrorResponse(w, http.StatusServiceUnavailable, "NOT_READY", "Transfer system initializing", nil)
		return
	}
	count := s.downloadManager.ResumeAll()
	s.sendJSONResponse(w, http.StatusOK, map[string]interface{}{"resumed": count})
}

// handleClearCompleted handles POST /api/v1/downloads/clear (F-029).
// Removes all completed and failed downloads from the status list.
func (s *Server) handleClearCompleted(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.sendErrorResponse(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "POST only", nil)
		return
	}
	if s.downloadManager == nil {
		s.sendErrorResponse(w, http.StatusServiceUnavailable, "NOT_READY", "Transfer system initializing", nil)
		return
	}
	count := s.downloadManager.ClearCompleted()
	s.sendJSONResponse(w, http.StatusOK, map[string]interface{}{"cleared": count})
}

// handleRetryDownloadByPath handles POST /api/v1/downloads/{cid}/retry (F-029).
func (s *Server) handleRetryDownloadByPath(w http.ResponseWriter, r *http.Request) {
	cid := r.PathValue("cid")
	if err := download.ValidateCID(cid); err != nil {
		s.sendErrorResponse(w, http.StatusBadRequest, "VALIDATION_ERROR", "Invalid CID format", nil)
		return
	}
	if s.downloadManager == nil {
		s.sendErrorResponse(w, http.StatusServiceUnavailable, "NOT_READY", "Transfer system initializing", nil)
		return
	}
	if err := s.downloadManager.RetryDownload(cid); err != nil {
		s.sendErrorResponse(w, http.StatusBadRequest, "RETRY_FAILED", "Download cannot be retried", nil)
		return
	}
	s.sendJSONResponse(w, http.StatusOK, map[string]string{"cid": cid, "status": "queued"})
}

// handleLibrary handles GET /api/v1/library
// Returns paginated shared files with storage summary per ADR-001 (default 20, max 100).
func (s *Server) handleLibrary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.sendErrorResponse(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "GET only", nil)
		return
	}

	// ADR-001: default 20, max 100
	limit := 20
	offset := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 && parsed <= 100 {
			limit = parsed
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed >= 0 {
			offset = parsed
		}
	}

	if s.chunkStore == nil {
		s.sendJSONResponse(w, http.StatusOK, map[string]interface{}{
			"files":   []interface{}{},
			"storage": map[string]interface{}{"used_bytes": 0, "file_count": 0},
			"meta":    map[string]interface{}{"total": 0, "limit": limit, "offset": offset},
		})
		return
	}

	files, total, err := s.chunkStore.ListFilesPaginated(limit, offset)
	if err != nil {
		s.sendErrorResponse(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to query library", nil)
		return
	}
	if files == nil {
		files = []*storage.LibraryFile{}
	}

	usedBytes, fileCount, err := s.chunkStore.GetStorageSummary()
	if err != nil {
		usedBytes = 0
		fileCount = 0
	}

	s.sendJSONResponse(w, http.StatusOK, map[string]interface{}{
		"files": files,
		"storage": map[string]interface{}{
			"used_bytes": usedBytes,
			"file_count": fileCount,
		},
		"meta": map[string]interface{}{
			"total":  total,
			"limit":  limit,
			"offset": offset,
		},
	})
}

// handleExportFileByCID streams a locally available shared file as bytes.
// Intended for internal replication workers running inside trusted network boundaries.
func (s *Server) handleExportFileByCID(w http.ResponseWriter, r *http.Request) {
	cid := strings.TrimSpace(r.PathValue("cid"))
	if cid == "" {
		s.sendErrorResponse(w, http.StatusBadRequest, "VALIDATION_ERROR", "Missing CID", nil)
		return
	}
	if s.chunkStore == nil {
		s.sendErrorResponse(w, http.StatusServiceUnavailable, "NOT_READY", "Storage not initialized", nil)
		return
	}

	file, err := s.chunkStore.GetFile(cid)
	if err != nil {
		s.sendErrorResponse(w, http.StatusNotFound, "NOT_FOUND", "File not found", nil)
		return
	}

	var buf bytes.Buffer
	for i := 0; i < file.TotalChunks; i++ {
		chunkData, err := s.chunkStore.GetChunk(cid, i)
		if err != nil {
			s.sendErrorResponse(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to reconstruct file", nil)
			return
		}
		if _, err := buf.Write(chunkData); err != nil {
			s.sendErrorResponse(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to stream file", nil)
			return
		}
	}

	filename := filepath.Base(file.Filename)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Header().Set("Content-Length", strconv.Itoa(buf.Len()))
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, &buf)
}

// handleTransferHistory handles GET /api/v1/transfers/history
// Returns paginated transfer history per ADR-001 (default 20, max 100).
func (s *Server) handleTransferHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.sendErrorResponse(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "GET only", nil)
		return
	}

	// ADR-001: default 20, max 100
	limit := 20
	offset := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 && parsed <= 100 {
			limit = parsed
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed >= 0 {
			offset = parsed
		}
	}

	if s.historyRepo == nil {
		s.sendJSONResponse(w, http.StatusOK, map[string]interface{}{
			"transfers": []interface{}{},
			"meta":      map[string]int{"total": 0, "limit": limit, "offset": offset},
		})
		return
	}

	records, total, err := s.historyRepo.List(limit, offset)
	if err != nil {
		s.sendErrorResponse(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to query history", nil)
		return
	}
	if records == nil {
		records = []*history.TransferRecord{}
	}

	s.sendJSONResponse(w, http.StatusOK, map[string]interface{}{
		"transfers": records,
		"meta": map[string]int{
			"total":  total,
			"limit":  limit,
			"offset": offset,
		},
	})
}

// portalProxyForward forwards to the tracker with X-API-Key. targetPath is the path on the tracker (e.g. /api/peers/123/trust).
// Used by both handlePortalProxy and handlePortalProxyV1.
func (s *Server) portalProxyForward(w http.ResponseWriter, r *http.Request, targetPath string) {
	key := s.getTrackerAPIKey()
	if key == "" {
		s.sendErrorResponse(w, http.StatusServiceUnavailable, "PORTAL_PROXY_UNAVAILABLE",
			"Tracker API key not available; daemon must register with tracker first.", nil)
		return
	}
	if s.config == nil || s.config.TrackerURL == "" {
		s.sendErrorResponse(w, http.StatusServiceUnavailable, "PORTAL_PROXY_UNAVAILABLE",
			"Tracker URL not configured.", nil)
		return
	}
	trackerBase := strings.TrimSuffix(s.config.TrackerURL, "/")
	target := trackerBase + targetPath
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	var body io.Reader
	if r.Body != nil && (r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodPatch) {
		body = r.Body
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target, body)
	if err != nil {
		if s.logger != nil {
			s.logger.Error("Server", "portalProxyForward", fmt.Sprintf("Failed to create proxy request: %v", err), nil)
		}
		s.sendErrorResponse(w, http.StatusBadGateway, "GATEWAY_REQUEST_FAILED", "Gateway request failed", nil)
		return
	}
	if ct := r.Header.Get("Content-Type"); ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	req.Header.Set("X-API-Key", key)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		if s.logger != nil {
			s.logger.Error("Server", "portalProxyForward", fmt.Sprintf("Upstream request failed: %v", err), nil)
		}
		s.sendErrorResponse(w, http.StatusBadGateway, "GATEWAY_UNREACHABLE", "Upstream service unavailable", nil)
		return
	}
	defer resp.Body.Close()

	// Forward allowed headers from upstream (TD-023: rate limit + cache headers)
	for _, h := range proxyAllowedHeaders {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// handlePortalProxy forwards requests under /api/ to the tracker with X-API-Key set (path as-is).
func (s *Server) handlePortalProxy(w http.ResponseWriter, r *http.Request) {
	s.portalProxyForward(w, r, r.URL.Path)
}

// handlePortalProxyV1 forwards /api/v1/portal/* to tracker /api/* with X-API-Key set.
// Covers: POST /api/v1/portal/peers/{id}/trust, /block; POST /api/v1/portal/board/posts, /board/posts/{id}/upvote, /board/posts/{id}/replies;
// GET /api/v1/portal/peers, /api/v1/portal/board/posts, etc. Body and query forwarded as-is; returns tracker { data: T } as-is.
func (s *Server) handlePortalProxyV1(w http.ResponseWriter, r *http.Request) {
	const prefix = "/api/v1/portal"
	reqPath := r.URL.Path
	if !strings.HasPrefix(reqPath, prefix) {
		s.sendErrorResponse(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid portal path", nil)
		return
	}
	trackerPath := "/api" + reqPath[len(prefix):]
	if trackerPath == "/api" {
		trackerPath = "/api/"
	}
	// TD-071: Sanitize path to prevent traversal attacks (../ escape to upstream endpoints)
	trackerPath = path.Clean(trackerPath)
	if !strings.HasPrefix(trackerPath, "/api/") && trackerPath != "/api" {
		s.sendErrorResponse(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid portal path", nil)
		return
	}
	s.portalProxyForward(w, r, trackerPath)
}

// ConnectionInfo represents a single connected peer
type ConnectionInfo struct {
	PeerID   string `json:"peer_id"`
	Protocol string `json:"protocol"`
}

// ConnectionsResponse represents the list of connected peers
type ConnectionsResponse struct {
	Data []ConnectionInfo `json:"data"`
}

// handleConnections handles GET /api/v1/connections — returns connected peer IDs.
func (s *Server) handleConnections(w http.ResponseWriter, r *http.Request) {
	var data []ConnectionInfo
	if s.p2pHost != nil {
		peers := s.p2pHost.ConnectedPeers()
		data = make([]ConnectionInfo, len(peers))
		for i, pid := range peers {
			data[i] = ConnectionInfo{PeerID: pid, Protocol: "libp2p"}
		}
	} else {
		data = []ConnectionInfo{}
	}
	s.sendJSONResponse(w, http.StatusOK, ConnectionsResponse{Data: data})
}

// handleF013Proxy forwards F-013 routes to the tracker with X-API-Key.
// Maps daemon /api/v1/{domain}/* → tracker /api/v1/tracker/{domain}/*
// where domain is: credits, social, purchase, account.
func (s *Server) handleF013Proxy(w http.ResponseWriter, r *http.Request) {
	const prefix = "/api/v1/"
	reqPath := r.URL.Path
	if !strings.HasPrefix(reqPath, prefix) {
		s.sendErrorResponse(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid F-013 proxy path", nil)
		return
	}
	// /api/v1/credits/balance → /api/v1/tracker/credits/balance
	trackerPath := "/api/v1/tracker/" + reqPath[len(prefix):]
	// TD-071: Sanitize path to prevent traversal attacks
	trackerPath = path.Clean(trackerPath)
	if !strings.HasPrefix(trackerPath, "/api/v1/tracker/") {
		s.sendErrorResponse(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid F-013 proxy path", nil)
		return
	}
	s.portalProxyForward(w, r, trackerPath)
}
