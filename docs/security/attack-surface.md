---
title: 'StonkAgents Attack Surface'
date: 2026-02-01
status: active
tags: [security, attack-surface, enumeration]
---

# StonkAgents Attack Surface

## Overview

This document enumerates all attack vectors and entry points for StonkAgents. Understanding the attack surface is critical for threat modeling, penetration testing, and security audits.

**Scope:** All network-exposed services, APIs, and protocols
**Methodology:** Bottom-up enumeration + top-down threat analysis
**Version:** 1.0 (Mega Sprint 2 - February 2026)

---

## Attack Surface Categories

1. **Network Layer** - P2P protocol, DHT, GossipSub, mDNS
2. **API Layer** - Tracker REST API, Daemon HTTP API
3. **Cryptography Layer** - CID generation, Ed25519 signatures, HMAC-SHA256
4. **Storage Layer** - Local file system, database (Postgres, SQLite)
5. **Dependency Layer** - Third-party libraries (libp2p, gorilla/mux, prometheus)

---

## 1. Network Layer Attack Surface

### 1.1 libp2p Protocol (P2P Network)

**Exposure:** Public internet-facing P2P connections
**Protocol:** libp2p over TCP/UDP (default port: 4001)
**Authentication:** PeerID derived from Ed25519 public key
**Encryption:** Noise protocol (libp2p built-in)

**Attack Vectors:**

- **A1.1.1** - Malformed libp2p handshake messages (buffer overflow, protocol confusion)
- **A1.1.2** - Peer ID collision attack (SHA-256 collision on public key hash)
- **A1.1.3** - Man-in-the-middle (MITM) on libp2p stream (pre-encryption handshake)
- **A1.1.4** - Resource exhaustion (connection flooding, max peer limit DoS)

**Entry Points:**

- `internal/daemon/p2p.go:NewP2PHost()` - P2P host initialization
- libp2p protocol handler: `/stonkagents/transfer/1.0.0` (chunk transfer)
- libp2p protocol handler: `/stonkagents/dht/1.0.0` (peer discovery)

**Mitigations:**

- libp2p Noise encryption (post-handshake confidentiality + integrity)
- PeerID = hash(PublicKey) enforced by libp2p
- Connection limits: max 100 peers (configurable, US-010-04)
- Timeout: 30s per connection attempt

---

### 1.2 DHT (Kademlia Distributed Hash Table)

**Exposure:** Public DHT network (IPFS bootstrap nodes)
**Protocol:** Kademlia DHT over libp2p
**Authentication:** None (public DHT)
**Encryption:** libp2p Noise (transport layer)

**Attack Vectors:**

