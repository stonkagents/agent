// Package: sdk/src
// Feature: F-004 (TypeScript SDK)
// Story: US-004-01 (SDK Scaffolding)
// Purpose: HTTP client for StonkAgents daemon

import * as http from 'node:http';
import * as https from 'node:https';
import * as fs from 'node:fs';
import * as path from 'node:path';
import { URL } from 'node:url';
import { HTTPError, NetworkError, RetryExhaustedError } from './errors';
import type {
  ShareOptions,
  ShareResult,
  SearchOptions,
  SearchResponse,
  DownloadOptions,
  DownloadStatus,
  StatusResponse,
} from './types';

export interface StonkAgentsClientOptions {
  daemonUrl: string;
  token?: string;
  maxRetries?: number;
  retryDelay?: number;
}

export interface HealthResponse {
  status: string;
  version: string;
  uptime?: number;
}

interface ErrorResponse {
  error: {
    code: string;
    message: string;
    details?: unknown;
  };
}

/**
 * TypeScript SDK client for StonkAgents daemon
 */
export class StonkAgentsClient {
  private readonly daemonUrl: string;
  private readonly token?: string;
  private readonly maxRetries: number;
  private readonly retryDelay: number;

  constructor(options: StonkAgentsClientOptions) {
    this.daemonUrl = options.daemonUrl;
    this.token = options.token;
    this.maxRetries = options.maxRetries ?? 3;
    this.retryDelay = options.retryDelay ?? 1000; // 1 second base delay
  }

  /**
   * Health check - verify daemon is running
   *
   * @returns Health status with version and uptime
   * @throws {NetworkError} If daemon is unreachable
   * @throws {HTTPError} If daemon returns an error
   *
   * @example
   * ```typescript
   * const client = new StonkAgentsClient({ daemonUrl: 'http://localhost:7841' });
   * const health = await client.healthCheck();
   * console.log(health.status); // "healthy"
   * ```
   */
  async healthCheck(): Promise<HealthResponse> {
    return this.request<HealthResponse>('GET', '/health');
  }

  /**
   * Get daemon status including peer info and connection count
   *
   * @returns Daemon status with peer_id, connected_peers, and version
   * @throws {NetworkError} If daemon is unreachable
   * @throws {HTTPError} If daemon returns an error
   *
   * @example
   * ```typescript
   * const status = await client.getStatus();
   * console.log(`Connected to ${status.connected_peers} peers`);
   * ```
   */
  async getStatus(): Promise<StatusResponse> {
    return this.request<StatusResponse>('GET', '/api/v1/status');
  }

  /**
   * Search for assets by query string
   *
   * @param query - Search query string
   * @param options - Search options (limit, manifestType, semantic)
   * @returns Array of matching assets with peer info
   * @throws {NetworkError} If daemon is unreachable
   * @throws {HTTPError} If daemon returns an error
   *
   * @example
   * ```typescript
   * // Keyword search
   * const results = await client.search('model weights', { limit: 10 });
   *
   * // Semantic search
   * const semantic = await client.search('CLIP embeddings', { semantic: true });
   * console.log(semantic[0].similarity); // 0.85
   * ```
   */
  async search(query: string, options?: SearchOptions): Promise<SearchResponse> {
    const params = new URLSearchParams();
    params.set('q', query);

    if (options?.limit) {
      params.set('pageSize', options.limit.toString());
    }
    if (options?.manifestType) {
      params.set('type', options.manifestType);
    }
    if (options?.semantic) {
      params.set('semantic', 'true');
    }

    // Audit C1: Use correct daemon endpoint and SearchResponse type
    return this.request<SearchResponse>('GET', `/api/v1/search?${params.toString()}`);
  }

