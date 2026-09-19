# Run the tracker on Windows

On this repo, the **real** tracker entrypoint is under `tracker/`, not `cmd/tracker`.  
`cmd/tracker/main.go` is a stub that prints the correct build command.

**Full E2E (share + download, same or cross-network):** see [e2e-test-step-by-step.md](e2e-test-step-by-step.md) for a single guide that covers both ngrok tunnels (HTTP + TCP), where to put each endpoint, and step-by-step testing.

## Steps (Windows)

1. **From the repo root** (e.g. `C:\Work\agent`):

   **Option A – Build and run in one go (recommended)**

   ```powershell
   .\scripts\run-tracker-windows.ps1
   ```

   This builds `bin\cs-tracker.exe` from `tracker/cmd/tracker/main.go` and runs it.

   **Option B – Build once, then run**

   ```powershell
   go build -o bin/cs-tracker.exe tracker/cmd/tracker/main.go
   .\bin\cs-tracker.exe
   ```

2. **Default behaviour**
   - Listens on **`:7842`** (HTTP).
   - Override with: `$env:TRACKER_ADDR = ":7840"; .\bin\cs-tracker.exe`

3. **Expose with ngrok**
   - In another terminal: `ngrok http 7842` (or the port you set in step 2).
   - Use the ngrok HTTPS URL as the tracker URL for daemons (e.g. in `config.yaml` or `TRACKER_URL`).

4. **Cross-network (mobile data / different WiFi)**  
   For a leecher on another network (e.g. mobile hotspot) to download, the **relay** must be reachable from the internet. The tracker only advertises local relay addresses (`127.0.0.1`, LAN IP) unless you set a public relay address:
   - **Expose the relay with ngrok TCP** (in a separate terminal):

     ```powershell
     ngrok tcp 9841
     ```

     Note the forwarded address, e.g. `tcp://0.tcp.in.ngrok.io:17826`.

   - **Start the tracker with the public relay address** (multiaddr format: host and port from ngrok TCP):

     ```powershell
     $env:RELAY_PUBLIC_ADDR = "/dns4/0.tcp.in.ngrok.io/tcp/17826"
     .\scripts\run-tracker-windows.ps1
     ```

     Replace `0.tcp.in.ngrok.io` and `17826` with the host and port from your ngrok TCP tunnel. After this, the tracker will advertise this public relay address to daemons so they can connect across NAT.

   - If you don’t set `RELAY_PUBLIC_ADDR`, only local relay addresses are advertised and cross-network downloads will not work.

Mac setup is unchanged; use your existing way to run the tracker there.
