# subscription-monitor (submon)

Monitors coding-plan quotas for **Zhipu** (open.bigmodel.cn / api.z.ai) and
**MiniMax**, with a shared cache so high-frequency consumers (terminal
statuslines) never hammer the APIs, plus Gotify push alerts on threshold
crossings.

Single Go binary, built with cobra.

## Install

```sh
go build -o ~/.local/bin/submon .
```

## Commands

### `submon status [zhipu|minimax]`

Human-readable quota view (5h / weekly windows, monthly MCP quota for zhipu,
per-model usage, reset countdowns).

```sh
submon status            # all configured providers, cache-first
submon status zhipu      # one provider
submon status --force    # refetch now and update the cache
submon status --json     # machine-readable
```

Serves cached data within the TTL (default 5 min); beyond it, refetches.

### `submon statusline`

One compact line for Claude Code's statusline (or any terminal). Reads the
**cache only** — never the network — so it always returns instantly and
survives API outages by showing stale data.

```
⚡5h 97%·4h22m │ W 80%·5d13h
```

Provider selection order: `--provider` flag → `ANTHROPIC_BASE_URL` domain
detection (`bigmodel.cn`/`z.ai` → zhipu, `minimax` → minimax) →
`statusline.default_provider` in config.

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

Alert state is persisted in the cache file, so restarts never re-notify.

```sh
mkdir -p ~/.config/systemd/user
cp manifests/submon-watch.{service,timer} ~/.config/systemd/user/
systemctl --user daemon-reload
systemctl --user enable --now submon-watch.timer
```

### `submon notify --test`

Send a test Gotify push to verify connectivity.

### `submon cache [--clear]`

Dump / delete the cache file (`~/.cache/submon/cache.json`).

## Configuration

`~/.config/submon/config.toml` — see [`submon.example.toml`](submon.example.toml).
Credentials resolve in this order:

| provider | config | environment | fallback |
|---|---|---|---|
| zhipu | `[zhipu] token/base_url` | `ANTHROPIC_AUTH_TOKEN` / `ANTHROPIC_BASE_URL` | — |
| minimax | `[minimax] token` | — | `~/.minimax-config.json` |

## Notes on API semantics

- Zhipu `quota/limit` returns one entry per window; windows are identified by
  `(type, unit, number)`: `(TOKENS_LIMIT, 3, 5)` = 5h, `(TOKENS_LIMIT, 6, 1)` =
  weekly, `(TIME_LIMIT, 5, 1)` = monthly MCP calls. Every entry carries
  `nextResetTime`. Zhipu's `percentage` is **used** %.
- MiniMax `coding_plan/remains` returns `model_remains[]` at the top level
  with epoch-ms timestamps; `*_remaining_percent` is literally **remaining** %
  (the opposite of Zhipu). The snapshot layer normalizes both to remaining.
- The old `scripts/zhipu-usage-query.mjs` labeled every TOKENS_LIMIT as
  "5 Hour", dropping the weekly window; submon parses all three windows.
