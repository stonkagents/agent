# End-to-end test: step-by-step

This guide runs the tracker behind ngrok and tests share + download on two machines (same network or cross-network, e.g. one on WiFi and one on mobile data).

---

## Do we need two ngrok tunnels?

**Yes.** You need **two** ngrok tunnels:

| Tunnel   | ngrok command     | Port | Purpose                                                                                                                    |
| -------- | ----------------- | ---- | -------------------------------------------------------------------------------------------------------------------------- |
| **HTTP** | `ngrok http 7842` | 7842 | Tracker **API** (register, heartbeat, peer list, relay info). Daemons talk to the tracker over HTTP/HTTPS.                 |
| **TCP**  | `ngrok tcp 9841`  | 9841 | Tracker **relay** (libp2p circuit relay). Peers use this to connect through NAT when they can’t reach each other directly. |

---

## Where do I put the HTTP and TCP endpoints?

| Endpoint             | Where it goes                                                                          | Who uses it                                                                                                               |
| -------------------- | -------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------- |
| **HTTP (HTTPS URL)** | **Daemon `config.yaml`** as `tracker_url`                                              | **Both** computers (seeder and leecher). Example: `https://2156dbb003fb.ngrok-free.app`                                   |
| **TCP (host:port)**  | **Tracker only**: env var `RELAY_PUBLIC_ADDR` on the **machine that runs the tracker** | Daemons **never** set this. They get the relay address from the tracker API. Example: `/dns4/0.tcp.in.ngrok.io/tcp/17826` |

So: put the **HTTP** URL in each daemon’s config; put the **TCP** address only in the tracker’s environment when you start the tracker.

---

## Step-by-step process

### 1. Start ngrok (two tunnels)

On the **machine that will run the tracker** (e.g. Computer 1), start ngrok with **both** tunnels.

**Option A – Two terminals**

- Terminal 1:
  ```powershell
  ngrok http 7842
  ```
- Terminal 2:
  ```powershell
  ngrok tcp 9841
  ```

**Option B – One config, one command (recommended)**

Create or edit ngrok config (e.g. `%USERPROFILE%\AppData\Local\ngrok\ngrok.yml` on Windows or `~/Library/Application Support/ngrok/ngrok.yml` on Mac):

```yaml
tunnels:
  tracker:
    proto: http
    addr: 7842
  relay:
    proto: tcp
    addr: 9841
```

Then in one terminal:

```powershell
ngrok start --all
```

**Note the two addresses from ngrok:**

- **HTTP** tunnel: the **HTTPS** URL, e.g. `https://2156dbb003fb.ngrok-free.app` (no port in URL).  
  ngrok shows: `Forwarding  https://2156dbb003fb.ngrok-free.app -> http://localhost:7842`
- **TCP** tunnel: the **host** and **port**, e.g. `0.tcp.in.ngrok.io` and `17826`.  
  ngrok shows: `Forwarding  tcp://0.tcp.in.ngrok.io:17826 -> localhost:9841`

---

### 2. Start the tracker (with public relay address)

On the **same machine**, from the repo root. Use the **TCP** tunnel host and port from step 1 (e.g. from `tcp://0.tcp.in.ngrok.io:17826` → host `0.tcp.in.ngrok.io`, port `17826`):

```powershell
# Use the TCP tunnel host and port from ngrok (e.g. tcp://0.tcp.in.ngrok.io:17826 -> localhost:9841)
$env:RELAY_PUBLIC_ADDR = "/dns4/0.tcp.in.ngrok.io/tcp/17826"
.\scripts\run-tracker-windows.ps1
```

You should see something like:

- `Relay host started: PeerID=...`
- `Relay public addr override: /dns4/0.tcp.in.ngrok.io/tcp/17826/p2p/...`

The tracker is now reachable at your **ngrok HTTPS URL** and advertises the **ngrok TCP** address as the relay. (If you restart ngrok, the TCP host/port may change — update `RELAY_PUBLIC_ADDR` and restart the tracker.)

---

### 3. Prepare daemon config on both computers

Both **Computer 1 (seeder)** and **Computer 2 (leecher)** must use the **same** tracker URL: your **ngrok HTTP (HTTPS)** URL.

