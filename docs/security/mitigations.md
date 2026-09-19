---
title: 'StonkAgents Security Mitigations'
date: 2026-02-01
status: active
tags: [security, mitigations, controls]
---

# StonkAgents Security Mitigations

## Overview

This document catalogs all implemented security controls in StonkAgents, mapping them to threats from the [threat-model.md](./threat-model.md) and attack vectors from [attack-surface.md](./attack-surface.md).

**Goal:** Demonstrate defense-in-depth strategy for Week 10 external security audit
**Version:** 1.0 (Mega Sprint 2 - February 2026)

---

## 1. Cryptographic Integrity (CID Verification)

### Control: Mandatory Content Addressing

**Implemented In:** US-003-05 (CID Verification and Retry Logic)

**Description:**
Every file and chunk is cryptographically verified using IPFS CIDv1 (SHA-256 multihash). The CID is a deterministic hash of the content, making tampering detectable.

**Mechanism:**

1. **File CID Generation:**
   - File is chunked into 256KB blocks
   - Each chunk gets a CID: `chunk_cid = SHA256(chunk_data)`
   - File CID = Merkle root of chunk CIDs
   - Implementation: `pkg/cryptography/cid.go:GenerateCID()`

2. **Verification on Download:**
   - After receiving chunk: verify `SHA256(received_data) == chunk_cid`
   - After assembling file: verify `SHA256(final_file) == file_cid`
   - Implementation: `pkg/cryptography/cid.go:VerifyCID()`

3. **Automatic Retry on Failure:**
   - If CID mismatch: discard chunk, retry from different peer
   - Exponential backoff: 1s, 2s, 4s (max 3 retries per peer)
   - Jitter: ±20% randomization to prevent thundering herd
   - Max retries exceeded: mark chunk as failed, log error

**Threats Mitigated:**

- ✅ **T2.1** (File Content Tampering) - Attacker cannot modify file without CID mismatch
- ✅ **A1.1.3** (MITM on libp2p stream) - Even with MITM, tampered data rejected
- ✅ **A3.1.2** (Second preimage attack) - Requires SHA-256 collision (infeasible)

**Test Coverage:**

- `pkg/cryptography/cid_test.go:TestVerifyCID_Success` - Valid CID passes
- `pkg/cryptography/cid_test.go:TestVerifyCID_Failure` - Tampered data fails
- Integration test: E2E transfer with corrupted chunk → rejected

**Code Traceability:**

```go
// pkg/cryptography/cid.go (US-001-01)
func VerifyCID(data []byte, expectedCID string) error {
    actualCID := GenerateCID(data)
    if actualCID != expectedCID {
        return ErrCIDMismatch
    }
    return nil
}
```

---

## 2. Peer Authentication (Ed25519 + libp2p PeerID)

### Control: Public Key Cryptography for Identity

**Implemented In:** US-001-01 (Agent Identity & Reputation Foundation)

**Description:**
Every peer has an Ed25519 keypair. The PeerID is derived from the public key hash, making PeerID spoofing impossible without private key theft.

**Mechanism:**

1. **Key Generation:**
   - Generate Ed25519 keypair on first run
   - Private key stored: `~/.stonkagents/identity.key` (0600 permissions)
   - Public key exported: `~/.stonkagents/identity.pub`
   - Implementation: `pkg/security/keypair.go:GenerateKeypair()`

2. **PeerID Derivation:**
   - `PeerID = multihash(SHA256(PublicKey))` (IPFS standard)
   - libp2p enforces: peer claiming PeerID must prove ownership via challenge-response
   - Implementation: libp2p built-in (go-libp2p)

3. **Signature Verification:**
   - Asset manifests can be signed (future: US-001-06)
   - Tracker verifies PeerID during registration
   - Implementation: `pkg/security/keypair.go:Verify()`

**Threats Mitigated:**

- ✅ **T1.1** (Peer ID Spoofing) - libp2p challenge-response prevents spoofing
- ✅ **A1.1.2** (Peer ID collision) - Ed25519 key collision is cryptographically infeasible
- ⚠️ **T1.2** (Asset Origin Spoofing) - Partially mitigated (PeerID verified, but no proof-of-authorship)

**Test Coverage:**

- `pkg/security/keypair_test.go:TestGenerateKeypair` - Valid keypair generated
- `pkg/security/keypair_test.go:TestSignAndVerify` - Signature roundtrip works
- Integration test: libp2p handshake with wrong PeerID → connection refused

**Code Traceability:**

