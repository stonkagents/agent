CREATE TABLE asset_replications (
    cid           VARCHAR(255) PRIMARY KEY REFERENCES assets(cid) ON DELETE CASCADE,
    s3_key        TEXT NOT NULL UNIQUE,
    status        TEXT NOT NULL CHECK (status IN ('pending', 'running', 'completed', 'failed', 'quarantined')),
    size_bytes    BIGINT NOT NULL DEFAULT 0 CHECK (size_bytes >= 0),
    etag          TEXT,
    replicated_at TIMESTAMPTZ,
    last_error    TEXT,
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_asset_replications_status ON asset_replications(status);
CREATE INDEX idx_asset_replications_updated_at ON asset_replications(updated_at DESC);

CREATE TABLE replication_jobs (
    id              TEXT PRIMARY KEY,
    cid             VARCHAR(255) NOT NULL REFERENCES assets(cid) ON DELETE CASCADE,
    assigned_worker TEXT NOT NULL,
    attempt         INT NOT NULL DEFAULT 1 CHECK (attempt > 0),
    status          TEXT NOT NULL CHECK (status IN ('pending', 'running', 'completed', 'failed')),
    started_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    finished_at     TIMESTAMPTZ
);

CREATE INDEX idx_replication_jobs_cid ON replication_jobs(cid);
CREATE INDEX idx_replication_jobs_status ON replication_jobs(status);
