---
title: 'StonkAgents Security Threat Model'
date: 2026-02-01
status: active
tags: [security, threat-model, stride]
---

# StonkAgents Security Threat Model

## Overview

This document provides a STRIDE-based threat model for the StonkAgents P2P file sharing protocol. StonkAgents enables autonomous AI agents to share data, embeddings, and model weights in a decentralized network.

**Scope:** StonkAgents daemon, tracker, TypeScript SDK, and P2P protocol
**Methodology:** STRIDE (Spoofing, Tampering, Repudiation, Information Disclosure, Denial of Service, Elevation of Privilege)
**Version:** 1.0 (Mega Sprint 2 - February 2026)

---

## System Architecture (Threat Surface)

```
┌─────────────────┐
│  AI Agent       │  (TypeScript SDK or Python client)
│  (Autonomous)   │
└────────┬────────┘
         │ HTTP/REST
         ▼
┌─────────────────┐
│  Local Daemon   │  (Go binary, localhost:7841)
│  - P2P Engine   │
│  - HTTP API     │
│  - File Storage │
└────────┬────────┘
         │ libp2p (DHT, GossipSub, mDNS)
         ▼
┌─────────────────┐         ┌─────────────────┐
│  Peer Daemons   │◄───────►│  Tracker        │
│  (P2P Network)  │         │  (Centralized)  │
└─────────────────┘         └─────────────────┘
```

---

## STRIDE Analysis

### 1. Spoofing (Identity Threats)

**Threat:** Malicious peer impersonates a legitimate peer to serve poisoned data or gain reputation.

#### T1.1: Peer ID Spoofing

- **Description:** Attacker claims a PeerID that doesn't match their public key
- **Attack Vector:** Malformed libp2p handshake, PeerID generation collision
- **Impact:** High - could distribute malicious files, gain unearned reputation
- **Likelihood:** Medium - libp2p PeerID is derived from public key, but implementation bugs possible
- **Mitigation:**
  - libp2p automatically verifies PeerID = hash(PublicKey)
  - Ed25519 signature verification on all asset announcements
  - Tracker verifies PeerID during registration
- **Status:** ✅ Mitigated (libp2p built-in + tracker verification)

#### T1.2: Asset Origin Spoofing

