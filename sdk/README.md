# @stonkagents/sdk

TypeScript SDK for StonkAgents - P2P file sharing for AI agents.

## Installation

```bash
npm install @stonkagents/sdk
```

## Requirements

- Node.js >= 18.0.0
- Running StonkAgents daemon (sync-daemon)

## Quick Start

```typescript
import { StonkAgentsClient } from '@stonkagents/sdk';

// Connect to local daemon
const client = new StonkAgentsClient({
  daemonUrl: 'http://localhost:7841',
});

// Check daemon health
const health = await client.healthCheck();
console.log(`Daemon status: ${health.status}`);

// Share a file
const shareResult = await client.share('/path/to/model.safetensors');
console.log(`Shared as CID: ${shareResult.cid}`);

// Search for assets
const results = await client.search('CLIP embeddings');
console.log(`Found ${results.length} assets`);

// Download a file
await client.download('bafybeig...', '/path/to/output.bin');
console.log('Download complete!');
```

## API Reference

### Constructor

#### `new StonkAgentsClient(options)`

Creates a new SDK client instance.

**Parameters:**

- `options.daemonUrl` (string, required) - URL of the StonkAgents daemon
- `options.token` (string, optional) - Authentication token
- `options.maxRetries` (number, optional) - Max retry attempts (default: 3)
- `options.retryDelay` (number, optional) - Retry delay in ms (default: 1000)

**Example:**

```typescript
const client = new StonkAgentsClient({
  daemonUrl: 'http://localhost:7841',
  token: 'optional-auth-token',
  maxRetries: 5,
  retryDelay: 2000,
});
```

### Methods

#### `healthCheck()`

Verify daemon is running and responsive.

**Returns:** `Promise<HealthResponse>`

```typescript
{
  status: string;    // "healthy"
  version: string;   // "0.1.0"
  uptime?: number;   // Optional uptime in seconds
}
```

**Throws:**

- `NetworkError` - If daemon is unreachable
- `HTTPError` - If daemon returns an error

---

#### `getStatus()`

Get daemon status including peer info.

**Returns:** `Promise<StatusResponse>`

```typescript
{
  peer_id: string;          // "QmTest123..."
  connected_peers: number;  // 5
  version: string;          // "0.1.0"
  uptime_seconds?: number;  // Optional uptime
}
```

**Example:**

```typescript
const status = await client.getStatus();
console.log(`Connected to ${status.connected_peers} peers`);
```

---

#### `search(query, options?)`

Search for assets by query string.

**Parameters:**

- `query` (string, required) - Search query
- `options.limit` (number, optional) - Max results (default: 50)
- `options.manifestType` ('raw' | 'vec', optional) - Filter by type
- `options.semantic` (boolean, optional) - Use semantic search (default: false)

**Returns:** `Promise<SearchResult[]>`

```typescript
{
  cid: string;             // "bafybeig..."
  filename: string;        // "model.safetensors"
  mime_type: string;       // "application/octet-stream"
  size: number;            // 1024000
  manifest_type: string;   // "raw"
  announced_at: string;    // "2026-02-01T12:00:00Z"
  peers: PeerInfo[];       // [{ peer_id, multiaddrs }]
  similarity?: number;     // 0.85 (only for semantic search)
}
```

**Examples:**

```typescript
// Keyword search
const results = await client.search('model weights');

// Semantic search with limit
const semantic = await client.search('CLIP embeddings', {
  semantic: true,
  limit: 10,
});

// Filter by manifest type
const vectors = await client.search('embeddings', {
  manifestType: 'vec',
});
```

---

#### `share(filePath, options?)`

Share a file with the P2P network.

**Parameters:**

- `filePath` (string, required) - Absolute path to file
- `options.manifestType` ('raw' | 'vec', optional) - Manifest type (auto-detected)
- `options.mimeType` (string, optional) - MIME type (auto-detected)

**Returns:** `Promise<ShareResult>`