  /**
   * Share a file with the P2P network
   *
   * Reads the file from disk, auto-detects MIME type and manifest type,
   * then uploads to the daemon for sharing with peers.
   *
   * @param filePath - Absolute path to file to share
   * @param options - Share options (manifestType, mimeType)
   * @returns Share result with CID and metadata
   * @throws {Error} If file not found
   * @throws {NetworkError} If daemon is unreachable
   * @throws {HTTPError} If daemon returns an error
   *
   * @example
   * ```typescript
   * // Share with auto-detection
   * const result = await client.share('/path/to/model.safetensors');
   * console.log(`Shared as CID: ${result.cid}`);
   *
   * // Share with explicit manifest type
   * const vecResult = await client.share('/path/to/embeddings.npy', {
   *   manifestType: 'vec'
   * });
   * ```
   */
  async share(filePath: string, options?: ShareOptions): Promise<ShareResult> {
    // Read file
    if (!fs.existsSync(filePath)) {
      throw new Error(`File not found: ${filePath}`);
    }

    const fileBuffer = fs.readFileSync(filePath);
    const filename = path.basename(filePath);
    const stats = fs.statSync(filePath);

    // Detect MIME type from extension
    const ext = path.extname(filename).toLowerCase();
    const mimeType = options?.mimeType || this.detectMimeType(ext) || 'application/octet-stream';

    // Detect manifest type from extension if not provided
    const manifestType = options?.manifestType || this.detectManifestType(ext);

    // Create multipart form data (simplified - using JSON for MVP)
    const formData = {
      filename,
      size: stats.size,
      mime_type: mimeType,
      manifest_type: manifestType,
      data: fileBuffer.toString('base64'), // Base64 encode for JSON transport
    };

    return this.request<ShareResult>('POST', '/api/v1/share', formData);
  }

  /**
   * Download a file by CID
   *
   * Queues the download, polls for completion, then writes the file to disk.
   * Automatically creates output directory if it doesn't exist.
   *
   * @param cid - Content ID (CID) of the file to download
   * @param outputPath - Absolute path where file should be written
   * @param options - Download options (timeout, pollInterval)
   * @returns Promise that resolves when download complete
   * @throws {Error} If download fails or times out
   * @throws {NetworkError} If daemon is unreachable
   * @throws {HTTPError} If daemon returns an error
   *
   * @example
   * ```typescript
   * // Download with default options (5 min timeout, 2s polling)
   * await client.download('bafybeig...', '/path/to/output.bin');
   *
   * // Download with custom polling interval
   * await client.download('bafybeig...', '/path/to/output.bin', {
   *   pollInterval: 1000, // Poll every 1 second
   *   timeout: 600000     // 10 minute timeout
   * });
   * ```
   */
  async download(cid: string, outputPath: string, options?: DownloadOptions): Promise<void> {
    const timeout = options?.timeout || 300000; // 5 minutes default
    const pollInterval = options?.pollInterval || 2000; // 2 seconds default

    // Start download
    await this.request<{ message: string }>('POST', '/api/v1/download', { cid });

    // Poll status until complete
    const startTime = Date.now();
    while (Date.now() - startTime < timeout) {
      const status = await this.request<DownloadStatus>('GET', `/api/v1/downloads/${cid}/status`);

      if (status.state === 'completed') {
        // Download file data
        const fileData = await this.requestRaw('GET', `/api/v1/assets/${cid}`);

        // Write to output path
        const outputDir = path.dirname(outputPath);
        if (!fs.existsSync(outputDir)) {
          fs.mkdirSync(outputDir, { recursive: true });
        }
        fs.writeFileSync(outputPath, fileData);
        return;
      }

      if (status.state === 'failed') {
        throw new Error(`Download failed for CID: ${cid}`);
      }

      // Wait before next poll
      await this.sleep(pollInterval);
    }

    throw new Error(`Download timeout after ${timeout}ms for CID: ${cid}`);
  }

