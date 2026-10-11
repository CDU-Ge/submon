# subscription-monitor (submon)

Monitors coding-plan quotas for **Zhipu** (open.bigmodel.cn / api.z.ai),
**MiniMax** and **OpenCode** (opencode.ai/zen), with a shared cache so
high-frequency consumers (terminal statuslines) never hammer the APIs, plus
Gotify push alerts on threshold crossings.

Go CLI built with cobra. Runs on Linux, macOS and Windows. Windows releases
embed a small, separate statusline renderer deployed by `submon install`.

## Install

```sh
go build -o ~/.local/bin/submon .          # POSIX
```

On Windows (PowerShell), build the CLI with its embedded renderer:

```powershell
.\scripts\build.ps1
```

Then wire it up: `submon login` (credentials) and `submon install` (periodic
refresh + Claude Code statusline).

## Windows

The CLI commands are the same; statusline uses a separate one-shot executable:

| Concern | Where it lives on Windows |
| --- | --- |
| config.toml, secret.key | `%AppData%\submon\` (roams with the user profile) |
| cache.json, completions | `%LocalAppData%\submon\` (machine-local) |
| statusline renderer | `%LocalAppData%\submon\statusline\submon.exe` |
| Claude Code settings | `%USERPROFILE%\.claude\settings.json` |
| periodic refresh | Task Scheduler task `submon-watch` (see `submon install`) |
| shell completion | PowerShell: `submon completion-install --shell powershell` |

```powershell
submon login                     # credentials (encrypted at rest)
submon install                   # Task Scheduler task + Claude Code statusline
submon status                    # quota panel
submon check                     # diagnose config / cache / task / Gotify
```

Notes:

- `submon install` deploys the embedded statusline renderer and migrates settings
  pointing at the CLI. Both executables retain the name `submon.exe`. The renderer
  uses the Windows GUI subsystem to avoid console startup, reads the same caches,
  and exits after each call. The main CLI retains normal console behavior.
- Existing custom statusline commands are preserved. Use `submon install claude`
  to explicitly switch them to submon. `--print` previews without deployment.
- A plain `go build` from a clean tree does not bundle the renderer; installation
  falls back to the main CLI. Use `scripts/build.ps1`, `make build` on Windows, or
  `make release` to include it.

- `submon install` registers the task via `schtasks`, running a generated VBS
  launcher (`%AppData%\submon\submon-watch.vbs`) so no console window flashes
  every interval. `--interval` must be whole minutes (1–1439).
- Console output is UTF-8 with ANSI color enabled (`SetConsoleOutputCP(65001)` +
  `ENABLE_VIRTUAL_TERMINAL_PROCESSING`); colors are turned off automatically if
  the console cannot do ANSI.
- `submon install --uninstall` deletes the task and the launcher.

## Commands

### `submon status [zhipu|minimax|opencode]`

Colored panel view of every configured provider (5h / weekly windows, monthly
MCP quota for zhipu, per-model usage, reset countdowns, data age):

```
ZHIPU · pro  fetched 3m ago
────────────────────────────────────────────────
  5h          ██████████░░  83% left · 2h29m
  weekly      █████████░░░  77% left · 5d11h
  monthly     ████████████ 100% left · 15d11h · 1.0K/1.0K
  MCP/tools   1.0K / 1.0K remaining
  models 24h  GLM-5.3 70.5M · GLM-4.7 122.9K
