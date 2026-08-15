# opencode Telemetry Setup

opencode is built on the Vercel AI SDK, which reports token usage on GenAI **spans** rather than the log records Claude Code sends. cc-otel reshapes the usage span into the same `api_request` shape, so opencode data lands in the normal Claude Code tables and views — there is no separate tab.

opencode speaks **OTLP/HTTP (`:4318`)**, not gRPC. Pointing it at `:4317` will not work.

## Prerequisites

- cc-otel is running (`cc-otel status`)
- OTLP/HTTP port is enabled (`otlp_http_port: 4318` in `cc-otel.yaml`, the default)

## Configuration

Two pieces are required: turn telemetry on in opencode, and tell the OTel SDK where to send it.

**1. Enable it** in `opencode.jsonc` (or `opencode.json`):

```jsonc
{
  "$schema": "https://opencode.ai/config.json",
  "experimental": {
    "openTelemetry": true
  }
}
```

**2. Point it at cc-otel** with the standard OTel environment variable:

```powershell
# Windows (persist for the user, so every opencode run picks it up)
[Environment]::SetEnvironmentVariable('OTEL_EXPORTER_OTLP_ENDPOINT','http://localhost:4318','User')
```

```bash
# macOS / Linux — add to your shell profile
export OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318
```

> **Use the BASE url, not the full signal path.** The OTel SDK appends `/v1/traces` itself. Setting `http://localhost:4318/v1/traces` here makes it request `/v1/traces/v1/traces`, which 404s.

Config file locations:
- Windows: `%USERPROFILE%\.config\opencode\opencode.jsonc`
- macOS / Linux: `~/.config/opencode/opencode.jsonc`

## Verify

1. Run an opencode session and let a model reply
2. Open `http://localhost:8899` (the normal Claude Code view, not a separate tab)
3. Look for rows whose **Source** column reads `opencode`

## Differences from Claude Code

- **No cost data reported.** opencode sends no `cost_usd` at all, so cc-otel always recomputes cost from the local pricing table for these rows — trusting upstream would pin them at $0 forever.
- **Claude models need a price row you add yourself.** The bundled pricing seed intentionally ships **no Claude entries** (the only client that needs none — Claude Code — reports its own cost). If you drive Claude models through opencode, add the Claude rows in the Pricing Table modal or `pricing:` YAML, or those rows stay at $0.
- **Usage arrives on spans**, from the leaf call (`ai.streamText.doStream` / `ai.generateText.doGenerate`). The parent span carries usage too; cc-otel deliberately ignores it, because counting both double-counts every request.
- **`request_id` is the hex span id**, so exporter retries collapse onto one row.

## How cc-otel Identifies opencode Data

By OTLP Resource `service.name`. When an AI-SDK usage span carries **no** `service.name`, cc-otel attributes it to `opencode` rather than the global `claude-code` default — anything emitting these spans is definitively not Claude Code.

Set `OTEL_SERVICE_NAME` if you run several AI-SDK clients and want them attributed separately.

## Troubleshooting

### No data in the dashboard

1. Confirm the endpoint is HTTP `:4318`, not gRPC `:4317` — opencode cannot speak gRPC to cc-otel
2. Confirm `OTEL_EXPORTER_OTLP_ENDPOINT` is the **base** url (no `/v1/traces` suffix)
3. Confirm `experimental.openTelemetry` is `true` and the JSONC parses
4. Restart opencode — the setting is read at startup

### Inspect what the client actually sends

`tools/otlp_dump` serves both transports and stores nothing. For AI-SDK usage spans it prints the `would_store_api_request` cc-otel would derive, rendered by the same code path as real ingestion, so it cannot drift:

```bash
go run ./tools/otlp_dump
```

For a one-line-per-request trace inside the running daemon instead, start it with `CC_OTEL_DEBUG_AISDK=1` (off by default — these clients are chatty).

### Token counts look inflated

cc-otel assumes the AI SDK reports `inputTokens` **inclusive** of cached tokens (OpenAI-compatible providers do), and derives uncached input as the remainder. If a provider reports input *exclusive* of cache, that derivation under-counts; the daemon warns once per model when it detects this. Check the log for that warning before trusting the split.

### Cost shows $0

Look the model up — a miss means no price is stored:

```
GET /api/pricing/lookup?model=<your-model>
```

Add it in the Pricing Table modal, then click ↻ 重算历史 to backfill existing rows.