- **Description:** Attacker claims to be the original publisher of an asset
- **Attack Vector:** Announcing someone else's CID with their own PeerID
- **Impact:** Medium - could dilute reputation, but doesn't change asset content (CID-verified)
- **Likelihood:** High - no proof-of-authorship in MVP
- **Mitigation:**
  - CID verification ensures content integrity (can't change file)
  - Future: Signed manifests with Ed25519 keypair (post-MVP)
  - Reputation system penalizes bad actors over time
- **Status:** ⚠️ Partially Mitigated (CID verification prevents tampering, but not false claims)

#### T1.3: Tracker Impersonation

- **Description:** Attacker runs fake tracker to serve malicious peer lists
- **Attack Vector:** DNS hijacking, MITM attack on tracker URL
- **Impact:** Critical - could isolate users, serve only malicious peers
- **Likelihood:** Low - requires network-level attack
- **Mitigation:**
  - HTTPS for tracker communication (TLS verification)
  - Future: DHT-only mode removes tracker dependency
  - Hardcoded bootstrap peers for fallback
- **Status:** ⚠️ Requires HTTPS deployment (MVP uses HTTP for localhost testing)

---

### 2. Tampering (Data Integrity Threats)

**Threat:** Attacker modifies files, manifests, or protocol messages in transit or at rest.

#### T2.1: File Content Tampering

- **Description:** Malicious peer serves corrupted or malicious file data
- **Attack Vector:** MITM on P2P transfer, malicious chunk serving
- **Impact:** Critical - AI agent could ingest poisoned data, backdoored model weights
- **Likelihood:** High - network adversary can modify unencrypted P2P streams
- **Mitigation:**
  - **CID verification** on every chunk and final file (cryptographic integrity check)
  - File CID = Merkle root of chunk CIDs (IPFS CIDv1 algorithm)
  - Download rejected if CID mismatch
  - Chunk-level verification prevents partial tampering
- **Status:** ✅ Fully Mitigated (mandatory CID verification in US-003-05)

#### T2.2: Manifest Tampering

- **Description:** Attacker modifies asset manifest (filename, tags, manifest_type)
- **Attack Vector:** MITM on GossipSub announcement, malicious tracker response
- **Impact:** Medium - could cause misclassification (.at-vec served as .at-raw)
- **Likelihood:** Medium - GossipSub has no built-in signature verification in MVP
- **Mitigation:**
  - Manifest CID included in asset CID calculation
  - Tracker stores canonical manifest for each CID
  - Future: Ed25519 signatures on manifests (US-001-06, post-MVP)
- **Status:** ⚠️ Partially Mitigated (CID prevents tampering, but metadata trust relies on tracker)

#### T2.3: Protocol Message Tampering

- **Description:** Attacker modifies DHT queries, tracker responses, search results
- **Attack Vector:** MITM on HTTP tracker API, DHT query injection
- **Impact:** High - could hide legitimate assets, promote malicious ones
- **Likelihood:** Medium - HTTP tracker vulnerable, DHT resistant due to Kademlia routing
- **Mitigation:**
  - HTTPS for tracker API (TLS encryption + integrity)
  - libp2p DHT uses signed messages (ECDSA signatures)
  - Client-side CID verification before download
- **Status:** ⚠️ HTTPS required for production tracker deployment

---

### 3. Repudiation (Non-Repudiation Threats)

**Threat:** Users deny performing malicious actions (uploading illegal content, DMCA violations).

#### T3.1: Illegal Content Distribution

- **Description:** User uploads illegal content (pirated models, CSAM) and denies responsibility
- **Attack Vector:** Anonymous P2P network, no audit trail
- **Impact:** Critical - legal liability, platform shutdown
- **Likelihood:** High - open P2P network
- **Mitigation:**
  - **DMCA handler** (US-007-05) logs all takedown requests with CID and timestamp
  - PeerID tied to Ed25519 keypair (cryptographic identity)
  - Tracker logs all `announce` requests with PeerID, CID, timestamp
  - Future: Reputation penalties for DMCA violations
- **Status:** ✅ Mitigated (audit logs + DMCA handler implemented)

#### T3.2: Bandwidth Theft (Free-Riding)

- **Description:** Users download without uploading (leeching)
- **Attack Vector:** Modified daemon that doesn't seed
- **Impact:** Low - degrades network health, but not a security threat
- **Likelihood:** High - rational economic behavior
- **Mitigation:**
  - EigenTrust reputation system (future, US-011)
  - Upload/download ratio tracking
  - Peers can refuse to serve low-reputation nodes
- **Status:** 🔜 Planned (US-011 EigenTrust implementation)

---

### 4. Information Disclosure (Privacy Threats)

**Threat:** Sensitive information exposed to unauthorized parties.

#### T4.1: Asset Metadata Leakage

- **Description:** Tracker logs reveal what assets a peer has shared/downloaded
- **Attack Vector:** Tracker database breach, log file exposure
- **Impact:** Medium - reveals user interests, AI agent behavior
- **Likelihood:** Medium - centralized tracker is single point of failure
- **Mitigation:**
  - Database access control (Postgres role-based permissions)
  - Tracker audit logs exclude sensitive metadata
  - Future: DHT-only mode removes tracker visibility
- **Status:** ✅ Mitigated (minimal metadata logging, Postgres RBAC)

#### T4.2: Peer Discovery Leakage

- **Description:** DHT queries reveal which CIDs a peer is searching for
- **Attack Vector:** Passive DHT monitoring, Sybil attack on DHT
- **Impact:** Low - reveals search behavior, but not download completion
- **Likelihood:** High - DHT queries are public
- **Mitigation:**
  - Accept as inherent P2P trade-off (similar to BitTorrent)
  - Future: Private information retrieval (PIR) for DHT queries
  - Use tracker for initial search (hides queries from DHT)
- **Status:** ⚠️ Known Limitation (no mitigation in MVP, documented risk)

#### T4.3: Local Storage Exposure

- **Description:** Daemon's `~/.stonkagents/` directory contains downloaded assets
- **Attack Vector:** File system access, malware, physical access
- **Impact:** High - exposes all downloaded files and metadata
- **Likelihood:** Medium - local file system security varies
- **Mitigation:**
  - File permissions: 0700 on `~/.stonkagents/` directory
  - User responsible for OS-level disk encryption
  - Future: Optional at-rest encryption with user passphrase
- **Status:** ⚠️ Partially Mitigated (filesystem permissions set, encryption future)

---

### 5. Denial of Service (Availability Threats)

**Threat:** Attacker makes the system unavailable or unusably slow.

#### T5.1: Tracker DoS

- **Description:** Flood tracker with `announce` or `search` requests
- **Attack Vector:** Botnet, distributed attack
- **Impact:** Critical - tracker unavailable, no asset discovery
- **Likelihood:** High - centralized tracker is single point of failure
- **Mitigation:**
  - Rate limiting on tracker API endpoints (429 responses, US-004-03)
  - Request throttling: 100 req/min per IP (configurable)
  - Future: DHT-only mode removes tracker dependency
  - Cloudflare DDoS protection (production deployment)
- **Status:** ✅ Mitigated (rate limiting implemented, DHT fallback available)

#### T5.2: Daemon Resource Exhaustion

- **Description:** Attacker floods daemon with chunk requests, fills disk with downloads
- **Attack Vector:** Malicious peer, coordinated swarm
- **Impact:** High - daemon crash, disk full
- **Likelihood:** Medium - requires compromised peers
- **Mitigation:**
  - Download manager limits: max 3 concurrent downloads (US-010-06)
  - Upload manager limits: max 10 concurrent uploads (US-010-07)
  - Disk space check before queuing downloads
  - Bandwidth throttling (configurable, US-010-06)
- **Status:** ✅ Mitigated (resource limits implemented)

#### T5.3: Sybil Attack on DHT

- **Description:** Attacker creates many fake peers to dominate DHT routing
- **Attack Vector:** Eclipse attack, routing table poisoning
- **Impact:** High - DHT routing compromised, assets undiscoverable
- **Likelihood:** Medium - requires significant resources
- **Mitigation:**
  - libp2p Kademlia DHT has built-in Sybil resistance (XOR distance metric)
  - Tracker provides fallback peer discovery
  - Future: Proof-of-work for DHT node registration
- **Status:** ⚠️ Partially Mitigated (Kademlia resistant, tracker fallback, but not immune)

---

### 6. Elevation of Privilege (Authorization Threats)

**Threat:** Attacker gains unauthorized access to privileged operations.

#### T6.1: Unauthorized DMCA Takedown

- **Description:** Attacker files fake DMCA notices to censor legitimate content
- **Attack Vector:** Spoofed DMCA request to tracker `/api/v1/tracker/dmca` endpoint
- **Impact:** High - censorship, legitimate assets removed
- **Likelihood:** High - DMCA endpoint is public
- **Mitigation:**
  - DMCA handler requires: CID, copyright_holder, contact_email (US-007-05)
  - Assets marked as `dmca_removed`, not deleted (reversible)
  - Future: Require DMCA agent signature verification (legal validation)
  - Audit log of all DMCA requests for review
- **Status:** ⚠️ Partially Mitigated (audit logs + reversible removal, but no signature verification)

#### T6.2: Tracker Admin Privilege Escalation

- **Description:** SQL injection or API bug grants attacker admin access
- **Attack Vector:** SQLi in search query, insecure deserialization
- **Impact:** Critical - full tracker compromise
- **Likelihood:** Low - requires implementation bug
- **Mitigation:**
  - Parameterized SQL queries (no string concatenation, US-007-01)
  - Input validation on all API endpoints
  - Postgres role-based access control (tracker service has minimal permissions)
  - Regular security audits (Week 10 external audit)
- **Status:** ✅ Mitigated (parameterized queries, input validation, RBAC)

#### T6.3: Daemon HTTP API Abuse

- **Description:** Local malware calls daemon API to share/download files without user consent
- **Attack Vector:** Localhost HTTP access (no authentication required in MVP)
- **Impact:** Medium - unauthorized file sharing, disk space abuse
- **Likelihood:** High - localhost APIs are unauthenticated by design
- **Mitigation:**
  - Accept as design decision (daemon is trusted localhost service, similar to Docker API)
  - Future: Optional JWT authentication for daemon API (US-001-06)
  - User education: daemon runs with user permissions
- **Status:** ⚠️ Known Limitation (localhost trust model, documented)

---

## Threat Summary

| Threat ID | Category        | Severity | Status       | Mitigation                                         |
| --------- | --------------- | -------- | ------------ | -------------------------------------------------- |
| T1.1      | Spoofing        | High     | ✅ Mitigated | libp2p PeerID verification                         |
| T1.2      | Spoofing        | Medium   | ⚠️ Partial   | CID verification (no proof-of-authorship)          |
| T1.3      | Spoofing        | Critical | ⚠️ Partial   | Requires HTTPS tracker deployment                  |
| T2.1      | Tampering       | Critical | ✅ Mitigated | Mandatory CID verification (US-003-05)             |
| T2.2      | Tampering       | Medium   | ⚠️ Partial   | CID prevents tampering, metadata trust via tracker |
| T2.3      | Tampering       | High     | ⚠️ Partial   | HTTPS required for tracker                         |
| T3.1      | Repudiation     | Critical | ✅ Mitigated | DMCA handler + audit logs (US-007-05)              |
| T3.2      | Repudiation     | Low      | 🔜 Planned   | EigenTrust reputation (US-011)                     |
| T4.1      | Info Disclosure | Medium   | ✅ Mitigated | Minimal logging + Postgres RBAC                    |
| T4.2      | Info Disclosure | Low      | ⚠️ Known     | DHT queries public (P2P trade-off)                 |
| T4.3      | Info Disclosure | High     | ⚠️ Partial   | Filesystem permissions (encryption future)         |
| T5.1      | DoS             | Critical | ✅ Mitigated | Rate limiting + DHT fallback (US-004-03)           |
| T5.2      | DoS             | High     | ✅ Mitigated | Resource limits (US-010-06/07)                     |
| T5.3      | DoS             | High     | ⚠️ Partial   | Kademlia resistance + tracker fallback             |
| T6.1      | Privilege       | High     | ⚠️ Partial   | Audit logs + reversible removal                    |
| T6.2      | Privilege       | Critical | ✅ Mitigated | Parameterized SQL + RBAC (US-007-01)               |
| T6.3      | Privilege       | Medium   | ⚠️ Known     | Localhost trust model                              |

---

## Residual Risks (Post-Mitigation)

### High Priority (Address Before v1.0)

1. **HTTPS Tracker Deployment** - Tracker must use TLS in production
2. **Manifest Signatures** - Ed25519 signatures on asset manifests (proof-of-authorship)
3. **Authenticated Daemon API** - Optional JWT authentication for localhost API

### Medium Priority (Post-v1.0)

4. **At-Rest Encryption** - Optional encryption of `~/.stonkagents/` with user passphrase
5. **DMCA Signature Verification** - Verify DMCA agent credentials
6. **Sybil Attack Hardening** - Proof-of-work for DHT registration

### Accepted Risks (Design Trade-offs)

7. **DHT Query Visibility** - DHT queries are public (inherent to P2P, similar to BitTorrent)
8. **Localhost API Trust** - Daemon API is unauthenticated on localhost (same as Docker, Postgres)

---

## Security Testing Recommendations

1. **Penetration Testing (Week 10)**
   - SQL injection attempts on tracker `/search` endpoint
   - Malformed CID uploads to trigger buffer overflows
   - DMCA endpoint abuse (flood testing, malformed requests)

2. **Fuzzing**
   - Protocol buffer message fuzzing (libp2p streams)
   - HTTP API fuzzing (tracker and daemon endpoints)
   - CID generation fuzzing (edge cases, invalid CIDs)

3. **Chaos Engineering**
   - Kill random peers during download (resume testing)
   - Inject malformed chunks (CID verification testing)
   - Disconnect tracker mid-announcement (DHT fallback testing)

---

## References

- STRIDE Threat Modeling: https://learn.microsoft.com/en-us/azure/security/develop/threat-modeling-tool-threats
- libp2p Security: https://docs.libp2p.io/concepts/security/
- IPFS CIDv1 Specification: https://github.com/multiformats/cid
- OWASP API Security Top 10: https://owasp.org/www-project-api-security/

---

**Version:** 1.0 (Mega Sprint 2)
**Author:** StonkAgents Security Team
**Last Updated:** 2026-02-01
**Next Review:** Week 10 (External Security Audit)