```

```sh
submon status            # all configured providers, cache-first
submon status zhipu      # one provider
submon status --force    # refetch now and update the cache
submon status --json     # machine-readable
```

Serves cached data within the TTL (default 5 min); beyond it, refetches.
Bars and percentages are colored by threshold (green / yellow / red).

### `submon statusline`

One robbyrussell-style line for Claude Code's statusline, combining the
workspace dir, git branch/dirty marker, model, context window and subscription
quotas. Reads the **cache only** — never the network — so it always returns
quickly and survives API outages by showing stale data.

Rendering never launches `watch` or updates quota caches. Run `submon install`
to schedule refreshes, or `submon watch` for an immediate refresh. Missing data
and expired caches are displayed honestly without starting background jobs.
Git status is cached per directory for 5 seconds, with a 150 ms query budget;
failed queries are throttled too and preserve the last known branch. If the
Git cache cannot be written, the query is skipped.

```
➜ subscription-monitor git:(main)✗ GLM-5.3 [12.3k/200k 6%] 5h 97% left·4h22m / W 80% left·5d13h (↻2m)
```

- Quota windows read `5h 97% left·4h22m`: percentage, then the `left` marker,
  then the reset countdown; the 5h and weekly windows are separated by a dim
  `/`.
- Statusline never displays a provider badge.
- Freshness trailer: `(↻2m)` counts down while fresh; `(缓存过期·36m前)`
  marks stale data.
- Provider comes only from the session's `ANTHROPIC_BASE_URL`; unknown URLs
  omit quotas. Use `submon status <provider>` to inspect another subscription.
- The former `--provider`, `--show-provider` and `SUBMON_SHOW_PROVIDER`
  controls have been removed. `submon install claude` removes the old flags
  from settings pointing to this binary while preserving other settings.

Claude Code `~/.claude/settings.json` snippet:

```json
{
  "statusLine": {
    "type": "command",
    "command": "/home/you/.local/bin/submon statusline",
    "padding": 0
  }
}
```

### `submon watch`

One-shot job for a systemd timer: force-refresh all providers, update the
cache (which keeps statusline warm), then run the alert state machine and push
**only on transitions**:

- ok → warn (remaining below `warn_below_pct`, default 20%)
- warn → crit (below `crit_below_pct`, default 5%)
- warn/crit → ok (recovered)

Robustness: alert state persists in the cache file (restarts never re-notify);
a failed Gotify push does not advance the state (retried next tick); a failed
API fetch skips evaluation entirely (no false "recovered").

### `submon install`

Install and enable the periodic `submon watch` job — a systemd user service +
timer on POSIX, a Task Scheduler task on Windows — and register the Claude Code
statusline, in one command:

```sh
submon install                # register the schedule + wire the statusline (5m default)
submon install --interval 15m # custom interval
submon install --print        # preview what would be written
submon install --uninstall    # stop, disable and remove
```

Credentials are never baked into the generated units — use `submon login`, or
`~/.config/environment.d/` on systemd hosts. On machines without a systemd user
session the units are still written, with the manual enable command printed.

### `submon config`

Merge-generate `~/.config/submon/config.toml` against the current schema:
existing values are preserved, new fields are filled with defaults, and a
MiniMax token is migrated from `~/.minimax-config.json` when present (that
file can then be deleted). Dry-run with a masked diff by default;
`--write` applies.

```sh
submon config          # preview diff (tokens masked)
submon config --write  # apply
```

### `submon notify test`

Send a test Gotify push to verify connectivity.

### `submon cache [clear]`

Dump / delete the cache file (`~/.cache/submon/cache.json`).

### `submon login` — encrypted credential storage

Credentials are stored in `[[accounts]]` and encrypted at rest with
**AES-256-GCM**; config.toml only ever holds `enc:v1:...` ciphertext. The
master key is self-managed (no OS keyring dependency): generated on first use
and stored at `~/.config/submon/secret.key` (0600). Keep that file out of
backups/sync if you sync config.toml.

```sh
submon login                 # interactive: provider → label → token
submon login ls              # list accounts and storage form (masked)
submon login remove          # interactive account removal
submon login discover        # scan env / Claude Code settings and import
```

For scripts, feed the fields on stdin (never argv):

```sh
printf 'zhipu\nalice\n%s\n\n' "$TOKEN" | submon login
```

Each account is `{provider, label, token, base_url?}`; the same provider can
have multiple accounts (different labels) and each is fetched separately.
Gotify is a first-class account too (`provider = "gotify"`, `base_url` =
server URL, `token` = app_token).

`submon config --write` migrates the old `[zhipu] token` / `[[zhipu.keys]]`
schema and plaintext tokens (including the legacy `~/.minimax-config.json`)
in one pass.

### `submon enc`

Encrypt/decrypt arbitrary values with the same master key, for use in other
tools' configs:

```sh
submon enc "secret"          # → enc:v1:...
echo "enc:v1:..." | submon enc -d
```

## Windows statusline resource test

Run from a Git repository in PowerShell after building `submon.exe`:

```powershell
.\scripts\statusline-stress.ps1 -Submon .\submon.exe -Workspace . -Iterations 10000
# Continuous loop within a 30-minute limit, with no deliberate delay:
.\scripts\statusline-stress.ps1 -Submon .\submon.exe -Iterations 0 -DurationSeconds 1800 -DelayMilliseconds 0
```

To measure the renderer actually installed on Windows:

```powershell
.\scripts\statusline-stress.ps1 -Submon "$env:LOCALAPPDATA\submon\statusline\submon.exe" -Iterations 500 -DelayMilliseconds 0 -TraceStages
```

For latency diagnosis without sampling or deliberate delays:

```powershell
.\scripts\statusline-stress.ps1 -Submon .\submon.exe -Iterations 200 -DelayMilliseconds 0 -NoProcessSampling -TraceStages
```

The summary separates loop time from the 3-second observation tail and reports
per-call P50/P95. Optional stage tracing writes only to stderr. Post-exit memory
queries are opt-in (`-CaptureInvocationMemory`) and excluded from call latency.

The script pipes mock session JSON into each invocation, with a 3-second
call timeout. It samples submon, Git, OpenConsole, conhost and PowerShell
processes every 100 ms and flushes each log entry to disk. Logs are retained
in a unique `statusline-logs/` directory, including invocation output/timing,
per-process private bytes/working sets/handles, baseline/peak/final totals,
and run metadata. It stops on failure or excessive growth (defaults: 512 MiB
or 64 extra processes). See [test guide](docs/statusline-stress.md) for
comparison steps and Windows regression tests.

## Configuration

`~/.config/submon/config.toml` (Windows: `%AppData%\submon\config.toml`) — see
[`submon.example.toml`](submon.example.toml).
Credentials live in `[[accounts]]` (`provider` / `label` / `token` /
`base_url`); environment variables still win for the default account:

| provider | account | environment |
|---|---|---|
| zhipu | `provider = "zhipu"` token (encrypted) / base_url | `ANTHROPIC_AUTH_TOKEN` / `ANTHROPIC_BASE_URL` |
| minimax | `provider = "minimax"` token (encrypted) | `MINIMAX_TOKEN` |
| opencode | `provider = "opencode"` token (encrypted) | `OPENCODE_API_KEY` |
| kimi | `provider = "kimi"` token (encrypted) / base_url | `KIMI_CODING_API_KEY` / `KIMI_API_KEY` |
| gotify | `provider = "gotify"` app_token (encrypted) / base_url | — |

## Build

```sh
make build      # → ./submon (version info injected; .exe on Windows)
make install    # → ~/.local/bin/submon (POSIX)
make test       # unit tests
make release    # cross-compile linux/amd64, darwin/{amd64,arm64}, windows/amd64 into dist/
```

## Notes on API semantics

- Zhipu `quota/limit` returns one entry per window; windows are identified by
  `(type, unit, number)`: `(TOKENS_LIMIT, 3, 5)` = 5h, `(TOKENS_LIMIT, 6, 1)` =
  weekly, `(TIME_LIMIT, 5, 1)` = monthly MCP calls. Every entry carries
  `nextResetTime`. Zhipu's `percentage` is **used** %.
- MiniMax `coding_plan/remains` returns `model_remains[]` at the top level
  with epoch-ms timestamps; `*_remaining_percent` is literally **remaining** %
  (the opposite of Zhipu). The snapshot layer normalizes both to remaining.
- OpenCode zen `/zen/go/v1/usage` returns `usage.{rolling,weekly,monthly}`
  with `percent` (**used** %, like Zhipu) and RFC3339 `resetsAt`. rolling →
  the 5h short window, weekly → the long window, monthly → the extra monthly
  row.
- The old `scripts/zhipu-usage-query.mjs` labeled every TOKENS_LIMIT as
  "5 Hour", dropping the weekly window; submon parses all three windows.