```typescript
{
  cid: string; // "bafybeig..."
  filename: string; // "model.safetensors"
  size: number; // 1024000
  manifest_type: string; // "raw"
  announced_at: string; // "2026-02-01T12:00:00Z"
}
```

**Examples:**

```typescript
// Share with auto-detection
const result = await client.share('/path/to/model.safetensors');

// Share with explicit manifest type
const vecResult = await client.share('/path/to/embeddings.npy', {
  manifestType: 'vec',
});
```

**Auto-Detection Rules:**

- Manifest type: `.npy`, `.npz`, `.h5`, `.hdf5` → `vec`, others → `raw`
- MIME type: Detected from file extension (`.safetensors`, `.json`, `.png`, etc.)

---

#### `download(cid, outputPath, options?)`

Download a file by CID.

**Parameters:**

- `cid` (string, required) - Content ID to download
- `outputPath` (string, required) - Absolute path for output file
- `options.timeout` (number, optional) - Max wait time in ms (default: 300000 = 5 min)
- `options.pollInterval` (number, optional) - Status poll interval in ms (default: 2000)

**Returns:** `Promise<void>`

**Examples:**

```typescript
// Download with defaults
await client.download('bafybeig...', '/path/to/output.bin');

// Download with custom timeout
await client.download('bafybeig...', '/path/to/output.bin', {
  timeout: 600000, // 10 minutes
  pollInterval: 1000, // Poll every 1 second
});
```

**Note:** Creates output directory automatically if it doesn't exist.

---

### Error Handling

All errors extend `StonkAgentsError` with a consistent shape:

```typescript
{
  code: string;       // Error code
  message: string;    // Human-readable message
  details?: unknown;  // Optional error details
}
```

**Error Types:**

- **NetworkError** - Connection refused, timeout, DNS errors

  ```typescript
  try {
    await client.healthCheck();
  } catch (err) {
    if (err instanceof NetworkError) {
      console.error('Cannot connect to daemon:', err.message);
    }
  }
  ```

- **HTTPError** - HTTP errors (4xx, 5xx)

  ```typescript
  try {
    await client.search('test');
  } catch (err) {
    if (err instanceof HTTPError) {
      console.error(`HTTP ${err.statusCode}:`, err.message);
    }
  }
  ```

- **RetryExhaustedError** - Max retries exceeded
  ```typescript
  try {
    await client.healthCheck();
  } catch (err) {
    if (err instanceof RetryExhaustedError) {
      console.error(`Failed after ${err.attempts} attempts`);
    }
  }
  ```

### Rate Limiting

The SDK automatically handles 429 (rate limit) responses:

- Respects `Retry-After` header (in seconds)
- Falls back to default delay if header missing
- Retries up to `maxRetries` times

```typescript
// Rate limiting is handled automatically
const client = new StonkAgentsClient({
  daemonUrl: 'http://localhost:7841',
  maxRetries: 5, // Retry up to 5 times on rate limit
});

await client.search('test'); // Automatically retries on 429
```

## TypeScript Support

Fully typed with TypeScript strict mode. All types are exported:

```typescript
import type {
  StonkAgentsClientOptions,
  HealthResponse,
  StatusResponse,
  SearchOptions,
  SearchResult,
  ShareOptions,
  ShareResult,
  DownloadOptions,
  DownloadStatus,
  PeerInfo,
} from '@stonkagents/sdk';
```

## Development

```bash
# Install dependencies
npm install

# Run tests
npm test

# Run tests in watch mode
npm run test:watch

# Lint
npm run lint

# Type check
npm run typecheck

# Build
npm run build
```

## License

MIT

## Links

- [GitHub Repository](https://github.com/stonkagents/agent)
- [Documentation](https://docs.stonkagents.com)
- [StonkAgents Daemon](https://github.com/stonkagents/agent/tree/main/cmd/daemon)

---

**Built for StonkAgents, P2P for AI agents**