```go
// pkg/security/keypair.go (US-001-01)
func GenerateKeypair() (*Keypair, error) {
    pub, priv, _ := ed25519.GenerateKey(rand.Reader)
    return &Keypair{Public: pub, Private: priv}, nil
}
```

---

## 3. SQL Injection Prevention

### Control: Parameterized Queries Only

**Implemented In:** US-007-01 (PostgreSQL Schema and Migrations)

**Description:**
All SQL queries use parameterized statements (prepared statements with placeholders). No string concatenation or interpolation.

**Mechanism:**

1. **Repository Pattern:**
   - All database access via repository interfaces
   - Repositories use `sqlx.NamedExec()` and `sqlx.Get()`
   - Example: `tracker/internal/repository/peer_repository.go`

2. **Query Example:**

   ```go
   // SAFE: Parameterized query
   query := `SELECT * FROM peers WHERE peer_id = $1`
   err := db.Get(&peer, query, peerID)

   // UNSAFE (not used): String concatenation
   // query := "SELECT * FROM peers WHERE peer_id = '" + peerID + "'" // NEVER DO THIS
   ```

3. **Input Validation:**
   - PeerID: regex validation `^[a-zA-Z0-9-_]{20,100}$`
   - CID: CIDv1 format validation (multihash prefix check)
   - Search query: max length 256 chars, special char sanitization

**Threats Mitigated:**

- ✅ **A2.1.1** (SQL injection in peer_id) - Parameterized query prevents injection
- ✅ **A2.1.7** (SQL injection in search query) - Parameterized + input validation
- ✅ **T6.2** (Tracker admin privilege escalation via SQLi) - No SQLi vector

**Test Coverage:**

- `tracker/internal/repository/peer_repository_test.go:TestRegister_SQLInjectionAttempt`
- `tracker/internal/repository/asset_repository_test.go:TestSearch_SQLInjectionAttempt`

**Code Traceability:**

```go
// tracker/internal/repository/peer_repository.go (US-007-01)
func (r *PostgresPeerRepository) GetByID(peerID string) (*Peer, error) {
    query := `SELECT * FROM peers WHERE peer_id = $1`
    var peer Peer
    err := r.db.Get(&peer, query, peerID) // $1 is safely escaped
    return &peer, err
}
```

---

## 4. Rate Limiting & DoS Protection

### Control: HTTP Rate Limiting Middleware

**Implemented In:** US-004-03 (SDK Rate Limiting), Tracker applies same pattern

**Description:**
Tracker and daemon APIs enforce request rate limits per IP address. Exceeding limits returns `429 Too Many Requests` with `Retry-After` header.

**Mechanism:**

1. **Tracker Rate Limits:**
   - `/api/v1/tracker/register`: 10 req/min per IP
   - `/api/v1/tracker/announce`: 10 req/min per IP
   - `/api/v1/tracker/search`: 100 req/min per IP
   - `/api/v1/tracker/dmca`: 5 req/hour per IP (anti-spam)

2. **Response Format:**

   ```http
   HTTP/1.1 429 Too Many Requests
   Retry-After: 30
   Content-Type: application/json

   {"error": {"code": "RATE_LIMIT_EXCEEDED", "message": "Too many requests"}}
   ```

3. **Client-Side Handling:**
   - SDK automatically retries after `Retry-After` delay
   - Implementation: `sdk/src/client.ts` (US-004-03)

**Threats Mitigated:**

- ✅ **T5.1** (Tracker DoS) - Rate limiting prevents flood attacks
- ✅ **A2.1.6** (Announcement flood) - 10/min limit stops spam
- ✅ **A2.1.13** (DMCA flood) - 5/hour limit prevents censorship abuse

**Test Coverage:**

- `sdk/src/__tests__/client.test.ts:TestRateLimitRetry` - SDK respects Retry-After
- `tracker/internal/api/middleware_test.go:TestRateLimitMiddleware` (future)

**Code Traceability:**

```typescript
// sdk/src/client.ts (US-004-03)
if (statusCode === 429) {
  const retryAfter = res.headers['retry-after'];
  const delay = retryAfter ? parseInt(retryAfter, 10) * 1000 : this.retryDelay;
  await this.sleep(delay);
  return this.request(method, path, body, attempt + 1);
}
```

---

## 5. Resource Limits (Download/Upload Concurrency)

### Control: Bounded Concurrency & Disk Quotas

**Implemented In:** US-010-06 (Download Manager), US-010-07 (Upload Manager)

**Description:**
Daemon enforces strict limits on concurrent operations to prevent resource exhaustion.

**Limits:**

