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

One robbyrussell-style line for Claude Code's statusline, combining the
workspace dir, git branch/dirty marker, model, context window and subscription
quotas. Reads the **cache only** — never the network — so it always returns
instantly and survives API outages by showing stale data.

```
➜ subscription-monitor git:(main)✗ GLM-5.3 [12.3k/200k 6%] │ ⚡ZHIPU 5h left 97%·4h22m │ W left 80%·5d13h (↻2m)
```

- The quota segment is prefixed with the provider (`ZHIPU` / `MINIMAX`) so
  subscriptions are distinguishable at a glance.
- Percentages are marked `left` (remaining); the reset countdown follows (`·`).
- Freshness trailer: `(↻2m)` counts down to the next cache refresh while data
  is fresh; `(age 36m)` (yellow) flags data older than the TTL.
- Provider selection order: `--provider` flag → `ANTHROPIC_BASE_URL` domain
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

### `submon generate config`

Merge-generate `~/.config/submon/config.toml` against the current schema:
existing values are preserved, new fields are filled with defaults, and a
MiniMax token is migrated from `~/.minimax-config.json` when present (that
file can then be deleted). Dry-run with a masked diff by default;
`--write` applies.

```sh
submon generate config          # preview diff (tokens masked)
submon generate config --write  # apply
```

### `submon notify --test`

Send a test Gotify push to verify connectivity.

### `submon cache [--clear]`

Dump / delete the cache file (`~/.cache/submon/cache.json`).

## Configuration

`~/.config/submon/config.toml` — see [`submon.example.toml`](submon.example.toml).
Credentials resolve in this order:

| provider | config | environment |
|---|---|---|
| zhipu | `[zhipu] token/base_url` | `ANTHROPIC_AUTH_TOKEN` / `ANTHROPIC_BASE_URL` |
| minimax | `[minimax] token` | `MINIMAX_TOKEN` |

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
