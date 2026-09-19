# Side-by-side environments on Windows (dev, staging, production)

From 2.4.0 the dev, staging and production installers can be installed on the same
Windows machine at the same time. Each environment has its own product, folders,
services, ports, firewall rule, CLI profile and Windows Installer identity, so one
never upgrades, replaces or talks to another. Production keeps every name, folder,
port and UpgradeCode it had before 2.4.0: an existing production install upgrades in
place exactly as it always did.

The environment is fixed at build time: `scripts\build-msi.ps1 -Environment dev|stg|prd`,
derived from the tracker URL when omitted (`tracker.dev.*` is dev, `tracker.stg.*` is
stg, anything else is prd). The Go side of the table is `internal/installenv`, the WiX
side `installer/wix/Environment.wxi`; `internal/installenv/installenv_test.go` pins the
production values and checks that no identity is shared between environments.

## The matrix

| | Production | Staging | Dev |
|---|---|---|---|
| Build | `-Environment prd` (default) | `-Environment stg` | `-Environment dev` |
| Tracker | tracker.stonkagents.com | tracker.stg.stonkagents.com | tracker.dev.stonkagents.com |
| Portal | stonkagents.com | stg.stonkagents.com | dev.stonkagents.com |
| Update manifest | releases.stonkagents.com | releases.stg.stonkagents.com | releases.dev.stonkagents.com |
| Product name (ARP, Start menu) | StonkAgents | StonkAgents Staging | StonkAgents Dev |
| Install folder | `C:\Program Files\StonkAgents` | `C:\Program Files\StonkAgents Staging` | `C:\Program Files\StonkAgents Dev` |
| Data folder | `%USERPROFILE%\.stonkagents` | `%USERPROFILE%\.stonkagents-stg` | `%USERPROFILE%\.stonkagents-dev` |
| Daemon service | `StonkAgentsDaemon` (StonkAgents Daemon) | `stonkagents-daemon-stg` (StonkAgents Staging Daemon) | `stonkagents-daemon-dev` (StonkAgents Dev Daemon) |
| Controller service | `StonkAgentsController` (StonkAgents Controller) | `stonkagents-controller-stg` (StonkAgents Staging Controller) | `stonkagents-controller-dev` (StonkAgents Dev Controller) |
| Daemon port | 7841 | 7851 | 7861 |
| Controller port | 7840 | 7850 | 7860 |
| CLI gateway port | 18789 | 19002 | 19001 |
| CLI profile (`OPENCLAW_PROFILE`) | default | `stg` | `dev` |
| CLI state folder | `%USERPROFILE%\.openclaw` | `%USERPROFILE%\.openclaw-stg` | `%USERPROFILE%\.openclaw-dev` |
| Gateway scheduled task | OpenClaw Gateway | OpenClaw Gateway (stg) | OpenClaw Gateway (dev) |
| Firewall rule | StonkAgents Agent | StonkAgents Staging Agent | StonkAgents Dev Agent |
| CLI wrapper on PATH | `stonkagents.cmd` | `stonkagents-stg.cmd` | `stonkagents-dev.cmd` |
| Registry key | `SOFTWARE\StonkAgents` | `SOFTWARE\StonkAgents Staging` | `SOFTWARE\StonkAgents Dev` |
| MSI UpgradeCode | B7E8E8A1-9C2D-4E5F-8A3B-1D2C3E4F5A6B | 5D2A7C31-8E4B-4F6A-9C1D-3B5E7F9A2C41 | 7F4C9E53-A06D-4B8C-9E3F-5D7A9B2C4E63 |
| Bundle UpgradeCode | C8D9E0F1-2A3B-4C5D-6E7F-8A9B0C1D2E3F | 6E3B8D42-9F5C-4A7B-8D2E-4C6F8A1B3D52 | 8A5DAF64-B17E-4C9D-AF40-6E8BAC3D5F74 |
| Setup logs in `%TEMP%` | `stonkagents-tools.log`, `gateway-setup.log` | `stonkagents-tools-stg.log`, `gateway-setup-stg.log` | `stonkagents-tools-dev.log`, `gateway-setup-dev.log` |
| Command tools task | StonkAgents command tools | StonkAgents Staging command tools | StonkAgents Dev command tools |