  /**
   * Internal HTTP request method with retry logic
   */
  private async request<T>(
    method: string,
    path: string,
    body?: unknown,
    attempt: number = 1
  ): Promise<T> {
    const url = new URL(path, this.daemonUrl);
    const isHttps = url.protocol === 'https:';
    const client = isHttps ? https : http;

    return new Promise((resolve, reject) => {
      const options: http.RequestOptions = {
        method,
        hostname: url.hostname,
        port: url.port || (isHttps ? 443 : 80),
        path: url.pathname + url.search,
        headers: {
          'Content-Type': 'application/json',
          ...(this.token && { Authorization: `Bearer ${this.token}` }),
        },
      };

      const req = client.request(options, (res) => {
        let data = '';

        res.on('data', (chunk) => {
          data += chunk;
        });

        res.on('end', async () => {
          const statusCode = res.statusCode || 0;

          // Success (2xx)
          if (statusCode >= 200 && statusCode < 300) {
            try {
              const parsed = data ? JSON.parse(data) : {};
              resolve(parsed as T);
            } catch (err) {
              reject(new HTTPError(statusCode, 'PARSE_ERROR', 'Failed to parse JSON response'));
            }
            return;
          }

          // Parse error response
          let errorData: ErrorResponse | null = null;
          try {
            errorData = data ? JSON.parse(data) : null;
          } catch {
            // Ignore parse errors for error responses
          }

          const errorCode = errorData?.error?.code || 'HTTP_ERROR';
          const errorMessage = errorData?.error?.message || `HTTP ${statusCode} error`;

          // Rate limit (429) - retry with Retry-After delay
          if (statusCode === 429) {
            if (attempt < this.maxRetries) {
              // Parse Retry-After header (in seconds)
              const retryAfter = res.headers['retry-after'];
              const delay = retryAfter ? parseInt(retryAfter, 10) * 1000 : this.retryDelay;

              await this.sleep(delay);
              try {
                const result = await this.request<T>(method, path, body, attempt + 1);
                resolve(result);
              } catch (err) {
                reject(err);
              }
              return;
            } else {
              reject(
                new HTTPError(
                  statusCode,
                  'RATE_LIMIT_EXCEEDED',
                  'Rate limit exceeded',
                  errorData?.error?.details
                )
              );
              return;
            }
          }

          // Client error (4xx) - don't retry
          if (statusCode >= 400 && statusCode < 500) {
            reject(new HTTPError(statusCode, errorCode, errorMessage, errorData?.error?.details));
            return;
          }

          // Server error (5xx) - retry with exponential backoff
          if (statusCode >= 500) {
            if (attempt < this.maxRetries) {
              const delay = this.retryDelay * Math.pow(2, attempt - 1); // Exponential backoff
              await this.sleep(delay);
              try {
                const result = await this.request<T>(method, path, body, attempt + 1);
                resolve(result);
              } catch (err) {
                reject(err);
              }
              return;
            } else {
              reject(
                new RetryExhaustedError(
                  `Retry exhausted after ${this.maxRetries} attempts`,
                  this.maxRetries
                )
              );
              return;
            }
          }

          // Other errors
          reject(new HTTPError(statusCode, errorCode, errorMessage));
        });
      });

      req.on('error', (err) => {
        // Network errors (connection refused, timeout, DNS, etc.)
        reject(new NetworkError(`Network error: ${err.message}`, err));
      });

      if (body) {
        req.write(JSON.stringify(body));
      }

      req.end();
    });
  }

  /**
   * Sleep for given milliseconds (for retry backoff)
   */
  private sleep(ms: number): Promise<void> {
    return new Promise((resolve) => setTimeout(resolve, ms));
  }

  /**
   * Request raw binary data (for file downloads)
   */
  private async requestRaw(method: string, path: string): Promise<Buffer> {
    const url = new URL(path, this.daemonUrl);
    const isHttps = url.protocol === 'https:';
    const client = isHttps ? https : http;

    return new Promise((resolve, reject) => {
      const options: http.RequestOptions = {
        method,
        hostname: url.hostname,
        port: url.port || (isHttps ? 443 : 80),
        path: url.pathname + url.search,
        headers: {
          ...(this.token && { Authorization: `Bearer ${this.token}` }),
        },
      };

      const req = client.request(options, (res) => {
        const chunks: Buffer[] = [];

        res.on('data', (chunk: Buffer) => {
          chunks.push(chunk);
        });

        res.on('end', () => {
          const statusCode = res.statusCode || 0;

          if (statusCode >= 200 && statusCode < 300) {
            resolve(Buffer.concat(chunks));
          } else {
            reject(new HTTPError(statusCode, 'HTTP_ERROR', `HTTP ${statusCode} error`));
          }
        });
      });

      req.on('error', (err) => {
        reject(new NetworkError(`Network error: ${err.message}`, err));
      });

      req.end();
    });
  }

  /**
   * Detect MIME type from file extension
   */
  private detectMimeType(ext: string): string | null {
    const mimeTypes: Record<string, string> = {
      '.safetensors': 'application/octet-stream',
      '.bin': 'application/octet-stream',
      '.npy': 'application/octet-stream',
      '.npz': 'application/octet-stream',
      '.json': 'application/json',
      '.txt': 'text/plain',
      '.csv': 'text/csv',
      '.png': 'image/png',
      '.jpg': 'image/jpeg',
      '.jpeg': 'image/jpeg',
    };

    return mimeTypes[ext] || null;
  }

  /**
   * Detect manifest type from file extension
   */
  private detectManifestType(ext: string): 'raw' | 'vec' {
    const vectorTypes = ['.npy', '.npz', '.h5', '.hdf5'];
    return vectorTypes.includes(ext) ? 'vec' : 'raw';
  }
}