**If you use tester packages:**

```powershell
# From repo root; use your ngrok HTTPS URL from step 1 (no trailing slash)
.\scripts\gen-tester-package.ps1 -TesterName "computer1" -Platform "windows-amd64" -TrackerURL "https://2156dbb003fb.ngrok-free.app"
.\scripts\gen-tester-package.ps1 -TesterName "computer2" -Platform "windows-amd64" -TrackerURL "https://2156dbb003fb.ngrok-free.app"
```

This creates e.g. `packages\sync-daemon-computer1-windows-amd64\` and `packages\sync-daemon-computer2-windows-amd64\` with `config.yaml` already containing `tracker_url: "https://2156dbb003fb.ngrok-free.app"`.

**If you edit config manually:** set `tracker_url` in each daemon’s `config.yaml` to that same HTTPS URL. You do **not** put the TCP address in the daemon config.

---

### 4. Computer 1 (seeder): start daemon and share

- From the package directory or with config path set:

  ```powershell
  cd packages\sync-daemon-computer1-windows-amd64
  $env:STONKAGENTS_CONFIG_PATH = (Join-Path $PWD "config.yaml")
  # Load private key (see secrets.env or run-daemon.ps1 in the package)
  .\bin\sync-daemon.exe
  ```

- Share a file (CLI or API), e.g.:

  ```powershell
  .\cs-share.ps1 "C:\path\to\file.pdf"
  ```

- Note the **CID** printed (e.g. `bafkreie...`). The seeder stays running.

---

### 5. Computer 2 (leecher): start daemon and download

- Start the daemon using the **same** tracker URL (same `config.yaml` or package as in step 3):

  ```powershell
  cd packages\sync-daemon-computer2-windows-amd64
  $env:STONKAGENTS_CONFIG_PATH = (Join-Path $PWD "config.yaml")
  .\bin\sync-daemon.exe
  ```

- Queue download with the CID from step 4:

  ```powershell
  .\cs-download.ps1 bafkreie...
  ```

- Check status (optional):

  ```powershell
  .\cs-status.ps1
  # or: GET /api/v1/downloads/status?cid=<CID>
  ```

- When the download completes, files appear under `data\downloads\` (or the path in your config).

---

### 6. Cross-network check (optional)

To confirm cross-network (e.g. mobile data) works:

- Leave Computer 1 on **WiFi** (tracker + seeder).
- Switch Computer 2 to **mobile hotspot** (or another network).
- Ensure Computer 2’s `config.yaml` still has the **same** `tracker_url` (ngrok HTTPS).
- Restart the daemon on Computer 2 if needed, then download again.

If you did **not** set `RELAY_PUBLIC_ADDR` when starting the tracker (step 2), cross-network will fail because the relay is only advertised on local addresses. With `RELAY_PUBLIC_ADDR` set to the ngrok TCP multiaddr, it should work.

---

## Quick reference (example from a real ngrok run)

| What                      | Value / place                                                                                                                                      |
| ------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------- |
| ngrok HTTP                | `ngrok http 7842` → **HTTPS URL** e.g. `https://2156dbb003fb.ngrok-free.app` (from `Forwarding  https://... -> http://localhost:7842`)             |
| ngrok TCP                 | `ngrok tcp 9841` → **host** and **port** e.g. `0.tcp.in.ngrok.io` and `17826` (from `Forwarding  tcp://0.tcp.in.ngrok.io:17826 -> localhost:9841`) |
| Tracker URL (for daemons) | `tracker_url` in **config.yaml** on **both** computers = ngrok **HTTPS** URL                                                                       |
| Relay public address      | **Only** on tracker machine: `RELAY_PUBLIC_ADDR="/dns4/0.tcp.in.ngrok.io/tcp/17826"` when starting the tracker (use your TCP host and port)        |
| Daemon config path        | `STONKAGENTS_CONFIG_PATH` or run from package dir that contains `config.yaml`                                                                         |

**Note:** The daemon extends the HTTP write deadline for `/share` and `/download` so that tracker calls over slow or cross-network links (e.g. mobile data) can complete; you may see "connection closed unexpectedly" only if the tracker is unreachable or takes longer than ~45s.
