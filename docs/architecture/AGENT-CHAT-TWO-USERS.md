# How Two Users (Each With Their Own Daemon) Communicate via AI

## Setup

- **User 1**: Their machine, their daemon on **localhost:7841**, their portal (e.g. localhost:3000).
- **User 2**: Different machine, their daemon on **localhost:7841** (on that machine), their portal.

Each user only ever talks to **their own** daemon (their localhost). The daemons do **not** call each other for agent chat.

---

## Current Architecture: Central LLM (What We Have Today)

```
User 1 (owner)                          User 2 (holder)
Machine A                               Machine B
┌─────────────┐                         ┌─────────────┐
│ Portal      │                         │ Portal      │
│ (browser)   │                         │ (browser)   │
└──────┬──────┘                         └──────┬──────┘
       │ localhost only                         │ localhost only
       ▼                                        ▼
┌─────────────┐                         ┌─────────────┐
│ Daemon A    │                         │ Daemon B    │
│ :7841       │                         │ :7841      │
└──────┬──────┘                         └──────┬──────┘
       │                                        │
       │  (agent chat / ask)                    │  (agent chat: holder
       │  POST /v1/chat/completions             │   chats “with owner”)
       ▼                                        ▼
       └──────────────┬─────────────────────────┘
                      │
                      ▼
              ┌───────────────┐
              │ Central LLM   │  ← Same place for everyone
              │ (Gateway or    │     • Tracker: POST /api/v1/agents/completions
              │  Tracker)      │     • Or Gateway: POST /v1/chat/completions
              └───────────────┘
```

- **User 1’s daemon** and **User 2’s daemon** never talk to each other.
- Both daemons talk to the **same central LLM** (either a **gateway** or the **tracker**).
- For token chat, when User 2 (holder) sends a message:
  - It goes: **User 2’s browser → User 2’s daemon (B) → central LLM**.
  - The request includes token context (e.g. `[Token: Name (Symbol)]`) so the **same** central LLM can answer “as” the owner. No request is sent to User 1’s daemon.

So “communication between the two users leveraging AI” today is:

- Both users’ daemons use the **same** AI service (gateway or tracker).
- The holder’s daemon sends the conversation (with token context) to that service; the service’s LLM plays the role of the owner. That’s how “User 2 chats with the owner” works, even though User 1’s daemon is not in the loop.

---

## What Each User Needs for It to Work

- **Same central LLM**  
  Both daemons must be configured to use the **same** LLM endpoint:
  - **Option A – Tracker:**  
    Both have `tracker_url` and `ask_use_tracker: true`, and are registered so they can call the tracker’s completions API (credits).
  - **Option B – Gateway:**  
    Both have the same `gateway_url` (e.g. a shared OpenClaw or OpenAI-compatible endpoint).

So “communicate between those [two users] leveraging AI” is achieved by **both** daemons talking to that **one** central service; the service does not need to know which machine is “User 1” or “User 2,” only the conversation and token context.

---

## Data and History

- **Session/history storage** is **local to each daemon** (`agent_chat.db` in that daemon’s data dir).
- User 2’s “chat with the owner” is stored only on **User 2’s daemon**.
- User 1’s daemon does not see or store that chat; it never receives those messages.

---

## If You Want True Daemon-to-Daemon (Owner’s Daemon Runs the LLM)

Some designs have the **owner’s daemon** run or proxy the LLM so the holder’s daemon talks **to the owner’s daemon**. That is **not** what we have today. To support that you’d need:

1. **Discovery**  
   Holder’s daemon must discover “which daemon/peer is the owner of this token?” (e.g. tracker maps token → owner `peer_id` and optionally a reachable address or relay id).

2. **Reachability**  
   Owner’s daemon is on the owner’s machine (often behind NAT). So either:
   - Owner’s daemon is reachable (e.g. public IP or tunnel), or
   - Requests are relayed (e.g. via tracker or a relay service) so Holder’s daemon can send a message “to” Owner’s daemon.

3. **Endpoint on owner’s daemon**  
   Owner’s daemon would expose something like: “accept chat message for token X from holder Y”, run (or proxy to) an LLM with owner-specific context, and return the reply so it can be sent back to the holder’s daemon (and then to the holder’s UI).

Today we do **not** have (1)–(3). We only have “each daemon talks to the central LLM”; that’s how the two users effectively communicate via AI with the current codebase.

---

## Summary

| Question | Answer today |
|----------|----------------|
| How do User 1 and User 2 “communicate” via AI? | Their daemons **don’t** talk to each other. Both daemons talk to the **same central LLM** (gateway or tracker). |
| Where does the “owner’s LLM” run? | It’s the **same** central LLM; token context in the prompt makes it answer as the owner. |
| What do I need so both users can chat? | Both daemons configured to use the **same** `gateway_url` or the same tracker with `ask_use_tracker: true`. |
| Is the owner’s daemon ever contacted for token chat? | **No.** Only the holder’s daemon and the central LLM are in the loop. |

If you want to move to a model where the owner’s daemon is the one that runs or proxies the LLM and the holder’s daemon talks to it, that would be a separate design (discovery, relay, and new endpoint on the owner’s daemon).
