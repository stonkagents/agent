# Testing Agent Chat & Token Chat

How to test the main Agent Chat and token-owner chat (holder ↔ owner’s LLM).

---

## Prerequisites

1. **Daemon running** on port **7841**
   - from the repo root: `.\bin\sync-daemon.exe` (Windows) or `./sync-daemon` (Mac/Linux)
   - Verify: `curl http://localhost:7841/health` → `"status":"healthy"`

2. **LLM configured** (otherwise you get 503 or no real replies)
   - Either set **gateway_url** in `~/.stonkagents/config.yaml` (or env `STONKAGENTS_GATEWAY_URL`)
   - Or set **ask_use_tracker: true** and have the daemon registered with the tracker (for credit-backed chat)

3. **portal** running (e.g. `npm run dev` in `frontend-apps/apps/portal`)
   - `.env`: `NEXT_PUBLIC_DAEMON_URL=http://localhost:7841` (portal may append `/api/v1` in code; if so use `http://localhost:7841` so base is correct)
   - `NEXT_PUBLIC_USE_REAL_DAEMON=true` and `NEXT_PUBLIC_USE_REAL_WALLET=true` if you use real wallet

4. **Wallet** (Phantom or mock) for user-scoped history: connect so `user_id` (wallet address) is sent.

---

## 1. Main Agent Chat (`/chat`)

**Flow:** Any user can chat with the main agent. History is per **user** (wallet). Sessions list and history require a connected wallet.

### Steps

1. Open the portal (e.g. `http://localhost:3000`).
2. **Connect your wallet** (needed for past conversations and session list).
3. Go to **Agent Chat** (nav link, often “Agent” or “Chat” → `/chat`).
4. **Send a message** (e.g. “Hello”). You should get an LLM reply.
5. **Send another message** in the same thread; reply should have context from the first.
6. **Past conversations (sidebar):**
   - Left sidebar shows “Past conversations”.
   - Without wallet: “Connect your wallet to see past conversations.”
   - With wallet: list of sessions (short id + date). Click one → that conversation loads.
7. **New session:** Click “New” → thread clears; next message starts a new session (new row in sidebar after reply).
8. **Refresh the page** (with wallet still connected) → last session should restore and messages reload.

### What to check

- Messages send and replies appear.
- Sidebar shows only **your** sessions (different wallet = different list).
- Clicking a session loads the correct thread.
- “New” clears and creates a new session; new session appears in sidebar after you send a message.

---

## 2. Token Chat (holder ↔ owner’s LLM)

**Flow:** Only **holders** (buyers) of a token can open this chat. They chat with the **owner’s** (creator’s) LLM. Owner cannot chat with their own token. History is per (holder, token).

### Steps

1. **As a holder:** Use a wallet that **holds** the token (bought it on the token’s page).
2. Open the **token detail** page for that token (Tokens → select the token).
3. You should see the **floating chat entry** (e.g. “Chat with this token’s owner”) at bottom-right. If you see “Buy tokens to unlock chat…”, that wallet is not a holder.
4. Click to **expand** the chat panel.
5. **Send a message.** Request goes to the daemon with `user_id` = your wallet and `context_id` = token contract address. You should get a reply (with token context in the prompt).
6. Send a few more messages; conversation is scoped to this user + this token.
7. **As a non-holder:** With a wallet that has **not** bought the token, the chat should not be available (gate message: buy tokens to unlock chat with this token’s owner).

### What to check

- Only holders see and can use the token chat.
- Replies are in context of the token (name/symbol in the prompt).
- Different tokens → different conversations (different `context_id`).
- Different holder wallets → different history for the same token.

---

## 3. API-level checks (optional)

Use these to confirm backend behavior (sessions and history are **user-scoped**).

### List sessions (requires `user_id`)

```powershell
# PowerShell – replace WALLET_ADDRESS with the chatter’s wallet (e.g. holder or main-chat user)
$userId = "WALLET_ADDRESS"
Invoke-RestMethod -Method Get -Uri "http://localhost:7841/api/v1/agent/chat/sessions?user_id=$userId"
```

```bash
# curl
curl -s "http://localhost:7841/api/v1/agent/chat/sessions?user_id=YOUR_WALLET_ADDRESS"
```

- Without `user_id`: 400 `MISSING_USER_ID`.
- With `user_id`: 200 and list of sessions for that user (and optional `context_id` for token-only list).