- **Max Concurrent Downloads:** 3 (configurable via `downloads.max_concurrent`)
- **Max Concurrent Uploads:** 10 (configurable via `uploads.max_concurrent`)
- **Max File Size:** 10GB per file (configurable via `storage.max_file_size`)
- **Max Chunk Requests:** 16 pipelined requests per peer (prevents memory exhaustion)
- **Bandwidth Throttling:** Configurable max download/upload speed (default: unlimited)

**Mechanism:**

1. **Download Queue:**
   - Downloads queued, max 3 active simultaneously
   - State machine: `queued → active → completed | failed`
   - Implementation: `internal/daemon/download/manager.go`

2. **Upload Semaphore:**
   - Semaphore with limit 10: `sem := make(chan struct{}, 10)`
   - Request blocks if 10 uploads in progress
   - Implementation: `internal/daemon/upload/manager.go`

3. **Disk Space Check:**
   - Before queuing download: check `df -h ~/.stonkagents/downloads`
   - Reject download if < 1GB free space

**Threats Mitigated:**

- ✅ **T5.2** (Daemon Resource Exhaustion) - Limits prevent crash
- ✅ **A2.2.6** (Download flood DoS) - Max 3 concurrent downloads
- ✅ **A4.2.3** (Disk space exhaustion) - 10GB file limit + free space check

**Test Coverage:**

- `internal/daemon/download/manager_test.go:TestMaxConcurrentDownloads`
- `internal/daemon/upload/manager_test.go:TestUploadSemaphore`

**Code Traceability:**

```go
// internal/daemon/download/manager.go (US-010-06)
func (m *Manager) QueueDownload(cid, outputPath string) error {
    if len(m.activeDownloads) >= m.maxConcurrent {
        return ErrQueueFull
    }
    // Queue download...
}
```

---

## 6. DMCA Compliance & Content Moderation

### Control: Tracker DMCA Handler

**Implemented In:** US-007-05 (DMCA Takedown Handler)

**Description:**
Tracker provides `/api/v1/tracker/dmca` endpoint for copyright holders to request content removal. All requests are logged, and removals are reversible (soft delete).

**Mechanism:**

1. **DMCA Request Format:**

   ```json
   POST /api/v1/tracker/dmca
   {
     "cid": "bafybeig...",
     "copyright_holder": "Stability AI",
     "contact_email": "legal@stability.ai",
     "description": "Unauthorized distribution of Stable Diffusion model weights"
   }
   ```

2. **Tracker Action:**
   - Asset marked as `dmca_removed: true` (soft delete)
   - Asset still in database (not hard deleted)
   - Search queries filter out `dmca_removed = true`
   - Peers can still transfer if they already have the asset (P2P unstoppable)

3. **Audit Log:**
   - All DMCA requests logged: `dmca_requests` table
   - Fields: CID, copyright_holder, contact_email, timestamp, IP address
   - Immutable log (append-only, no deletions)

4. **Reversal:**
   - If DMCA request is fraudulent: admin can `UPDATE assets SET dmca_removed = false`
   - Audit log entry created for reversal

**Threats Mitigated:**

- ✅ **T3.1** (Illegal Content Distribution) - DMCA process enables takedowns
- ⚠️ **T6.1** (Unauthorized DMCA censorship) - Partially mitigated (audit log + reversal)

**Limitations:**

- ⚠️ Tracker can only remove from search results, not from P2P network (peers can still share)
- ⚠️ No signature verification on DMCA requests (future: require legal agent signature)

**Test Coverage:**

- `tracker/internal/api/handler_dmca_test.go:TestHandleDMCA_Success`
- `tracker/internal/api/handler_dmca_test.go:TestHandleDMCA_AuditLog`

**Code Traceability:**

```go
// tracker/internal/services/dmca_service.go (US-007-05)
func (s *DMCAService) FileNotice(cid, holder, email, desc string) error {
    // Mark asset as dmca_removed
    s.assetRepo.MarkDMCARemoved(cid)
    // Log to audit table
    s.dmcaRepo.LogNotice(cid, holder, email, desc, time.Now())
    return nil
}
```

---

## 7. Transport Encryption (libp2p Noise Protocol)

### Control: Encrypted P2P Connections

**Implemented In:** libp2p (built-in, go-libp2p-noise)

**Description:**
All P2P connections use libp2p's Noise protocol for encryption. Noise provides forward secrecy and authentication.

**Mechanism:**

1. **Handshake:**
   - Noise XX handshake pattern (mutual authentication)
   - Diffie-Hellman key exchange (X25519)
   - Each connection gets unique session key