The data folder (identity in `secrets.env`, config, the SQLite databases, shared files and
downloads) is placed by the
MSI under the Documents folder (WiX `PersonalFolder`, so
`%USERPROFILE%Documents.stonkagents` on a stock profile); the script installs use
`%USERPROFILE%.stonkagents`. Paths that point
outside a folder with one of these names (a custom `data_dir`, a custom
`STONKAGENTS_SECRETS_PATH`) are used as they are.


The P2P listener picks a random port, so it never collides. The npm package
`stonkagents` is shared by design: one global install serves all three environments,
and the CLI profile keeps each environment's config, state and gateway task apart.

Portal configuration per environment: `NEXT_PUBLIC_DAEMON_URL` and
`NEXT_PUBLIC_CONTROLLER_URL` must point at that environment's daemon and controller
ports (`http://127.0.0.1:7861` and `http://127.0.0.1:7860` for dev, `7851`/`7850` for
staging, `7841`/`7840` for production). The daemon and the controller already allow
all three portal origins in CORS.

## How the environment reaches each part

- `setuphelper.exe` (MSI custom action) has the environment baked in at build time and
  writes it to `daemon.env` as `STONKAGENTS_ENV`, together with the daemon port and the
  gateway URL in `config.yaml`. It creates this environment's services, firewall rule and
  nothing else.
- The daemon and the controller read `STONKAGENTS_ENV` (the service wrapper passes
  `daemon.env` to the daemon; the controller loads it itself) and take their listen
  address, the daemon health URL, the service names and the firewall rule from the profile.
