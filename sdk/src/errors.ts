// Package: sdk/src
// Feature: F-004 (TypeScript SDK)
// Story: US-004-01 (SDK Scaffolding)
// Purpose: Consistent error envelope for all SDK errors

/**
 * Base error class for all StonkAgents SDK errors
 */
export class StonkAgentsError extends Error {
  constructor(
    public readonly code: string,
    message: string,
    public readonly details?: unknown
  ) {
    super(message);
    this.name = 'StonkAgentsError';
    Object.setPrototypeOf(this, StonkAgentsError.prototype);
  }
}

/**
 * HTTP error from daemon API
 */
export class HTTPError extends StonkAgentsError {
  constructor(
    public readonly statusCode: number,
    code: string,
    message: string,
    details?: unknown
  ) {
    super(code, message, details);
    this.name = 'HTTPError';
    Object.setPrototypeOf(this, HTTPError.prototype);
  }
}

/**
 * Network error (connection refused, timeout, etc.)
 */
export class NetworkError extends StonkAgentsError {
  constructor(
    message: string,
    public readonly cause?: Error
  ) {
    super('NETWORK_ERROR', message, { cause: cause?.message });
    this.name = 'NetworkError';
    Object.setPrototypeOf(this, NetworkError.prototype);
  }
}

/**
 * Retry exhausted error after max retries
 */
export class RetryExhaustedError extends StonkAgentsError {
  constructor(
    message: string,
    public readonly attempts: number
  ) {
    super('RETRY_EXHAUSTED', message, { attempts });
    this.name = 'RetryExhaustedError';
    Object.setPrototypeOf(this, RetryExhaustedError.prototype);
  }
}