### Get history (requires `user_id`)

```powershell
$sessionId = "SESSION_ID_FROM_RESPONSE"
$userId = "WALLET_ADDRESS"
Invoke-RestMethod -Method Get -Uri "http://localhost:7841/api/v1/agent/chat/history/$sessionId`?user_id=$userId"
```

```bash
curl -s "http://localhost:7841/api/v1/agent/chat/history/SESSION_ID?user_id=YOUR_WALLET_ADDRESS"
```

- Wrong or missing `user_id` for that session: 403 FORBIDDEN.
- Correct `user_id`: 200 and session + messages.

### Send message (main chat with user scoping)

```powershell
$body = '{"messages":[{"role":"user","content":"Hi"}],"user_id":"YOUR_WALLET_ADDRESS"}'
Invoke-RestMethod -Method Post -Uri "http://localhost:7841/api/v1/agent/chat" -ContentType "application/json" -Body $body
```

### Send message (token chat)

```powershell
$body = @{
  user_id = "HOLDER_WALLET_ADDRESS"
  context_id = "TOKEN_CONTRACT_ADDRESS"
  messages = @(@{ role = "user"; content = "Hello from holder" })
} | ConvertTo-Json -Depth 5
Invoke-RestMethod -Method Post -Uri "http://localhost:7841/api/v1/agent/chat" -ContentType "application/json" -Body $body
```

---

## 4. Curl examples (copy-paste)

Set these once (replace with your values), then run the curls below.

```bash
DAEMON_URL="http://localhost:7841"
TRACKER_URL="http://localhost:7842"
USER_ID="YourWalletAddress"
# Get API_KEY from: curl -s $DAEMON_URL/api/v1/installer/peer-key  (localhost only)
API_KEY="your_daemon_api_key"
TOKEN_CONTRACT="TokenContractAddress"   # for token chat / owner lookup
```

**Daemon – health**
```bash
curl -s "$DAEMON_URL/health"
```

**Daemon – get API key** (localhost only; use this value for tracker calls)
```bash
curl -s "$DAEMON_URL/api/v1/installer/peer-key"
```

**Daemon – main agent chat**
```bash
curl -s -X POST "$DAEMON_URL/api/v1/agent/chat" \
  -H "Content-Type: application/json" \
  -d "{\"messages\":[{\"role\":\"user\",\"content\":\"Hi, say OK\"}],\"user_id\":\"$USER_ID\"}"
```

**Daemon – token chat** (holder; tries direct P2P then relay)
```bash
curl -s -X POST "$DAEMON_URL/api/v1/agent/chat" \
  -H "Content-Type: application/json" \
  -d "{\"messages\":[{\"role\":\"user\",\"content\":\"Hello\"}],\"user_id\":\"$USER_ID\",\"context_id\":\"$TOKEN_CONTRACT\"}"
```

**Daemon – list sessions**
```bash
curl -s "$DAEMON_URL/api/v1/agent/chat/sessions?user_id=$USER_ID"
```

**Daemon – get history** (replace SESSION_ID with one from list sessions)
```bash
curl -s "$DAEMON_URL/api/v1/agent/chat/history/SESSION_ID?user_id=$USER_ID"
```

**Tracker – resolve owner** (for direct P2P; needs X-API-Key)
```bash
curl -s -H "X-API-Key: $API_KEY" \
  "$TRACKER_URL/api/v1/agent/chat/owner?token_contract_address=$TOKEN_CONTRACT"
```

**Tracker – forward** (relay path; holder’s daemon sends message)
```bash
curl -s -X POST "$TRACKER_URL/api/v1/agent/chat/forward" \
  -H "X-API-Key: $API_KEY" \
  -H "Content-Type: application/json" \
  -d "{\"token_contract_address\":\"$TOKEN_CONTRACT\",\"user_id\":\"$USER_ID\",\"messages\":[{\"role\":\"user\",\"content\":\"Hi\"}],\"session_id\":\"\"}"
