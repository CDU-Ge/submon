# subscription-monitor (submon)

Monitors coding-plan quotas for **Zhipu** (open.bigmodel.cn / api.z.ai),
**MiniMax** and **OpenCode** (opencode.ai/zen), with a shared cache so
high-frequency consumers (terminal statuslines) never hammer the APIs, plus
Gotify push alerts on threshold crossings.

Single Go binary, built with cobra. Runs on Linux, macOS and Windows.

## Install

```sh
go build -o ~/.local/bin/submon .          # POSIX
go build -o submon.exe .                   # Windows (PowerShell / cmd)
```

Then wire it up: `submon login` (credentials) and `submon install` (periodic
refresh + Claude Code statusline).

## Windows

Same binary, same commands — only the plumbing differs:

| Concern | Where it lives on Windows |
| --- | --- |
| config.toml, secret.key | `%AppData%\submon\` (roams with the user profile) |
| cache.json, completions | `%LocalAppData%\submon\` (machine-local) |
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
instantly and survives API outages by showing stale data.

```
➜ subscription-monitor git:(main)✗ GLM-5.3 [12.3k/200k 6%] 5h 97% left·4h22m / W 80% left·5d13h (↻2m)
```

- Quota windows read `5h 97% left·4h22m`: percentage, then the `left` marker,
  then the reset countdown; the 5h and weekly windows are separated by a dim
  `/`.
- Smart provider badge: when the session's model already belongs to the
  displayed subscription (GLM → zhipu, MiniMax → minimax, opencode → opencode)
  the badge is omitted as redundant; otherwise `⚡ZHIPU` / `⚡MINIMAX` /
  `⚡OPENCODE` is shown.
- Freshness trailer: `(↻2m)` counts down to the next cache refresh while data
  is fresh; `(age 36m)` (yellow) flags data older than the TTL.
- Provider selection order: `--provider` flag → `ANTHROPIC_BASE_URL` domain
  detection (`bigmodel.cn`/`z.ai` → zhipu, `minimax` → minimax, `opencode` →
  opencode) → `statusline.default_provider` in config.

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
