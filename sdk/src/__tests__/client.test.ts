// Package: sdk/src/__tests__
// Feature: F-004 (TypeScript SDK)
// Story: US-004-01 (SDK Scaffolding)
// Purpose: TDD tests for StonkAgentsClient

import { StonkAgentsClient } from '../client';
import { HTTPError, NetworkError } from '../errors';
import * as http from 'node:http';

// Mock HTTP server for testing
let server: http.Server | null = null;
let serverUrl: string;

beforeAll(async () => {
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
});

describe('StonkAgentsClient', () => {
  describe('constructor', () => {
    it('creates client with daemonUrl', () => {
      const client = new StonkAgentsClient({ daemonUrl: 'http://localhost:7841' });
      expect(client).toBeInstanceOf(StonkAgentsClient);
    });

    it('creates client with optional token', () => {
      const client = new StonkAgentsClient({
        daemonUrl: 'http://localhost:7841',
        token: 'test-token',
      });
      expect(client).toBeInstanceOf(StonkAgentsClient);
    });
  });

  describe('healthCheck', () => {
    // RED TEST 1: healthCheck returns daemon status
    it('returns daemon status on success', async () => {
      // Setup mock server to respond with health data
      server!.removeAllListeners('request');
      server!.on('request', (req, res) => {
        if (req.url === '/health' && req.method === 'GET') {
          res.writeHead(200, { 'Content-Type': 'application/json' });
          res.end(JSON.stringify({ status: 'healthy', version: '0.1.0' }));
        } else {
          res.writeHead(404);
          res.end();
        }
      });

      const client = new StonkAgentsClient({ daemonUrl: serverUrl });
      const health = await client.healthCheck();

      expect(health).toEqual({
        status: 'healthy',
        version: '0.1.0',
      });
    });

    // RED TEST 2: throws HTTPError on 4xx
    it('throws HTTPError on 404', async () => {
      server!.removeAllListeners('request');
      server!.on('request', (req, res) => {
        res.writeHead(404, { 'Content-Type': 'application/json' });
        res.end(
          JSON.stringify({
            error: {
              code: 'NOT_FOUND',
              message: 'Route not found',
            },
          })
        );
      });

      const client = new StonkAgentsClient({ daemonUrl: serverUrl });

      await expect(client.healthCheck()).rejects.toThrow(HTTPError);
      await expect(client.healthCheck()).rejects.toThrow('Route not found');
    });

    // RED TEST 3: retries on 5xx error
    it('retries on 5xx error and eventually succeeds', async () => {
      let attempts = 0;
      server!.removeAllListeners('request');
      server!.on('request', (req, res) => {
        attempts++;
        if (attempts < 3) {
          // Fail first 2 attempts with 500
          res.writeHead(500, { 'Content-Type': 'application/json' });
          res.end(JSON.stringify({ error: { code: 'INTERNAL_ERROR', message: 'Server error' } }));
        } else {
          // Succeed on 3rd attempt
          res.writeHead(200, { 'Content-Type': 'application/json' });
          res.end(JSON.stringify({ status: 'healthy', version: '0.1.0' }));
        }
      });

      const client = new StonkAgentsClient({ daemonUrl: serverUrl });
      const health = await client.healthCheck();

      expect(health.status).toBe('healthy');
      expect(attempts).toBe(3); // Verify it retried
    });

    // RED TEST 4: exhausts retries after max attempts
    it('throws RetryExhaustedError after 3 failed attempts', async () => {
      let attempts = 0;
      server!.removeAllListeners('request');
      server!.on('request', (req, res) => {
        attempts++;
        res.writeHead(500, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ error: { code: 'INTERNAL_ERROR', message: 'Server error' } }));
      });

      const client = new StonkAgentsClient({ daemonUrl: serverUrl });

      await expect(client.healthCheck()).rejects.toThrow('Retry exhausted after 3 attempts');
      expect(attempts).toBe(3); // Verify it tried 3 times
    });

    // RED TEST 5: throws NetworkError on connection refused
    it('throws NetworkError when daemon is unreachable', async () => {
      const client = new StonkAgentsClient({ daemonUrl: 'http://localhost:9999' }); // Non-existent port

      await expect(client.healthCheck()).rejects.toThrow(NetworkError);
      // Error message varies by system (ECONNREFUSED, ETIMEDOUT, etc.)
    });
  });

  describe('rate limiting (429 handling)', () => {
    // RED TEST 6: handles 429 with Retry-After header
    it('retries after 429 with Retry-After delay', async () => {
      let attempts = 0;
      server!.removeAllListeners('request');
      server!.on('request', (req, res) => {
        attempts++;
        if (attempts === 1) {
          // First attempt: rate limited with Retry-After: 1 (second)
          res.writeHead(429, {
            'Content-Type': 'application/json',
            'Retry-After': '1',
          });
          res.end(
            JSON.stringify({
              error: {
                code: 'RATE_LIMIT_EXCEEDED',
                message: 'Rate limit exceeded',
              },
            })
          );
        } else {
          // Second attempt: success
          res.writeHead(200, { 'Content-Type': 'application/json' });
          res.end(JSON.stringify({ status: 'healthy', version: '0.1.0' }));
        }
      });

      const client = new StonkAgentsClient({ daemonUrl: serverUrl });
      const startTime = Date.now();
      const health = await client.healthCheck();
      const elapsed = Date.now() - startTime;

      expect(health.status).toBe('healthy');
      expect(attempts).toBe(2);
      // Should have waited ~1 second (allow 800-1500ms range for timing variance)
      expect(elapsed).toBeGreaterThanOrEqual(800);
      expect(elapsed).toBeLessThan(1500);
    });

    // RED TEST 7: respects Retry-After in seconds
    it('parses Retry-After as seconds', async () => {
      server!.removeAllListeners('request');
      server!.on('request', (req, res) => {
        res.writeHead(429, {
          'Content-Type': 'application/json',
          'Retry-After': '2',
        });
        res.end(JSON.stringify({ error: { code: 'RATE_LIMIT', message: 'Rate limited' } }));
      });

      const client = new StonkAgentsClient({ daemonUrl: serverUrl, maxRetries: 1 });

      await expect(client.healthCheck()).rejects.toThrow('Rate limit exceeded');
    });

    // RED TEST 8: falls back to default delay if Retry-After missing
    it('uses default delay when Retry-After header missing', async () => {
      let attempts = 0;
      server!.removeAllListeners('request');
      server!.on('request', (req, res) => {
        attempts++;
        if (attempts === 1) {
          // Rate limited without Retry-After header
          res.writeHead(429, { 'Content-Type': 'application/json' });
          res.end(
            JSON.stringify({
              error: { code: 'RATE_LIMIT', message: 'Rate limited' },
            })
          );
        } else {
          res.writeHead(200, { 'Content-Type': 'application/json' });
          res.end(JSON.stringify({ status: 'healthy', version: '0.1.0' }));
        }
      });

      const client = new StonkAgentsClient({ daemonUrl: serverUrl });
      const startTime = Date.now();
      await client.healthCheck();
      const elapsed = Date.now() - startTime;

      expect(attempts).toBe(2);
      // Should use default 1s delay
      expect(elapsed).toBeGreaterThanOrEqual(800);
    });
  });
});