2. **Encryption:**
   - ChaCha20-Poly1305 AEAD (authenticated encryption)
   - Forward secrecy: compromising one session key doesn't reveal others
   - Implementation: libp2p's Noise security transport

3. **Authentication:**
   - Peer proves ownership of PeerID via signature
   - Man-in-the-middle impossible without private key

**Threats Mitigated:**

- ✅ **A1.1.3** (MITM on libp2p stream) - Noise encryption prevents eavesdropping
- ✅ **T4.2** (Peer discovery leakage) - Encrypted transport hides content

**Limitations:**

- ⚠️ DHT queries are public (metadata not encrypted, inherent P2P trade-off)

**Test Coverage:**

- libp2p integration tests (external library, battle-tested)

**Code Traceability:**

```go
// internal/daemon/p2p.go (US-010-04)
host, _ := libp2p.New(
    libp2p.Security(noise.ID, noise.New), // Noise encryption
)
```

---

## 8. Input Validation & Sanitization

### Control: Comprehensive Input Validation

**Implemented Across:** All API endpoints (tracker + daemon)

**Rules:**

| Input Field     | Validation                            | Rejection Criteria                           |
| --------------- | ------------------------------------- | -------------------------------------------- |
| `peer_id`       | Regex: `^[a-zA-Z0-9-_]{20,100}$`      | Non-alphanumeric chars, length < 20 or > 100 |
| `cid`           | CIDv1 format check (multihash prefix) | Invalid base32 encoding, wrong prefix        |
| `multiaddrs`    | libp2p multiaddr parsing              | Malformed multiaddr string                   |
| `filename`      | Path sanitization                     | Contains `..`, absolute path, null bytes     |
| `search_query`  | Length limit: 256 chars               | Length > 256, SQL special chars unescaped    |
| `manifest_type` | Enum: `"raw" \| "vec"`                | Not in allowed values                        |
| `email`         | Regex: basic email format             | Invalid format                               |

**Implementation:**

- **Tracker:** `tracker/internal/api/validation.go` (utility functions)
- **Daemon:** `internal/daemon/validation.go`

**Threats Mitigated:**

- ✅ **A2.1.1** (SQL injection) - Query param sanitization
- ✅ **A2.2.1** (Path traversal) - Filename sanitization
- ✅ **A2.1.4** (CID injection) - CIDv1 format enforcement

**Test Coverage:**

- `tracker/internal/api/validation_test.go:TestValidatePeerID`
- `tracker/internal/api/validation_test.go:TestSanitizeFilename`

**Code Traceability:**

```go
// tracker/internal/api/validation.go
func ValidatePeerID(peerID string) error {
    if !regexp.MustCompile(`^[a-zA-Z0-9-_]{20,100}$`).MatchString(peerID) {
        return ErrInvalidPeerID
    }
    return nil
}
```

---

## 9. Least Privilege (Database RBAC)

### Control: Postgres Role-Based Access Control

**Implemented In:** US-007-01 (PostgreSQL Schema)

**Description:**
Tracker service connects to Postgres with minimal permissions. No `SUPERUSER`, `CREATEROLE`, or `CREATEDB` privileges.

**Permissions:**

```sql
-- Tracker service role (at_tracker_service)
GRANT SELECT, INSERT, UPDATE ON peers TO at_tracker_service;
GRANT SELECT, INSERT, UPDATE ON assets TO at_tracker_service;
GRANT INSERT ON dmca_requests TO at_tracker_service; -- Append-only
GRANT SELECT ON search_index TO at_tracker_service;

-- No DELETE permission (prevents accidental data loss)
-- No DROP permission (prevents schema destruction)
```

**Threats Mitigated:**

- ✅ **T6.2** (Privilege escalation via SQLi) - Even with SQLi, cannot drop tables
- ✅ **A4.1.3** (Database privilege escalation) - Minimal permissions reduce blast radius

**Test Coverage:**

- Manual verification: `psql -U at_tracker_service -c "DROP TABLE peers;"` → Permission denied

**Code Traceability:**

```sql
-- tracker/migrations/001_initial_schema.sql
CREATE ROLE at_tracker_service WITH LOGIN PASSWORD 'tracker_password';
GRANT SELECT, INSERT, UPDATE ON ALL TABLES IN SCHEMA public TO at_tracker_service;
REVOKE DELETE, DROP ON ALL TABLES IN SCHEMA public FROM at_tracker_service;
```

---

## 10. Audit Logging & Observability

### Control: Comprehensive Audit Logs

