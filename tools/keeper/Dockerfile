# StonkAgents revenue keeper.
# One image, three jobs. The command decides which job runs, so the same image backs three
# scheduled tasks. Runs as a non-root user and ships no keys: mount them at runtime.

FROM node:22-alpine AS deps
WORKDIR /app
COPY package.json package-lock.json* ./
RUN npm ci --omit=dev --no-audit --no-fund || npm install --omit=dev --no-audit --no-fund

FROM node:22-alpine AS runtime
WORKDIR /app
ENV NODE_ENV=production

# tsx is a dev dependency of the source tree but the runtime entry point, so install it explicitly.
COPY --from=deps /app/node_modules ./node_modules
RUN npm install --no-save --no-audit --no-fund tsx@^4.19.0

COPY package.json tsconfig.json ./
COPY src ./src

# state/ holds the per-mint idempotency ledgers, the buyback watermark and the failed-ledger queue.
# Mount a persistent volume (EFS on ECS) here or a rerun cannot prove it already paid.
ENV STATE_DIR=/data/state
RUN mkdir -p /data/state && addgroup -S keeper && adduser -S keeper -G keeper && chown -R keeper:keeper /data
USER keeper

# Dry run by default: a container started with no arguments never sends a transaction.
ENTRYPOINT ["npx", "tsx"]
CMD ["src/jobs/all.ts"]
