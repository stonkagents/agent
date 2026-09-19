# Tracker Docker image

Build and run the tracker in a container (from repo root). The tracker requires PostgreSQL; migrations run on startup.

## Run tracker locally (easiest: Postgres + Tracker)

From the **repo root**:

```bash
docker compose -f docker-compose.tracker.yml up -d
```

This starts PostgreSQL (port 5432) and the tracker (port 7842). The tracker runs migrations automatically.

- **Health:** http://localhost:7842/health  
- **Stop:** `docker compose -f docker-compose.tracker.yml down`

Your daemon’s `config.yaml` already has `tracker_url: "http://localhost:7842"`; the daemon will register with this tracker.

---

## Build and run tracker image only

If you already have PostgreSQL (e.g. installed or another container):

**Build:**

```bash
docker build -f tracker/Dockerfile -t cs-tracker .
```

**Run:**

```bash
docker run --rm -e DATABASE_URL="postgres://user:pass@host:5432/dbname" -p 7842:7842 cs-tracker
```

- From the **host**, use `host.docker.internal` as the DB host (e.g. `postgres://user:pass@host.docker.internal:5432/dbname`).
- From another container on the same Docker network, use the Postgres service name as host.

Then open http://localhost:7842/health.

The image exposes port **7842** and includes a HEALTHCHECK on `/health`. Set `DATABASE_URL` (required); optionally `REDIS_URL`, `TRACKER_ADDR`, `RELAY_PUBLIC_ADDR`.