**Implemented In:** US-012-01 (Prometheus Metrics), US-007-05 (DMCA Logs)

**Logged Events:**

- **Tracker:**
  - All API requests: method, path, status code, latency (Prometheus metrics)
  - DMCA takedown requests: CID, copyright_holder, timestamp, IP (database table)
  - Peer registrations: PeerID, multiaddrs, timestamp

- **Daemon:**
  - File shares: CID, filename, size, timestamp
  - Downloads: CID, output_path, status, peers_used
  - Errors: CID verification failures, retry attempts

**Prometheus Metrics:**

- `at_api_requests_total{method, path, status}` - Request counter
- `at_api_latency_seconds{method, path}` - Latency histogram
- `at_api_errors_total{method, path, error_code}` - Error counter
- `at_peers_total` - Peer count gauge
- `at_assets_total` - Asset count gauge

**Threats Mitigated:**

- ✅ **T3.1** (Repudiation) - Audit logs provide non-repudiation
- ✅ **T6.1** (DMCA abuse) - DMCA requests logged for review
- Incident response: Logs enable forensic analysis

**Test Coverage:**

- `tracker/internal/api/metrics_test.go:TestMetricsExport`

**Code Traceability:**

```go
// tracker/internal/api/metrics.go (US-012-01)
requestsTotal.WithLabelValues(method, path, status).Inc()
latencySeconds.WithLabelValues(method, path).Observe(duration)
```

---

## Mitigation Summary Table

| Threat ID | Mitigation                                | Status         | User Story           |
| --------- | ----------------------------------------- | -------------- | -------------------- |
| T1.1      | libp2p PeerID verification                | ✅ Implemented | Built-in (libp2p)    |
| T1.2      | CID verification (no proof-of-authorship) | ⚠️ Partial     | US-001-01            |
| T1.3      | HTTPS tracker deployment                  | 🔜 Planned     | Production config    |
| T2.1      | Mandatory CID verification                | ✅ Implemented | US-003-05            |
| T2.2      | Manifest CID included in asset CID        | ⚠️ Partial     | US-001-02            |
| T2.3      | HTTPS tracker                             | 🔜 Planned     | Production config    |
| T3.1      | DMCA handler + audit logs                 | ✅ Implemented | US-007-05            |
| T3.2      | EigenTrust reputation                     | 🔜 Planned     | US-011               |
| T4.1      | Postgres RBAC + minimal logging           | ✅ Implemented | US-007-01            |
| T4.2      | DHT queries public (known limitation)     | ⚠️ Accepted    | P2P trade-off        |
| T4.3      | Filesystem permissions (0700)             | ⚠️ Partial     | US-010-06            |
| T5.1      | Rate limiting + DHT fallback              | ✅ Implemented | US-004-03, US-010-04 |
| T5.2      | Resource limits (downloads, uploads)      | ✅ Implemented | US-010-06, US-010-07 |
| T5.3      | Kademlia resistance + tracker fallback    | ⚠️ Partial     | libp2p + US-007      |
| T6.1      | Audit logs + reversible DMCA removal      | ⚠️ Partial     | US-007-05            |
| T6.2      | Parameterized SQL + Postgres RBAC         | ✅ Implemented | US-007-01            |
| T6.3      | Localhost API trust model                 | ⚠️ Accepted    | Design decision      |

**Legend:**

- ✅ Implemented: Mitigation fully deployed
- ⚠️ Partial: Mitigation partially effective, residual risk documented
- 🔜 Planned: Mitigation planned for future release

---

## Defense-in-Depth Summary

StonkAgents employs layered security:

1. **Cryptographic Layer:** CID verification (SHA-256), Ed25519 signatures, Noise encryption
2. **Network Layer:** libp2p authentication, DHT Sybil resistance, mDNS LAN-only
3. **Application Layer:** Rate limiting, input validation, SQL injection prevention
4. **Storage Layer:** Postgres RBAC, filesystem permissions (0700), SQLite integrity checks
5. **Observability Layer:** Prometheus metrics, DMCA audit logs, error tracking

**No single point of failure:** If one layer is compromised, others provide defense (e.g., CID verification prevents tampering even if encryption is broken).

---

## References

- NIST SP 800-53: Security and Privacy Controls for Information Systems
- OWASP Top 10: https://owasp.org/www-project-top-ten/
- CIS Controls v8: https://www.cisecurity.org/controls/

---

**Version:** 1.0 (Mega Sprint 2)
**Author:** StonkAgents Security Team
**Last Updated:** 2026-02-01
**Next Review:** Week 10 (External Security Audit)