```
→ Returns `request_id`; use it below for result and response.

**Tracker – get result** (holder polls; 202 = pending, 200 = done)
```bash
curl -s -H "X-API-Key: $API_KEY" "$TRACKER_URL/api/v1/agent/chat/result/REQUEST_ID"
```

**Tracker – get pending** (owner’s daemon; use owner’s API key)
```bash
curl -s -H "X-API-Key: $API_KEY" "$TRACKER_URL/api/v1/agent/chat/pending"
```

**Tracker – post response** (owner’s daemon after calling local LLM)
```bash
curl -s -X POST "$TRACKER_URL/api/v1/agent/chat/response" \
  -H "X-API-Key: $API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"request_id":"REQUEST_ID","response":"Hello from owner!"}'
```

---

## 5. Troubleshooting

| Issue | What to check |
|-------|----------------|
| 503 ASK_NOT_CONFIGURED | Daemon has no `gateway_url` and no `ask_use_tracker` + tracker. Set one of them in config or env. |
| 402 INSUFFICIENT_CREDITS | Using tracker path; top up credits (e.g. via portal Settings). |
| Session list empty / history not loading | Wallet connected? Backend requires `user_id` for list and history. |
| Token chat not visible | Wallet is holder of that token? Only holders see the chat. |
| “Connect your wallet to see past conversations” | Connect wallet on the Agent Chat page so `user_id` is sent. |
| Daemon not responding | Is it running on 7841? `curl http://localhost:7841/health`. |

Session and message DB: `{DataDir}/agent_chat.db` (default `~/.stonkagents/agent_chat.db` or per `data_dir` in config).

---

## 6. Testing agent chat APIs directly

Use these to test the daemon and tracker agent-chat endpoints (main chat, token chat, direct P2P, relay).

### 6.1 Get daemon API key (for tracker calls)

The tracker’s agent-chat endpoints require `X-API-Key` (the daemon’s peer key). From the same machine as the daemon:

```powershell
# PowerShell (localhost only)
Invoke-RestMethod -Method Get -Uri "http://localhost:7841/api/v1/installer/peer-key"
```

```bash
# curl (localhost only)
curl -s http://localhost:7841/api/v1/installer/peer-key
```

Response: `{"api_key":"...", "tracker_url":"..."}`. Use `api_key` as `X-API-Key` when calling the tracker.

---

### 6.2 Daemon APIs (no key in request; daemon uses its own key for tracker)

**Base:** `http://localhost:7841` (or your daemon URL.)

| What | Method | URL | Body (JSON) |
|------|--------|-----|-------------|
| Main chat | POST | `/api/v1/agent/chat` | `{"messages":[{"role":"user","content":"Hi"}],"user_id":"YOUR_WALLET"}` |
| Token chat | POST | `/api/v1/agent/chat` | `{"messages":[{"role":"user","content":"Hello"}],"user_id":"HOLDER_WALLET","context_id":"TOKEN_CONTRACT_ADDRESS"}` |
| List sessions | GET | `/api/v1/agent/chat/sessions?user_id=WALLET` | — |
| History | GET | `/api/v1/agent/chat/history/SESSION_ID?user_id=WALLET` | — |

**Example: main chat**

```bash
curl -s -X POST http://localhost:7841/api/v1/agent/chat \
  -H "Content-Type: application/json" \
  -d '{"messages":[{"role":"user","content":"Say hello in one word."}],"user_id":"YourWallet123"}'
```

**Example: token chat** (holder; daemon will try direct P2P then relay)

```bash
curl -s -X POST http://localhost:7841/api/v1/agent/chat \
  -H "Content-Type: application/json" \
  -d '{"messages":[{"role":"user","content":"Hello"}],"user_id":"HOLDER_WALLET","context_id":"TOKEN_CONTRACT_ADDRESS"}'
```

Replace `HOLDER_WALLET` and `TOKEN_CONTRACT_ADDRESS` with a real holder wallet and a token the tracker knows (owner registered that token).

---

### 6.3 Tracker agent-chat APIs (need X-API-Key)

**Base:** tracker URL, e.g. `http://localhost:7842`. Full path prefix: `/api/v1/agent/chat/...`.

All require header: `X-API-Key: <daemon_peer_key>` (from 6.1 or from the daemon that will act as holder/owner).

