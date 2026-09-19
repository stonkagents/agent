#!/usr/bin/env bash
set -euo pipefail

REPO_URL="${REPO_URL:-https://github.com/stonkagents/agent.git}"
INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/bin}"
DATA_DIR="${DATA_DIR:-$HOME/.stonkagents/data}"
CONFIG_DIR="${CONFIG_DIR:-$HOME/.stonkagents}"
SECRETS_PATH="${SECRETS_PATH:-$CONFIG_DIR/secrets.env}"

ensure_cmd() {
  command -v "$1" >/dev/null 2>&1 || { echo "Missing dependency: $1"; exit 1; }
}

ensure_cmd git
ensure_cmd go

WORK_DIR="$(pwd)"
if [ ! -f "$WORK_DIR/go.mod" ]; then
  WORK_DIR="$(mktemp -d)"
  git clone "$REPO_URL" "$WORK_DIR"
fi

mkdir -p "$INSTALL_DIR"
mkdir -p "$CONFIG_DIR"
mkdir -p "$DATA_DIR"

pushd "$WORK_DIR" >/dev/null
go build -o "$INSTALL_DIR/sync-daemon" ./cmd/daemon/main.go
popd >/dev/null

gen_out="$(cat <<'EOF' | go run /dev/stdin
package main

import (
  "encoding/base64"
  "fmt"

  crypto "github.com/stonkagents/agent/pkg/cryptography"
)

func main() {
  pub, priv, err := crypto.GenerateKeypair()
  if err != nil { panic(err) }
  fmt.Printf("%s\n%s\n",
    base64.StdEncoding.EncodeToString(pub),
    base64.StdEncoding.EncodeToString(priv),
  )
}
EOF
)"

PUB_KEY="$(echo "$gen_out" | sed -n '1p')"
PRIV_KEY="$(echo "$gen_out" | sed -n '2p')"

if [ ! -f "$CONFIG_DIR/config.yaml" ]; then
  cat > "$CONFIG_DIR/config.yaml" <<EOF
peer_id: ""
public_key: "$PUB_KEY"
daemon_host: "127.0.0.1"
daemon_port: 7841
bootstrap_peers: []
tracker_url: "https://tracker.stonkagents.com"
data_dir: "$DATA_DIR"
EOF
fi

cat > "$SECRETS_PATH" <<EOF
STONKAGENTS_PRIVATE_KEY=$PRIV_KEY
EOF

if ! echo "$PATH" | grep -q "$INSTALL_DIR"; then
  if [ -f "$HOME/.bashrc" ]; then
    echo "export PATH=\"$INSTALL_DIR:\$PATH\"" >> "$HOME/.bashrc"
  fi
  if [ -f "$HOME/.zshrc" ]; then
    echo "export PATH=\"$INSTALL_DIR:\$PATH\"" >> "$HOME/.zshrc"
  fi
fi

echo "Installed: $INSTALL_DIR/sync-daemon"
echo "Config: $CONFIG_DIR/config.yaml"
echo "Secrets: $SECRETS_PATH"
echo "Start: sync-daemon"
