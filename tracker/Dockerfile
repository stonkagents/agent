# Multi-stage build for StonkAgents tracker. ECS-ready.
# Build from repo root: docker build -f tracker/Dockerfile -t cs-tracker .

# ---- Build ----
FROM golang:1.26.4-alpine AS builder
RUN apk add --no-cache ca-certificates
WORKDIR /app

# Dependencies first for better layer caching
COPY go.mod go.sum ./
# proxy.golang.org drops HTTP/2 streams now and then from CI; the pipe makes Go fall
# back to the next proxy on any error, and the loop retries the whole download.
ENV GOPROXY="https://proxy.golang.org|https://goproxy.io|direct"
RUN for i in 1 2 3; do go mod download && break; echo "go mod download failed (attempt $i), retrying"; sleep 10; done && go mod verify

# Build tracker (static binary, no cgo for tracker deps)
# Build the package directory (not single file) to include bootstrap.go, envconfig.go, etc.
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /tracker ./tracker/cmd/tracker

# ---- Runtime ----
FROM alpine:3.19
RUN apk add --no-cache ca-certificates tzdata wget
# Non-root user for ECS security best practices
RUN adduser -D -g "" appuser
USER appuser
WORKDIR /app

COPY --from=builder /tracker /tracker

EXPOSE 7842
# Health check for ECS / load balancer
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=2 \
  CMD wget -q -O- http://localhost:7842/health || exit 1

ENTRYPOINT ["/tracker"]