| What | Method | URL | Body / notes |
|------|--------|-----|----------------|
| Resolve owner (for direct P2P) | GET | `/api/v1/agent/chat/owner?token_contract_address=ADDR` | Returns `owner_peer_id`, `multiaddrs`, `online`. |
| Forward (relay path) | POST | `/api/v1/agent/chat/forward` | `{"token_contract_address":"...","user_id":"...","messages":[...],"session_id":"..."}`. Returns `request_id`. |
| Pending (owner’s daemon) | GET | `/api/v1/agent/chat/pending` | Returns `pending` list for this peer. |
| Response (owner’s daemon) | POST | `/api/v1/agent/chat/response` | `{"request_id":"...","response":"..."}`. 204 on success. |
| Result (holder’s daemon) | GET | `/api/v1/agent/chat/result/REQUEST_ID` | 200 + `response` when done; 202 when pending. |

**Example: resolve owner** (use a token contract address that exists on the tracker)

```bash
TRACKER_URL="http://localhost:7842"
API_KEY="YOUR_DAEMON_API_KEY"   # from 5.1
TOKEN_ADDR="YourTokenContractAddress"

curl -s -H "X-API-Key: $API_KEY" \
  "$TRACKER_URL/api/v1/agent/chat/owner?token_contract_address=$TOKEN_ADDR"
```

**Example: forward (relay)** then poll result

```bash
# 1) Forward
curl -s -X POST "$TRACKER_URL/api/v1/agent/chat/forward" \
  -H "X-API-Key: $API_KEY" -H "Content-Type: application/json" \
  -d "{\"token_contract_address\":\"$TOKEN_ADDR\",\"user_id\":\"HOLDER\",\"messages\":[{\"role\":\"user\",\"content\":\"Hi\"}],\"session_id\":\"\"}"
# → save request_id from response

# 2) Poll result (holder’s daemon; use holder’s API key)
curl -s -H "X-API-Key: $HOLDER_API_KEY" \
  "$TRACKER_URL/api/v1/agent/chat/result/REQUEST_ID"
# 202 = pending, 200 = completed with response
```

**Example: pending + response** (owner’s daemon; use owner’s API key)

```bash
# 1) Get pending
curl -s -H "X-API-Key: $OWNER_API_KEY" "$TRACKER_URL/api/v1/agent/chat/pending"

# 2) Post response for a request_id
curl -s -X POST "$TRACKER_URL/api/v1/agent/chat/response" \
  -H "X-API-Key: $OWNER_API_KEY" -H "Content-Type: application/json" \
  -d '{"request_id":"REQUEST_ID","response":"Hello from owner!"}'
```

---

## 7. Quick test script (PowerShell)

Run from the repo root. Assumes daemon on 7841, tracker on 7842, and `gateway_url` set.

```powershell
# Get daemon API key (localhost only)
$keyResp = Invoke-RestMethod -Method Get -Uri "http://localhost:7841/api/v1/installer/peer-key"
$apiKey = $keyResp.api_key
$trackerUrl = $keyResp.tracker_url
if (-not $apiKey) { Write-Error "Daemon not registered (no api_key)"; exit 1 }

# Health
Invoke-RestMethod -Method Get -Uri "http://localhost:7841/health"

# Main agent chat
$body = '{"messages":[{"role":"user","content":"Reply with one word: OK"}],"user_id":"TestWallet"}'
Invoke-RestMethod -Method Post -Uri "http://localhost:7841/api/v1/agent/chat" -ContentType "application/json" -Body $body

# List sessions
Invoke-RestMethod -Method Get -Uri "http://localhost:7841/api/v1/agent/chat/sessions?user_id=TestWallet"

# Tracker: resolve owner (replace TOKEN_CONTRACT with a real token on your tracker)
$base = if ($trackerUrl) { $trackerUrl } else { "http://localhost:7842" }
Invoke-RestMethod -Method Get -Uri "$base/api/v1/agent/chat/owner?token_contract_address=TOKEN_CONTRACT" -Headers @{ "X-API-Key" = $apiKey }
```

For **token chat** (and relay), use a real `context_id` (token contract) and holder `user_id`; ensure the owner’s daemon is running and has registered that token so the tracker can resolve the owner.

**Run the script:** from the repo root:

```powershell
.\scripts\test-agent-chat.ps1
# With a token (for owner lookup):
.\scripts\test-agent-chat.ps1 -TokenContract "YourTokenContractAddress"
# Custom URLs:
.\scripts\test-agent-chat.ps1 -DaemonUrl "http://localhost:7841" -TrackerUrl "http://localhost:7842"
```
