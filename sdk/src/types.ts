// Package: sdk/src
// Feature: F-004 (TypeScript SDK)
// Story: US-004-02 (Core API Methods)
// Purpose: Type definitions for share, search, download operations

/**
 * Options for sharing a file
 */
export interface ShareOptions {
  manifestType?: 'raw' | 'vec';
  mimeType?: string;
}

/**
 * Result from sharing a file
 */
export interface ShareResult {
  cid: string;
  filename: string;
  size: number;
  manifest_type: string;
  announced_at: string;
}

/**
 * Options for searching assets
 */
export interface SearchOptions {
  manifestType?: 'raw' | 'vec';
  limit?: number;
  semantic?: boolean;
}

/**
 * Peer information in search results
 */
export interface PeerInfo {
  peer_id: string;
  multiaddrs: string[];
}

/**
 * Search result item
 */
export interface SearchResult {
  cid: string;
  filename: string;
  mime_type: string;
  size: number;
  manifest_type: string;
  announced_at: string;
  peers: PeerInfo[];
  similarity?: number; // Only present for semantic search
}

/**
 * Paginated search response envelope from daemon
 * Audit C1: Matches daemon SearchResponse shape
 */
export interface SearchResponse {
  data: SearchResult[];
  total: number;
  page: number;
  pageSize: number;
}

/**
 * Options for downloading a file
 */
export interface DownloadOptions {
  timeout?: number; // Max time to wait for download (ms)
  pollInterval?: number; // Status polling interval (ms)
}

/**
 * Download status
 */
export interface DownloadStatus {
  cid: string;
  filename: string;
  state: 'queued' | 'active' | 'paused' | 'completed' | 'failed';
  progress: number; // 0-1
  downloaded_bytes: number;
  total_bytes: number;
  speed_bps?: number;
  eta_seconds?: number;
  peers?: number;
}

/**
 * Daemon status response
 */
export interface StatusResponse {
  peer_id: string;
  connected_peers: number;
  version: string;
  uptime_seconds?: number;
}