- **A1.2.1** - Sybil attack (create many fake peers to dominate routing)
- **A1.2.2** - Eclipse attack (isolate victim by controlling all its DHT neighbors)
- **A1.2.3** - Routing table poisoning (inject malicious peer IDs into victim's routing table)
- **A1.2.4** - DHT query censorship (malicious peers refuse to route queries for certain CIDs)

**Entry Points:**

- `internal/daemon/p2p.go:setupDHT()` - DHT initialization
- `internal/daemon/p2p.go:FindPeers(ctx, cid)` - DHT peer lookup

**Mitigations:**

- Kademlia XOR distance metric (Sybil resistance)
- Tracker fallback for peer discovery (DHT not required)
- Bootstrap from trusted IPFS nodes (hardcoded list)

**Residual Risk:** Sybil attacks still possible with significant resources (accepted P2P trade-off)

---

### 1.3 GossipSub (Pub/Sub Messaging)

**Exposure:** Public GossipSub mesh network
**Protocol:** GossipSub v1.1 over libp2p
**Topic:** `/stonkagents/assets/1.0.0`
**Authentication:** None (public topic)

**Attack Vectors:**

- **A1.3.1** - Message flooding (spam topic with fake asset announcements)
- **A1.3.2** - Message replay (re-broadcast old announcements to confuse peers)
- **A1.3.3** - Message injection (malicious asset announcements with fake CIDs)
- **A1.3.4** - Topic mesh manipulation (isolate peers by controlling mesh connections)

**Entry Points:**

- `internal/daemon/pubsub/gossipsub.go:PublishAssetAnnouncement()` - Publish to topic
- `internal/daemon/pubsub/gossipsub.go:SubscribeTopic()` - Subscribe to announcements

**Mitigations:**

- GossipSub built-in deduplication (message ID hash)
- CID verification before download (malicious announcements ignored after download attempt)
- Future: Message signatures (Ed25519 signature on asset manifest, US-001-06)

**Residual Risk:** Announcement spam can degrade performance (no signature verification in MVP)

---

### 1.4 mDNS (Local Network Discovery)

**Exposure:** Local network (LAN) only
**Protocol:** mDNS (Multicast DNS)
**Service Name:** `_stonkagents._tcp.local`
**Authentication:** None (LAN trust model)

**Attack Vectors:**

- **A1.4.1** - LAN spoofing (malicious peer announces fake service on LAN)
- **A1.4.2** - mDNS amplification (use mDNS for DDoS reflection attack)

**Entry Points:**

- `internal/daemon/p2p.go:setupMDNS()` - mDNS initialization

**Mitigations:**

- LAN-only exposure (not internet-routable)
- mDNS disabled by default (opt-in via config: `p2p.enable_mdns: true`)
- PeerID verification after discovery (same as DHT peers)

**Residual Risk:** Trusted LAN assumption (if LAN is compromised, mDNS is vulnerable)

---

## 2. API Layer Attack Surface

### 2.1 Tracker REST API

**Exposure:** Public internet-facing HTTP server
**Port:** 7842 (default, configurable)
**Protocol:** HTTP (⚠️ HTTPS required for production)
**Authentication:** None (public tracker in MVP)

**Endpoints:**

| Endpoint                   | Method | Attack Vectors                                                                                                                        | Mitigations                                                                                     |
| -------------------------- | ------ | ------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------- |
| `/api/v1/tracker/register` | POST   | A2.1.1: SQL injection in peer_id<br>A2.1.2: JSON injection (oversized payload)<br>A2.1.3: PeerID spoofing                             | Parameterized SQL (US-007-01)<br>Max body size: 1MB<br>PeerID format validation                 |
| `/api/v1/tracker/announce` | POST   | A2.1.4: CID injection (malformed CID)<br>A2.1.5: Manifest injection (oversized metadata)<br>A2.1.6: Flood attack (spam announcements) | CID validation (must be valid CIDv1)<br>Max manifest size: 10KB<br>Rate limiting: 10/min per IP |
| `/api/v1/tracker/search`   | GET    | A2.1.7: SQL injection in query parameter<br>A2.1.8: Query parameter overflow<br>A2.1.9: Semantic search vector injection              | Parameterized SQL + HNSW sanitization<br>Query max length: 256 chars<br>Vector normalization    |
| `/api/v1/tracker/peers`    | GET    | A2.1.10: DoS via large limit param<br>A2.1.11: Unauthorized peer list scraping                                                        | Max limit: 100 peers<br>Rate limiting: 100/min per IP                                           |
| `/api/v1/tracker/dmca`     | POST   | A2.1.12: Fake DMCA notice (censorship attack)<br>A2.1.13: DMCA flood (spam takedowns)                                                 | Audit log (all requests logged)<br>Rate limiting: 5/hour per IP<br>Reversible removal           |
| `/health`                  | GET    | A2.1.14: Health check abuse (info disclosure)                                                                                         | No sensitive data in response                                                                   |
| `/metrics`                 | GET    | A2.1.15: Metrics scraping (info disclosure)                                                                                           | Prometheus metrics are public by design                                                         |

**Entry Points:**

- `tracker/internal/api/server.go:setupRoutes()` - Route registration
- `tracker/internal/api/handler_*.go` - HTTP handlers

**Mitigations:**

- Rate limiting: 100 req/min per IP (US-004-03, tracker applies same pattern)
- Request size limits: max 1MB body, max 1MB headers
- Input validation on all endpoints (JSON schema validation)
- HTTPS required for production (TLS 1.3)

**Residual Risk:** No authentication in MVP (public tracker model)

---

### 2.2 Daemon HTTP API

**Exposure:** Localhost only (127.0.0.1)
**Port:** 7841 (default, configurable)
**Protocol:** HTTP (no TLS required for localhost)
**Authentication:** None (localhost trust model)

**Endpoints:**

| Endpoint                         | Method | Attack Vectors                                                                                                                        | Mitigations                                                                          |
| -------------------------------- | ------ | ------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------ |
| `/api/v1/share`                  | POST   | A2.2.1: Path traversal in file_path<br>A2.2.2: Symlink attack (share system files)<br>A2.2.3: Disk space exhaustion (share huge file) | Absolute path validation<br>Symlink resolution check<br>Max file size: 10GB (config) |
| `/api/v1/download`               | POST   | A2.2.4: CID injection (malformed CID)<br>A2.2.5: Path traversal in output_path<br>A2.2.6: Disk space exhaustion (download flood)      | CID validation<br>Output path sanitization<br>Max concurrent downloads: 3            |
| `/api/v1/downloads/{cid}/status` | GET    | A2.2.7: CID enumeration (leak download history)                                                                                       | Localhost-only (local malware already has file access)                               |
| `/api/v1/assets/search`          | GET    | A2.2.8: Query injection<br>A2.2.9: DoS via expensive semantic search                                                                  | Tracker forward (same mitigations as A2.1.7)<br>Result caching                       |
| `/health`                        | GET    | A2.2.10: Info disclosure (version, uptime)                                                                                            | Minimal info (similar to tracker)                                                    |

**Entry Points:**

- `internal/daemon/api.go:setupRoutes()` - Route registration
- `internal/daemon/api.go:handle*()` - HTTP handlers

**Mitigations:**

- Localhost binding only: `host: 127.0.0.1` (not `0.0.0.0`)
- No network exposure (requires local process access)
- Resource limits: max 3 concurrent downloads, max 10GB file size
- Future: Optional JWT authentication (US-001-06)

**Residual Risk:** Local malware can abuse API (accepted localhost trust model, similar to Docker daemon)

---

### 2.3 TypeScript SDK (HTTP Client)

**Exposure:** Indirect (calls daemon API)
**Protocol:** HTTP to localhost:7841
**Authentication:** None (inherits daemon trust model)

**Attack Vectors:**

- **A2.3.1** - Malicious daemon hijacking (rogue daemon on localhost:7841)
- **A2.3.2** - SDK dependency hijacking (npm package poisoning)
- **A2.3.3** - Prototype pollution via JSON responses

**Entry Points:**

- `sdk/src/client.ts:request()` - HTTP request wrapper

**Mitigations:**

- Zero runtime dependencies (no transitive dependency attack surface)
- Strict TypeScript mode (no `any` types, prototype pollution resistant)
- SDK validates all API responses (TypeScript interfaces)

**Residual Risk:** Rogue daemon can impersonate legitimate daemon (localhost trust model)

---

## 3. Cryptography Layer Attack Surface

### 3.1 CID Generation (Content Addressing)

**Algorithm:** IPFS CIDv1 (SHA-256 multihash)
**Library:** go-cid (IPFS official library)
**Use:** File integrity verification, content addressing

**Attack Vectors:**

- **A3.1.1** - SHA-256 collision (break CID integrity)
- **A3.1.2** - Second preimage attack (find different file with same CID)
- **A3.1.3** - CID parsing bug (malformed CID crashes daemon)

**Entry Points:**

- `pkg/cryptography/cid.go:GenerateCID()` - CID generation
- `pkg/cryptography/cid.go:VerifyCID()` - CID verification

**Mitigations:**

- SHA-256 is collision-resistant (2^256 complexity)
- CID parsing uses battle-tested `go-cid` library (IPFS standard)
- Verification on every chunk and final file (US-003-05)

**Residual Risk:** SHA-256 collision is theoretical (accepted cryptographic assumption)

---

### 3.2 Ed25519 Signatures (Peer Identity)

**Algorithm:** Ed25519 (EdDSA)
**Library:** Go standard library `crypto/ed25519`
**Use:** PeerID generation, future manifest signatures

**Attack Vectors:**

- **A3.2.1** - Private key extraction (from daemon storage)
- **A3.2.2** - Signature forgery (break Ed25519)
- **A3.2.3** - Key generation weakness (predictable RNG)

**Entry Points:**

- `pkg/security/keypair.go:GenerateKeypair()` - Key generation
- `pkg/security/keypair.go:Sign()` - Signature generation
- `pkg/security/keypair.go:Verify()` - Signature verification

**Mitigations:**

- Private key stored with 0600 permissions (`~/.stonkagents/identity.key`)
- Go stdlib uses `crypto/rand` (OS-level CSPRNG)
- Ed25519 is EUF-CMA secure (signature forgery is infeasible)

**Residual Risk:** Private key exposure via filesystem access (mitigated by OS permissions)

---

### 3.3 HMAC-SHA256 (WebSocket JWT)

**Algorithm:** HMAC-SHA256
**Use:** WebSocket JWT token generation (tracker → client auth)
**Secret:** Environment variable `JWT_SECRET` (min 32 bytes)

**Attack Vectors:**

- **A3.3.1** - Secret leakage (environment variable exposure)
- **A3.3.2** - HMAC collision (forge JWT signature)
- **A3.3.3** - Secret brute-force (weak secret)

**Entry Points:**

- `pkg/security/jwt.go:GenerateToken()` - JWT generation
- `pkg/security/jwt.go:ValidateToken()` - JWT verification
- `tracker/internal/websocket/auth.go:GenerateToken()` - Tracker JWT

**Mitigations:**

- Secret read from environment variable (no hardcoded secret)
- Min secret length enforced: 32 bytes (256 bits)
- HMAC-SHA256 is collision-resistant

**Residual Risk:** Weak secret configuration by user (documentation recommends strong random secret)

---

## 4. Storage Layer Attack Surface

### 4.1 PostgreSQL Database (Tracker)

**Exposure:** Localhost only (default), or network-exposed (production)
**Protocol:** PostgreSQL wire protocol (port 5432)
**Authentication:** Password (from `DATABASE_URL` env var)

**Attack Vectors:**

- **A4.1.1** - SQL injection via tracker API (A2.1.1, A2.1.7)
- **A4.1.2** - Credential theft (`DATABASE_URL` environment variable)
- **A4.1.3** - Database privilege escalation (tracker service has excessive permissions)
- **A4.1.4** - Data exfiltration (tracker database backup stolen)

**Entry Points:**

- `tracker/internal/repository/*.go` - Database queries

**Mitigations:**

- Parameterized queries (no string concatenation, US-007-01)
- Postgres RBAC: tracker service has minimal permissions (INSERT, SELECT only)
- `DATABASE_URL` read from environment (no hardcoded credentials)
- Future: Encrypt database backups (pg_dump + GPG)

**Residual Risk:** Database credential theft exposes all tracker data

---

### 4.2 SQLite Database (Daemon Chunks)

**Exposure:** Local file system (`~/.stonkagents/chunks.db`)
**Protocol:** Embedded (no network exposure)
**Authentication:** File system permissions (0600)

**Attack Vectors:**

- **A4.2.1** - Database corruption (malformed chunk data)
- **A4.2.2** - File permission escalation (chmod 777 by malware)
- **A4.2.3** - Disk space exhaustion (unbounded chunk storage)

**Entry Points:**

- `internal/daemon/storage/chunk_store.go` - SQLite operations

**Mitigations:**

- SQLite integrity checks (PRAGMA integrity_check)
- File permissions enforced on startup: 0600
- Max storage limit: configurable (default 100GB)

**Residual Risk:** Local malware can access SQLite database (accepted OS trust model)

---

### 4.3 File System (Downloaded Assets)

**Exposure:** Local file system (`~/.stonkagents/assets/`, `~/.stonkagents/downloads/`)
**Permissions:** 0700 (directory), 0600 (files)
**Encryption:** None (plaintext storage)

**Attack Vectors:**

- **A4.3.1** - Unauthorized file access (malware, physical access)
- **A4.3.2** - Symlink attack (download overwrites system file)
- **A4.3.3** - Path traversal (download to arbitrary location)

**Entry Points:**

- `internal/daemon/download/manager.go` - File writing
- `internal/daemon/chunking/chunker.go` - File reading (share)

**Mitigations:**

- Output path sanitization: reject `..`, absolute paths, symlinks
- Directory permissions: 0700 (user-only access)
- Future: Optional at-rest encryption with user passphrase

**Residual Risk:** No encryption at rest (accepted for MVP, planned for v1.0)

---

## 5. Dependency Layer Attack Surface

### 5.1 Go Dependencies (go.mod)

**Count:** 50+ dependencies (direct + transitive)
**Supply Chain Risk:** High (any dependency can introduce vulnerabilities)

**Critical Dependencies:**

| Dependency                 | Use            | Attack Vectors                                            |
| -------------------------- | -------------- | --------------------------------------------------------- |
| `libp2p/go-libp2p`         | P2P networking | A5.1.1: libp2p protocol bug (buffer overflow, DoS)        |
| `gorilla/mux`              | HTTP routing   | A5.1.2: Path traversal bug, header injection              |
| `lib/pq` (Postgres driver) | Database       | A5.1.3: SQL injection bypass, connection hijacking        |
| `prometheus/client_golang` | Metrics        | A5.1.4: DoS via metrics endpoint                          |
| `mattn/go-sqlite3`         | Embedded DB    | A5.1.5: SQLite C code vulnerabilities (memory corruption) |

**Mitigations:**

- `go mod verify` on every build (integrity check)
- Dependabot / Renovate for automated dependency updates
- Regular `go list -m -u all` to check for updates
- Pin dependencies with `go.mod` (no floating versions)

**Residual Risk:** Zero-day vulnerabilities in dependencies (monitored via security advisories)

---

### 5.2 TypeScript SDK Dependencies (npm)

**Count:** 0 runtime dependencies (zero-dependency SDK)
**Supply Chain Risk:** Low (no transitive dependencies)

**Dev Dependencies:** ~10 (TypeScript, Jest, ESLint, Prettier)

**Attack Vectors:**

- **A5.2.1** - Dev dependency compromise (malicious test runner)
- **A5.2.2** - npm package hijacking (namespace takeover)

**Mitigations:**

- Zero runtime dependencies (no attack surface in production)
- `npm ci` for deterministic builds (lockfile integrity)
- Dev dependencies only used in build pipeline (not shipped)

**Residual Risk:** Dev dependency compromise (CI environment isolation mitigates risk)

---

## Attack Surface Summary

### Total Attack Vectors Identified: 50+

**By Category:**

- Network Layer: 14 vectors
- API Layer: 19 vectors
- Cryptography Layer: 9 vectors
- Storage Layer: 8 vectors
- Dependency Layer: 6 vectors

**By Severity:**

- Critical: 8 vectors (SQL injection, CID tampering, tracker DoS)
- High: 15 vectors (Sybil attack, DMCA abuse, private key theft)
- Medium: 20 vectors (mDNS spoofing, info disclosure, GossipSub spam)
- Low: 7 vectors (health check abuse, local malware access)

**Mitigation Status:**

- ✅ Fully Mitigated: 30 vectors
- ⚠️ Partially Mitigated: 15 vectors
- 🔜 Planned (Post-MVP): 5 vectors

---

## Recommendations for External Audit (Week 10)

1. **Penetration Testing Focus Areas:**
   - Tracker API: SQL injection, rate limit bypass, DMCA abuse
   - Daemon API: Path traversal, symlink attacks, DoS
   - P2P Protocol: libp2p fuzzing, DHT Sybil attack simulation

2. **Code Review Priorities:**
   - `tracker/internal/repository/*.go` (SQL injection surface)
   - `pkg/cryptography/cid.go` (CID verification correctness)
   - `internal/daemon/download/manager.go` (path sanitization)

3. **Fuzzing Targets:**
   - Protocol buffer messages (asset manifests)
   - HTTP API endpoints (malformed JSON, oversized payloads)
   - CID parsing (invalid CIDv1 formats)

---

## References

- OWASP Attack Surface Analysis: https://owasp.org/www-community/Attack_Surface_Analysis_Cheat_Sheet
- NIST SP 800-30: Guide for Conducting Risk Assessments
- CWE Top 25: https://cwe.mitre.org/top25/

---

**Version:** 1.0 (Mega Sprint 2)
**Author:** StonkAgents Security Team
**Last Updated:** 2026-02-01
**Next Review:** Week 10 (External Security Audit)
