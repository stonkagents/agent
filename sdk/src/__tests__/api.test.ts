// Package: sdk/src/__tests__
// Feature: F-004 (TypeScript SDK)
// Story: US-004-02 (Core API Methods)
// Purpose: TDD tests for share, search, download, getStatus methods

import { StonkAgentsClient } from '../client';
import * as http from 'node:http';
import * as fs from 'node:fs';
import * as path from 'node:path';
import * as os from 'node:os';

// Mock HTTP server for testing
let server: http.Server | null = null;
let serverUrl: string;
let testDir: string;

beforeAll(async () => {
  // Create temp directory for test files
  testDir = fs.mkdtempSync(path.join(os.tmpdir(), 'sdk-test-'));

  server = http.createServer();
  await new Promise<void>((resolve) => {
    server!.listen(0, () => {
      const address = server!.address();
      if (address && typeof address !== 'string') {
        serverUrl = `http://localhost:${address.port}`;
      }
      resolve();
    });
  });
});

afterAll(async () => {
  if (server) {
    await new Promise<void>((resolve) => server!.close(() => resolve()));
  }

  // Cleanup test directory
  if (testDir && fs.existsSync(testDir)) {
    fs.rmSync(testDir, { recursive: true, force: true });
  }
});

describe('Core API Methods', () => {
  describe('getStatus', () => {
    // RED TEST 1: getStatus returns daemon status
    it('returns daemon status with peer info', async () => {
      server!.removeAllListeners('request');
      server!.on('request', (req, res) => {
        if (req.url === '/api/v1/status' && req.method === 'GET') {
          res.writeHead(200, { 'Content-Type': 'application/json' });
          res.end(
            JSON.stringify({
              peer_id: 'QmTest123',
              connected_peers: 5,
              version: '0.1.0',
              uptime_seconds: 3600,
            })
          );
        } else {
          res.writeHead(404);
          res.end();
        }
      });

      const client = new StonkAgentsClient({ daemonUrl: serverUrl });
      const status = await client.getStatus();

      expect(status.peer_id).toBe('QmTest123');
      expect(status.connected_peers).toBe(5);
      expect(status.version).toBe('0.1.0');
    });
  });

  describe('search', () => {
    // RED TEST 2: search returns typed results
    it('searches and returns asset list', async () => {
      server!.removeAllListeners('request');
      server!.on('request', (req, res) => {
        if (req.url?.startsWith('/api/v1/search') && req.method === 'GET') {
          res.writeHead(200, { 'Content-Type': 'application/json' });
          res.end(
            JSON.stringify({
              data: [
                {
                  cid: 'bafytest123',
                  filename: 'model.safetensors',
                  mime_type: 'application/octet-stream',
                  size: 1024000,
                  manifest_type: 'raw',
                  announced_at: '2026-02-01T12:00:00Z',
                  peers: [{ peer_id: 'QmPeer1', multiaddrs: ['/ip4/1.1.1.1/tcp/4001'] }],
                },
              ],
              total: 1,
              page: 1,
              pageSize: 20,
            })
          );
        } else {
          res.writeHead(404);
          res.end();
        }
      });

      const client = new StonkAgentsClient({ daemonUrl: serverUrl });
      const response = await client.search('model');

      expect(response.data).toHaveLength(1);
      expect(response.total).toBe(1);
      expect(response.data[0].cid).toBe('bafytest123');
      expect(response.data[0].filename).toBe('model.safetensors');
      expect(response.data[0].peers).toHaveLength(1);
    });

    // RED TEST 3: search with options (limit, type, semantic)
    it('sends correct query parameters', async () => {
      let capturedUrl = '';
      server!.removeAllListeners('request');
      server!.on('request', (req, res) => {
        capturedUrl = req.url || '';
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ data: [], total: 0, page: 1, pageSize: 20 }));
      });

      const client = new StonkAgentsClient({ daemonUrl: serverUrl });
      await client.search('test', { limit: 10, manifestType: 'vec', semantic: true });

      expect(capturedUrl).toContain('q=test');
      expect(capturedUrl).toContain('pageSize=10');
      expect(capturedUrl).toContain('type=vec');
      expect(capturedUrl).toContain('semantic=true');
    });
  });

  describe('share', () => {
    // RED TEST 4: share uploads file and returns CID
    it('shares file and receives CID', async () => {
      // Create test file
      const testFile = path.join(testDir, 'test.bin');
      fs.writeFileSync(testFile, Buffer.from('test data'));

      server!.removeAllListeners('request');
      server!.on('request', (req, res) => {
        if (req.url === '/api/v1/share' && req.method === 'POST') {
          // Read multipart body (unused for mock, but needed for real request handling)
          req.on('data', () => {
            // Body reading not needed for mock test
          });
          req.on('end', () => {
            res.writeHead(201, { 'Content-Type': 'application/json' });
            res.end(
              JSON.stringify({
                cid: 'bafyshared123',
                filename: 'test.bin',
                size: 9,
                manifest_type: 'raw',
                announced_at: '2026-02-01T12:00:00Z',
              })
            );
          });
        } else {
          res.writeHead(404);
          res.end();
        }
      });

      const client = new StonkAgentsClient({ daemonUrl: serverUrl });
      const result = await client.share(testFile);

      expect(result.cid).toBe('bafyshared123');
      expect(result.filename).toBe('test.bin');
      expect(result.size).toBe(9);
    });

    // RED TEST 5: share with options
    it('sends manifest type in request', async () => {
      const testFile = path.join(testDir, 'embeddings.npy');
      fs.writeFileSync(testFile, Buffer.from('numpy data'));

      let capturedBody = '';
      server!.removeAllListeners('request');
      server!.on('request', (req, res) => {
        req.on('data', (chunk: Buffer) => {
          capturedBody += chunk.toString();
        });
        req.on('end', () => {
          res.writeHead(201, { 'Content-Type': 'application/json' });
          res.end(
            JSON.stringify({
              cid: 'bafyvec123',
              filename: 'embeddings.npy',
              size: 10,
              manifest_type: 'vec',
              announced_at: '2026-02-01T12:00:00Z',
            })
          );
        });
      });

      const client = new StonkAgentsClient({ daemonUrl: serverUrl });
      await client.share(testFile, { manifestType: 'vec' });

      // Multipart body should include manifest_type field
      expect(capturedBody).toContain('manifest_type');
      expect(capturedBody).toContain('vec');
    });
  });

  describe('download', () => {
    // RED TEST 6: download writes file to disk
    it('downloads file and writes to output path', async () => {
      const outputPath = path.join(testDir, 'downloaded.bin');

      server!.removeAllListeners('request');
      server!.on('request', (req, res) => {
        if (req.url === '/api/v1/download' && req.method === 'POST') {
          // Start download
          res.writeHead(202, { 'Content-Type': 'application/json' });
          res.end(JSON.stringify({ message: 'Download queued' }));
        } else if (req.url?.startsWith('/api/v1/downloads/') && req.method === 'GET') {
          // Status polling - return completed immediately for test
          res.writeHead(200, { 'Content-Type': 'application/json' });
          res.end(
            JSON.stringify({
              cid: 'bafytest123',
              filename: 'test.bin',
              state: 'completed',
              progress: 1.0,
              downloaded_bytes: 100,
              total_bytes: 100,
            })
          );
        } else if (req.url?.startsWith('/api/v1/assets/') && req.method === 'GET') {
          // Get file data
          res.writeHead(200);
          res.end(Buffer.from('downloaded data'));
        } else {
          res.writeHead(404);
          res.end();
        }
      });

      const client = new StonkAgentsClient({ daemonUrl: serverUrl });
      await client.download('bafytest123', outputPath);

      // Verify file was written
      expect(fs.existsSync(outputPath)).toBe(true);
      const content = fs.readFileSync(outputPath, 'utf8');
      expect(content).toBe('downloaded data');
    });

    // RED TEST 7: download polls status until complete
    it('polls download status and waits for completion', async () => {
      const outputPath = path.join(testDir, 'polled.bin');
      let statusCallCount = 0;

      server!.removeAllListeners('request');
      server!.on('request', (req, res) => {
        if (req.url === '/api/v1/download' && req.method === 'POST') {
          res.writeHead(202, { 'Content-Type': 'application/json' });
          res.end(JSON.stringify({ message: 'Download queued' }));
        } else if (req.url?.includes('/downloads/') && req.method === 'GET') {
          statusCallCount++;
          const state = statusCallCount < 3 ? 'active' : 'completed';
          const progress = statusCallCount < 3 ? 0.5 : 1.0;

          res.writeHead(200, { 'Content-Type': 'application/json' });
          res.end(
            JSON.stringify({
              cid: 'bafytest123',
              filename: 'test.bin',
              state,
              progress,
              downloaded_bytes: progress * 100,
              total_bytes: 100,
            })
          );
        } else if (req.url?.includes('/assets/')) {
          res.writeHead(200);
          res.end(Buffer.from('data'));
        } else {
          res.writeHead(404);
          res.end();
        }
      });

      const client = new StonkAgentsClient({ daemonUrl: serverUrl });
      await client.download('bafytest123', outputPath, { pollInterval: 100 });

      // Should have polled multiple times
      expect(statusCallCount).toBeGreaterThanOrEqual(3);
    });
  });

  describe('full roundtrip', () => {
    // RED TEST 8: share → search → download integration
    it('completes full workflow: share → search → download', async () => {
      const sourceFile = path.join(testDir, 'source.txt');
      const downloadedFile = path.join(testDir, 'downloaded.txt');
      fs.writeFileSync(sourceFile, 'roundtrip test data');

      let sharedCid = '';

      server!.removeAllListeners('request');
      server!.on('request', (req, res) => {
        if (req.url === '/api/v1/share' && req.method === 'POST') {
          sharedCid = 'bafyroundtrip123';
          res.writeHead(201, { 'Content-Type': 'application/json' });
          res.end(
            JSON.stringify({
              cid: sharedCid,
              filename: 'source.txt',
              size: 19,
              manifest_type: 'raw',
              announced_at: '2026-02-01T12:00:00Z',
            })
          );
        } else if (req.url?.includes('/search') && req.method === 'GET') {
          res.writeHead(200, { 'Content-Type': 'application/json' });
          res.end(
            JSON.stringify({
              data: [
                {
                  cid: sharedCid,
                  filename: 'source.txt',
                  mime_type: 'text/plain',
                  size: 19,
                  manifest_type: 'raw',
                  announced_at: '2026-02-01T12:00:00Z',
                  peers: [{ peer_id: 'QmTest', multiaddrs: [] }],
                },
              ],
              total: 1,
              page: 1,
              pageSize: 20,
            })
          );
        } else if (req.url === '/api/v1/download' && req.method === 'POST') {
          res.writeHead(202, { 'Content-Type': 'application/json' });
          res.end(JSON.stringify({ message: 'Download queued' }));
        } else if (req.url?.includes('/downloads/')) {
          res.writeHead(200, { 'Content-Type': 'application/json' });
          res.end(
            JSON.stringify({
              cid: sharedCid,
              filename: 'source.txt',
              state: 'completed',
              progress: 1.0,
              downloaded_bytes: 19,
              total_bytes: 19,
            })
          );
        } else if (req.url?.includes('/assets/')) {
          res.writeHead(200);
          res.end(Buffer.from('roundtrip test data'));
        } else {
          res.writeHead(404);
          res.end();
        }
      });

      const client = new StonkAgentsClient({ daemonUrl: serverUrl });

      // 1. Share
      const shareResult = await client.share(sourceFile);
      expect(shareResult.cid).toBe('bafyroundtrip123');

      // 2. Search
      const searchResponse = await client.search('source');
      expect(searchResponse.data).toHaveLength(1);
      expect(searchResponse.data[0].cid).toBe(sharedCid);

      // 3. Download
      await client.download(sharedCid, downloadedFile, { pollInterval: 50 });
      expect(fs.existsSync(downloadedFile)).toBe(true);

      const content = fs.readFileSync(downloadedFile, 'utf8');
      expect(content).toBe('roundtrip test data');
    });
  });
});
