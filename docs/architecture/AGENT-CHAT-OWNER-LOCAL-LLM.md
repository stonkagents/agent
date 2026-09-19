# Token Chat: Owner Uses Their Own LLM (via Tracker Relay)

## Goal

- **Tracker**: Same for everyone (discovery, relay).
- **LLM**: Each user uses **their own** LLM, via **their daemon** (local gateway_url).
- **Main agent chat**: Daemon uses its **local** gateway only (no tracker completions).
- **Token chat (holder → owner)**: Holder's daemon sends message to **owner's daemon** via tracker relay; **owner's daemon** calls **owner's local** LLM and responds.

## Flow

```
Holder (User 2)                              Tracker                    Owner (User 1)
Machine B                                                               Machine A
┌─────────────┐                            ┌─────────────┐              ┌─────────────┐
│ Portal      │                            │             │              │ Daemon A    │
└──────┬──────┘                            │  POST       │              │ (polls      │
       │                                   │  /forward   │              │  pending)   │
       ▼                                   │  lookup     │              └──────┬──────┘
┌─────────────┐   POST /forward            │  token→     │                     │
│ Daemon B    │ ──────────────────────────►│  owner_peer │                     │ GET /pending
│             │   (token, messages,        │  store      │◄────────────────────┤
│             │    user_id, session_id)    │  pending    │   (owner's API key) │
│             │                            │             │                     │
│             │   GET /result/{id}         │             │   POST /response    │
│             │ ◄───────────────────────── │  store      │ ◄───────────────────┤
│             │   (poll until 200)         │  response  │   (request_id,      │
└─────────────┘                            └─────────────┘    response)       │
                                                                               │
                                                                               ▼
                                                                        ┌─────────────┐
                                                                        │ Owner's     │
                                                                        │ gateway_url │
                                                                        │ (local LLM) │
                                                                        └─────────────┘
```

1. **Holder's daemon** (when context_id = token): POST to tracker `/api/v1/agent/chat/forward` with token_contract_address, user_id, messages, session_id. Tracker resolves token → owner peer_id, stores pending request, returns request_id.
2. **Holder's daemon** polls GET `/api/v1/agent/chat/result/{request_id}` until 200 (response) or timeout.
3. **Owner's daemon** (periodically or on demand): GET `/api/v1/agent/chat/pending` (with owner's API key). Tracker returns pending requests for this peer. For each, owner's daemon calls **local** gateway_url with messages (with token context), gets LLM response, POSTs to tracker `/api/v1/agent/chat/response` with request_id and response.
4. **Tracker** stores response; next holder poll returns 200 with response.

## Tracker API (new)

| Method | Path | Auth | Purpose |
|--------|------|------|---------|
| POST | /api/v1/agent/chat/forward | X-API-Key (holder's daemon) | Submit chat request for token owner. Body: token_contract_address, user_id, messages, session_id. Returns request_id. |
| GET | /api/v1/agent/chat/pending | X-API-Key (owner's daemon) | List pending chat requests for this peer (owner). |
| POST | /api/v1/agent/chat/response | X-API-Key (owner's daemon) | Submit LLM response for a request_id. Body: request_id, response. |
| GET | /api/v1/agent/chat/result/:request_id | X-API-Key | Get result: 200 { response } if done, 202 { status: "pending" } if not. |

## Daemon behavior

- **Main agent chat** (context_id empty): Use **gateway_url** only (local LLM). Do not use tracker completions for agent chat.
- **Token chat** (context_id = token): Do not call gateway or tracker completions. Call tracker forward, then poll result. Session history still stored on **holder's** daemon (agent_chat.db).
- **Owner's daemon**: Must poll (or be triggered) to fetch pending and post responses; uses its **gateway_url** for the LLM call.

## Token lookup

Tracker has TokenRepository.GetByContractAddress(token_contract_address) → PeerToken with PeerID (owner). Used in forward to set owner_peer_id for pending request.