- `stonkagents-tools.exe` and `gateway-setup.exe` are installed into the install folder and run
  by the "<ProductName> command tools" scheduled task setuphelper registers (see "The
  command tools run in the background" below) with `--env <env>`; they run the
  post-install script and the CLI with `STONKAGENTS_ENV`, `STONKAGENTS_DAEMON_URL`,
  `OPENCLAW_PROFILE` and `OPENCLAW_GATEWAY_PORT` set (production: CLI defaults).
- `post-install.ps1` also reads `STONKAGENTS_ENV` from the `daemon.env` next to it,
  so the Start menu "Configure ... AI" shortcut works without the bundle.
- The CLI wrapper in the install folder sets the same variables before calling `npx`.

Uninstalling one environment stops and deletes only its own two services, its firewall
rule, its command tools scheduled task, its Program Files folder and its Start menu entries. Data folders under the user
profile are kept, as before.

## The command tools run in the background (2.6.0)

The setup exe (Burn bundle) has two packages: Step 1 of 2 installs Node.js when it is
missing, Step 2 of 2 is the StonkAgents MSI ("Agent service, under a minute"). The MSI
copies the files and `setuphelper.exe` writes the config, creates the services, starts
them and then registers and starts the scheduled task

    <ProductName> command tools

(`StonkAgents command tools`, `StonkAgents Staging command tools`, `StonkAgents Dev
command tools`; `internal/commandtools.TaskName`, the WiX side is `CommandToolsTask` in
`Environment.wxi`). The task runs

    "<install folder>\stonkagents-tools.exe" --env <env> --background --data-dir "<data folder>"

as the installing user after the installer has closed: `post-install.ps1` (wait for
the daemon, `npm install -g stonkagents`, `doctor --fix`, `onboard --skip-daemon`) and
then `gateway-setup.exe --env <env> --progress` (`stonkagents gateway install`,
hidden-window task, start). The 2.4.x custom actions `CA_CommandTools` and
`CA_GatewaySetup` and the managed custom action `installer/msica` that relayed their
progress into the setup window are gone: the installer finishes in 1 to 2 minutes on any
machine, and the portal shows the rest.

**Who the task runs as, and why that works from a LocalSystem custom action.**
`CA_RunSetupHelper` is deferred and not impersonated, so `setuphelper.exe` runs as
LocalSystem. It resolves the user of the active console session from that session's
token (`WTSGetActiveConsoleSessionId`, `WTSQueryUserToken`, `commandtools.ConsoleUser`;
reading the token needs SE_TCB, which LocalSystem has) and registers the task with
`schtasks /Create /F /TN "<name>" /XML <file>` from a task definition whose principal is
that user's SID with `LogonType InteractiveToken` and `RunLevel HighestAvailable`, no
triggers, `AllowStartOnDemand`, `MultipleInstancesPolicy IgnoreNew`, a two hour
`ExecutionTimeLimit`. An interactive-token task stores no password and runs on the user's
own desktop, in the user's session, with the user's profile (npm's global prefix, the CLI
config, the gateway task all live there) and, for an administrator, the full token, as
the impersonated custom action had. Then `schtasks /Run /TN "<name>"` starts it; Task
Scheduler launches it in the user's session, so the job survives the installer exiting.
When no console session exists (a remote or `/quiet` install) the helper falls back to
the MSI's `LogonUser` property, which the custom action data passes as a fourth field
(`[INSTALLDIR];[SECRETSDIR];[DATADIR];[LogonUser]`); with neither, the task is skipped
and the portal's Retry registers it later. The XML route was chosen over
`/SC ONCE /ST /SD` because those take locale-dependent date formats.

`stonkagents-tools.exe` is built with `-H=windowsgui` and starts every child with
`CREATE_NO_WINDOW` (`msiprogress.Prepare`): the task runs on the user's desktop, where a
console program would open a black window.

**Status file.** The job writes `<data folder>\command-tools.json` (the environment's data
folder, `%USERPROFILE%\Documents\.stonkagents-<env>\data`; `internal/commandtools`),
atomically (temp file, rename), on every change and at least every 5 seconds:

| Field | Meaning |
|---|---|
| `state` | `running`, `ready`, `failed`; a reader reports `not_started` when the file is missing |
| `phase` | the script's `phase: ...` line or the gateway tool's progress line, e.g. `Downloading the command tools` |
| `detail` | npm's counter while it runs: `213 package files ready`, `... unpacking`, `664 packages installed` |
| `started_at`, `updated_at`, `finished_at` | RFC 3339, UTC |
| `error` | failed only: the child's exit status plus the last plain line it printed |
| `log_path` | `%TEMP%\stonkagents-tools[-<env>].log` of the user the job ran as |
| `attempt` | 1 for the install's run, plus one per retry |
| `pid` | the job's process id |
| `cli_path`, `gateway_task` | what the job found: the npm shim, the CLI's gateway task name |

**Controller.** `GET /setup/command-tools` (loopback only; also
`/api/v1/controller/setup/command-tools` through the daemon's proxy) returns the file
plus `cli_present` (the recorded shim still exists), `gateway_running` (this environment's
gateway port answers), `task_name` and `supported` (Windows). A `running` file that has
not moved for 2 minutes is reported as `failed` ("The setup stopped before it finished"):
the job writes at least every 5 seconds while it lives. `POST /setup/command-tools/retry`
(the setup mutation headers `Content-Type: application/json` and `X-StonkAgents-Setup: 1`,
like every setup POST; loopback; rate limited) runs `schtasks /Run` on the task, after
registering it for the console user when it is missing, writes a `running` placeholder so
readers stop seeing the old failure at once, and answers `202 {status: "started",
task_name, attempt}`; `409 ALREADY_RUNNING` while a live job is in progress, `500
RETRY_FAILED` with schtasks' output (for example when the user is not logged on), `500
NO_USER` when the task is missing and no console session exists, `501 NOT_SUPPORTED` on
macOS. The controller runs as LocalSystem, so `schtasks /Run` from session 0 on the
interactive-token task starts the job on the user's desktop.

**Portal.** `useCommandTools` (apps/portal, `daemon-command-tools.ts`) polls the
controller through the daemon proxy (direct controller URL as the fallback, like the update
status) every 5 seconds while the job runs and every 60 seconds otherwise, and the
`CommandToolsChip` next to the agent status in the navbar shows `Command tools: setting
up (213 package files ready, 1:42)` with the phase, `Command tools ready` (hidden a minute
after `finished_at`) or `Command tools failed: <error>` with a Retry button. The "Open
OpenClaw" link on the chat page is disabled with a tooltip until the state is ready.

**Retry by hand.** Task Scheduler: run "<ProductName> command tools"; or
`schtasks /Run /TN "StonkAgents command tools"`; or double-click `stonkagents-tools.exe` in
the install folder (silent; same status file). The Start menu "Configure ... AI" shortcut
runs only the script part, visibly.

**Uninstall and rollback.** The uninstaller ends the task (`schtasks /End`) and deletes it
(`schtasks /Delete /F`; `CA_EndCommandToolsTask`, `CA_DeleteCommandToolsTask`, before
`RemoveFiles`). Cancelling the install after the files were copied runs the same two
commands as rollback actions (`CA_RollbackEndCommandToolsTask`,
`CA_RollbackDeleteCommandToolsTask`), so a rolled-back install leaves no job running.
Every command the script runs still has its own hard timeout (npm install 600 s, doctor
180 s, onboard 300 s, npm queries 60 s), and the task itself has a two hour limit.

### Only the gateway step starts the gateway (2.4.2)

The 2.4.0 staging installer was seen sitting on the command tools step for over half an
hour with the gateway running in a console window. Two things in the command tools step
could do that, and 2.4.2 removed both (they still apply to the background job):

- `doctor --fix --yes` auto-approves the CLI's runtime repairs ("Install gateway service
  now?", "Start/Restart gateway service now?", the gateway service config rewrite), each
  of which installs or (re)starts the gateway from inside the script, before `onboard` has
  written the config and before `gateway-setup.exe` sets the task up. The script runs
  doctor with `OPENCLAW_UPDATE_IN_PROGRESS=1`, the CLI's own switch for "repair the config,
  leave the gateway alone" (its updater uses it): config repairs still apply, the gateway
  is neither installed, started nor restarted. `onboard` runs with `--skip-daemon` (and
  `--skip-health` as before) and a 300 s timeout.
- A gateway started from inside the script inherits the script's stdout and stderr pipes,
  and a plain wait for end of file never returns while the gateway lives, even after the
  script has exited. `stonkagents-tools.exe` and `gateway-setup.exe` set `WaitDelay` on the
  child (Go's `os/exec`: the pipes are closed by the tool five seconds after the child
  exits, the log says "a process it started still holds its output"). Regression test:
  `installer/msiprogress/wait_test.go` (a child whose grandchild holds the pipes).

## Release notes text for 2.6.0

Use this as `RELEASE_NOTES` when publishing the 2.6.0 manifests:

```
2.6.0: the Windows installer finishes in 1 to 2 minutes. The command tools (the
stonkagents CLI, its onboarding and the OpenClaw gateway) are now installed by a
background task after setup; the portal shows their progress next to your agent's status
and offers a retry if npm fails. Uninstalling removes the task.
```

## Release notes text for 2.4.2

Use this as `RELEASE_NOTES` when publishing the 2.4.2 manifests (staging inserts
" (staging)" after 2.4.2):

```
StonkAgents 2.4.2: fixes the setup hanging on the command tools step (the gateway was
started during onboarding and kept the installer waiting); onboarding now runs with a
time limit and the gateway is set up by its own step. The agent chat can now open the
OpenClaw gateway in a new tab. Includes 2.4.1 and 2.4.0.
```

## Release notes text for 2.4.1

Use this as `RELEASE_NOTES` when publishing the 2.4.1 manifests:

```
2.4.1: the Windows installer shows its whole progress in one window. The command tools
and gateway steps no longer open a second window: their phase, npm download count and
elapsed time appear under the setup window's progress bar, refreshed at least every five
seconds, and Cancel now stops them. The setup has two steps (Node.js when missing, then
StonkAgents with its command tools and gateway) and the progress label no longer repeats
"Step:". Everything else (folders, services, ports, logs in %TEMP%) is unchanged.
```

## Release notes text for 2.4.0

Use this as `RELEASE_NOTES` when publishing the 2.4.0 manifests:

```
2.4.0: dev, staging and production installs can now live side by side on one Windows
machine. Each environment has its own install folder (StonkAgents, StonkAgents Staging,
StonkAgents Dev), data folder (.stonkagents, .stonkagents-stg, .stonkagents-dev), services,
ports (daemon 7841/7851/7861, controller 7840/7850/7860), firewall rule, CLI profile and
update identity, so installing one never replaces another. Production keeps every name,
folder and port it had, and upgrades in place as before. The installer now numbers its
steps and shows a live progress window with elapsed time while the command tools install.
```
