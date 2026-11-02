---
name: otel-setup
description: |
  Use when the user asks about setting up OTEL telemetry for Claude Code,
  Codex, opencode, or DeepSeek Harness (dsh); configuring cc-otel;
  troubleshooting telemetry data collection; or understanding their token
  usage through the cc-otel dashboard.
  Trigger with "set up telemetry", "configure otel", "token dashboard",
  "cc-otel not working", "no data in dashboard", "send opencode usage to
  cc-otel", "dsh telemetry", "deepseek harness telemetry".
---

# CC-OTEL Setup & Troubleshooting

## What is CC-OTEL

CC-OTEL is a self-hosted OTLP receiver (gRPC **and** HTTP) + Web dashboard for token usage and cost tracking.
Default source is **Claude Code**; also supports **Codex**, **opencode**, and **DeepSeek Harness** (see below).

Architecture:
```
Claude Code ─┐
Codex        ┴─OTLP gRPC(:4317)─┐
                                ├─> cc-otel ──> SQLite
opencode     ─┐                 |       |
DeepSeek     ─┴─OTLP HTTP(:4318)┘   Web UI <── Browser (localhost:8899)
Harness
```

Both ports feed the same receiver, so cost recompute, attribution, dedup and live
updates behave identically no matter which transport a client uses.

## Quick Setup

1. Run `/cc-otel:setup` to auto-install and configure
2. Restart Claude Code (env vars take effect on restart)
3. Open dashboard: `http://localhost:8899`

## Manual Configuration

Add to `~/.claude/settings.json` under `"env"` (merge only, preserve other keys):

```json
{
  "env": {
    "CLAUDE_CODE_ENABLE_TELEMETRY": "1",
    "OTEL_EXPORTER_OTLP_PROTOCOL": "grpc",
    "OTEL_EXPORTER_OTLP_ENDPOINT": "http://localhost:4317",
    "OTEL_METRICS_EXPORTER": "otlp",
    "OTEL_LOGS_EXPORTER": "otlp"
  }
}
```

## Troubleshooting

### No data showing in dashboard

1. **Is cc-otel running?** Check: `cc-otel status`
2. **Is OTEL enabled?** Check `~/.claude/settings.json` has `CLAUDE_CODE_ENABLE_TELEMETRY=1`
3. **Did you restart Claude Code?** Env vars only load on startup.
4. **Port conflict?** Check if another service uses :4317. Change via `cc-otel.yaml` → `otel_port`.

For a non-Claude-Code source, the transport and endpoint form are the usual culprits —
see the per-tool guide linked under [Other Tool Sources](#other-tool-sources). `/api/status`
reports whether each receiver is actually listening (`otel_receiver_listening`,
`otlp_http_receiver_listening`).

### Dashboard shows wrong model names

Use `model_mapping` in `cc-otel.yaml` to map proxy model names to actual Claude models:

```yaml
model_mapping:
  my-proxy-model: claude-sonnet-4-6
  another-proxy: claude-opus-4-6
```

### Cost shows $0

Which answer applies depends on the source, because only **Claude Code on a Claude model** keeps its upstream-reported cost. Everything else is recomputed locally.

- **Claude Code + a Claude model** — the number comes from Claude Code's own `cost_usd` field. A proxy that strips it leaves no cost data.
- **Any other combination** (Codex, opencode, DSH, or Claude Code driven through a non-Claude model) — cc-otel computes cost from the local pricing table, so $0 means **no price is stored for that model**. Check it:

  ```
  GET /api/pricing/lookup?model=<your-model>
  ```

  A miss (`"found": false`) is the cause. Add the model in the Pricing Table modal (Server Status → Pricing Table), then click ↻ 重算历史 to backfill existing rows. Models the upstream catalog has not picked up yet — `glm-5.x` and similar recent releases — always need this.

- **Claude models driven by opencode/DSH** additionally need a Claude price row you add yourself: the bundled seed intentionally contains no Claude entries.

### High DB size

Run `cc-otel cleanup` or set `retention_days` in config:

```yaml
retention_days: 30  # default is 90, set 0 to keep forever
```

## Config File Locations

CC-OTEL resolves its data directory (`internal/config/config.go` `defaultDataDir`) in order:
1. The `bin/` directory containing the executable (dev mode — a `make build` layout)
2. `~/.cc-otel/` (default; auto-created). A pre-v0.1.0 `~/.claude/cc-otel/` install is migrated here automatically on first run
3. `.` (fallback only when the home directory can't be resolved)

Config, DB, PID and log files all live in that one directory: `cc-otel.yaml`, `cc-otel.db`, `cc-otel.pid`, `cc-otel.log`.

## Environment Variable Overrides

| Variable | Default | Description |
|----------|---------|-------------|
| `CC_OTEL_OTEL_PORT` | 4317 | OTLP gRPC port |
| `CC_OTEL_OTLP_HTTP_PORT` | 4318 | OTLP/HTTP port (`0` disables) |
| `CC_OTEL_WEB_PORT` | 8899 | Web dashboard port |
| `CC_OTEL_DB_PATH` | (auto) | SQLite database path |

## Other Tool Sources

cc-otel also receives telemetry from **Codex**, **opencode**, and **DeepSeek Harness**.
When the user asks about configuring one, read the corresponding guide in this folder:

| Tool | Transport | Endpoint to configure | Setup Guide |
|------|-----------|-----------------------|-------------|
| Codex | OTLP gRPC | `http://localhost:4317` | [`codex-setup.md`](codex-setup.md) |
| opencode | OTLP HTTP | `http://localhost:4318` (base url — SDK appends `/v1/traces`) | [`opencode-setup.md`](opencode-setup.md) |
| DeepSeek Harness | OTLP HTTP | `http://localhost:4318/v1/logs` (full path) | [`dsh-setup.md`](dsh-setup.md) |

The endpoint forms genuinely differ and are the most common setup mistake — opencode
wants the **base** url because its SDK appends the signal path, while DSH wants the
**complete** logs path. Swapping them yields a 404 or a silently dead exporter.

Where the data lands:

| Tool | Tables | Dashboard view |
|------|--------|----------------|
| Codex | unified pool (`api_requests`, `service_name='codex'`; legacy `codex_*` archive merges via one-click migration) | All Clients, **Source** column |
| opencode, DeepSeek Harness | shared Claude tables, tagged by `service_name` | main view, **Source** column |

Since the AllClient merge every client — Codex included — is normalized into the
Claude Code shape and shares one unified pool, distinguished by the `service_name`
attribution label rather than by a query filter. Codex's pre-merge `codex_*`
archive is pulled in by the one-click migration (the "Merge Codex history" button in the DB-import dialog); live Codex
reports are normalized at ingest. The legacy `/api/codex/*` read routes still serve
the codex-specific dashboards from the same pool.

Cost applies to all of them: only Claude Code on a Claude model keeps its
upstream-reported `cost_usd`. Everything else is recomputed from the local pricing
table, so an unpriced model shows $0 until you add a row.
