---
name: otel-setup
description: |
  Use when the user asks about setting up OTEL telemetry for Claude Code,
  configuring cc-otel, troubleshooting telemetry data collection, or
  understanding their token usage through the cc-otel dashboard.
  Trigger with "set up telemetry", "configure otel", "token dashboard",
  "cc-otel not working", "no data in dashboard".
---

# CC-OTEL Setup & Troubleshooting

## What is CC-OTEL

CC-OTEL is a self-hosted OTLP gRPC receiver + Web dashboard for Claude Code token usage and cost tracking.

Architecture:
```
Claude Code ──OTLP gRPC(:4317)──> cc-otel ──> SQLite
                                      |
                                  Web UI <── Browser (localhost:8899)
```

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

### Dashboard shows wrong model names

Use `model_mapping` in `cc-otel.yaml` to map proxy model names to actual Claude models:

```yaml
model_mapping:
  my-proxy-model: claude-sonnet-4-6
  another-proxy: claude-opus-4-6
```

### Cost shows $0

Cost data comes from the `cost_usd` field in Claude Code's OTEL events. If your setup uses a proxy that doesn't pass through cost, the dashboard won't have cost data.

### High DB size

Run `cc-otel cleanup` or set `retention_days` in config:

```yaml
retention_days: 30  # default is 90, set 0 to keep forever
```

## Config File Locations

CC-OTEL looks for `cc-otel.yaml` in order:
1. Next to the executable (portable mode)
2. `~/.claude/` (legacy)
3. `%APPDATA%\CC-OTEL\` (Windows) or `~/.config/CC-OTEL/` (Unix)

## Environment Variable Overrides

| Variable | Default | Description |
|----------|---------|-------------|
| `CC_OTEL_OTEL_PORT` | 4317 | OTLP gRPC port |
| `CC_OTEL_WEB_PORT` | 8899 | Web dashboard port |
| `CC_OTEL_DB_PATH` | (auto) | SQLite database path |
