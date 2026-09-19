---
title: 'Tracker Database Schema'
date: 2026-02-01
status: active
tags: [tracker, database, schema, postgresql]
---

# Tracker Database Schema

PostgreSQL 16+ schema for the StonkAgents centralized tracker (F-007).

## Tables

### peers

Stores registered agent peers.

| Column               | Type         | Constraints            | Description                                  |
| -------------------- | ------------ | ---------------------- | -------------------------------------------- |
| peer_id              | VARCHAR(255) | PRIMARY KEY            | Unique peer identifier (libp2p PeerID)       |
| ed25519_pubkey       | TEXT         | NOT NULL               | Ed25519 public key for identity verification |
| multiaddrs           | TEXT[]       | NOT NULL DEFAULT '{}'  | libp2p multiaddresses for connectivity       |
| first_seen           | TIMESTAMPTZ  | NOT NULL DEFAULT NOW() | First registration timestamp                 |
| last_seen            | TIMESTAMPTZ  | NOT NULL DEFAULT NOW() | Last heartbeat timestamp                     |
| total_uptime_seconds | BIGINT       | NOT NULL DEFAULT 0     | Cumulative uptime for reputation             |

**Indexes:** `idx_peers_last_seen` on `last_seen`

### assets

Stores announced assets (`.at-raw`, `.at-vec` manifests).

| Column        | Type         | Constraints            | Description                                |
| ------------- | ------------ | ---------------------- | ------------------------------------------ |
| cid           | VARCHAR(255) | PRIMARY KEY            | CIDv1 content address                      |
| filename      | VARCHAR(500) | NOT NULL               | Original filename                          |
| mime_type     | VARCHAR(255) |                        | MIME type (e.g., application/octet-stream) |
| size          | BIGINT       | NOT NULL               | File size in bytes                         |
| peer_id       | VARCHAR(255) | NOT NULL, FK → peers   | Announcing peer                            |
| announced_at  | TIMESTAMPTZ  | NOT NULL DEFAULT NOW() | Announcement timestamp                     |
| manifest_type | VARCHAR(50)  | NOT NULL               | Asset type: "raw" or "vec"                 |
| manifest_data | JSONB        |                        | Full manifest payload                      |
| quarantined   | BOOLEAN      | NOT NULL DEFAULT FALSE | DMCA quarantine flag                       |

**Indexes:** `idx_assets_peer_id`, `idx_assets_manifest_type`, `idx_assets_filename_gin` (GIN on tsvector), `idx_assets_quarantined` (partial WHERE quarantined = FALSE)

### reputation_scores

Stores EigenTrust-derived reputation scores per peer.

| Column            | Type             | Constraints             | Description                                   |
| ----------------- | ---------------- | ----------------------- | --------------------------------------------- |
| peer_id           | VARCHAR(255)     | PRIMARY KEY, FK → peers | Peer being scored                             |
| bandwidth_score   | DOUBLE PRECISION | NOT NULL DEFAULT 0      | Upload/download ratio [0,1]. Weight: 40%      |
| quality_score     | DOUBLE PRECISION | NOT NULL DEFAULT 0      | Verification success rate [0,1]. Weight: 30%  |
| security_score    | DOUBLE PRECISION | NOT NULL DEFAULT 1      | Binary DMCA flag (0 or 1). Weight: 20%        |
| citizenship_score | DOUBLE PRECISION | NOT NULL DEFAULT 1      | DMCA reports filed penalty [0,1]. Weight: 10% |
| composite_score   | DOUBLE PRECISION | NOT NULL DEFAULT 0.5    | Weighted sum of 4 factors [0,1]               |
| updated_at        | TIMESTAMPTZ      | NOT NULL DEFAULT NOW()  | Last recalculation timestamp                  |

**Indexes:** `idx_reputation_composite` on `composite_score DESC`

### dmca_notices

Stores DMCA takedown requests for safe harbor compliance (17 U.S.C. 512).

| Column         | Type         | Constraints                           | Description                   |
| -------------- | ------------ | ------------------------------------- | ----------------------------- |
| id             | UUID         | PRIMARY KEY DEFAULT gen_random_uuid() | Unique notice ID              |
| cid            | VARCHAR(255) | NOT NULL, FK → assets                 | Targeted asset CID            |
| reporter_email | VARCHAR(255) | NOT NULL                              | Reporter contact (not public) |
| complaint_text | TEXT         | NOT NULL                              | DMCA complaint body           |
| quarantined_at | TIMESTAMPTZ  | NOT NULL DEFAULT NOW()                | When asset was quarantined    |
| status         | VARCHAR(50)  | NOT NULL DEFAULT 'pending'            | pending, confirmed, rejected  |

**Indexes:** `idx_dmca_notices_cid`, `idx_dmca_notices_status`

## Migrations

Located at `tracker/internal/db/migrations/`:

| Migration             | Direction | Description                                        |
| --------------------- | --------- | -------------------------------------------------- |
| 001_create_peers      | up/down   | Creates `peers` table                              |
| 002_create_assets     | up/down   | Creates `assets` table with FK to peers            |
| 003_create_dmca       | up/down   | Creates `dmca_notices` table with FK to assets     |
| 004_create_reputation | up/down   | Creates `reputation_scores` table with FK to peers |

## Entity Relationships

```
peers (1) ──→ (N) assets
peers (1) ──→ (1) reputation_scores
assets (1) ──→ (N) dmca_notices
```

## Notes

- MVP uses in-memory implementations (no PostgreSQL required for tests)
- PostgreSQL backend swap is zero-change to services/handlers via repository interfaces
- Redis presence tracking (online/offline) is separate from PostgreSQL persistence
