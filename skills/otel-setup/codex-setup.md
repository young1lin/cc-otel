# Codex CLI Telemetry Setup

OpenAI Codex CLI supports OTLP telemetry via configuration. Both Claude Code and Codex share the same OTLP gRPC port (`:4317`); cc-otel auto-detects the source via `service.name` in the OTLP Resource attributes and routes to independent `codex_*` tables.

## Prerequisites

- cc-otel is running (`cc-otel status` should show active)
- Default OTLP gRPC port is `4317`

## Configuration

Edit or create `~/.codex/config.toml` (back up first, then append — do not overwrite existing settings):

```toml
[otel]
environment = "dev"
exporter.otlp-grpc.endpoint = "http://localhost:4317"
trace-exporter.otlp-grpc.endpoint = "http://localhost:4317"
metrics-exporter.otlp-grpc.endpoint = "http://localhost:4317"
```

Location varies by OS:
- Windows: `%USERPROFILE%\.codex\config.toml`
- macOS / Linux: `~/.codex/config.toml`

## Verify

1. Start a Codex CLI session and make a request
2. Open `http://localhost:8899/?source=codex` or click the "Codex" tab
3. Check that token counts appear in the dashboard

## Differences from Claude Code

- **No cost data**: Codex does not report `cost_usd`. cc-otel recomputes cost using the local pricing table.
- **Token data via SSE events**: Codex sends token counts in `codex.sse_event(kind=response.completed)`, not in a single `api_request` event.
- **Separate tables**: All Codex data is stored in `codex_*` tables, completely isolated from Claude Code data.

## How cc-otel Identifies Codex Data

Codex is detected by OTLP Resource `service.name` containing `codex` (case-insensitive). Known values: `codex_cli_rs`, `codex_exec`, `codex-app-server`, `codex_mcp_server`.

## Data Flow

Unlike Claude Code (single `api_request` event with all data), Codex sends data across multiple events that cc-otel merges:

| Event | Purpose |
|-------|---------|
| `codex.api_request` | Request metadata (model, conversation ID); token fields are usually zero |
| `codex.sse_event` + `response.completed` | Token counts (input, output, cache read, cache creation) |
| `codex.websocket_event` + `response.completed` | Duration (`duration_ms`) |

cc-otel merges these into a single `codex_api_requests` row using a 5-minute span tracker keyed on `(conversation.id, model)`.

## Troubleshooting

### No data in Codex tab

1. Check `~/.codex/config.toml` has correct TOML syntax
2. Confirm all three endpoint lines point to cc-otel's OTLP port (default `4317`)
3. Restart Codex CLI after config changes

### Cost shows $0

Codex CLI does not report `cost_usd`. cc-otel recomputes cost using the local pricing table. If pricing data is missing for the model, cost stays at $0. Check with:

```
GET /api/pricing/lookup?model=<your-model>
```

### Proxy warning

If you use `http_proxy` / `https_proxy` (Clash, V2Ray, etc.), you **must** add `no_proxy` to exclude localhost — otherwise OTLP gRPC traffic to `localhost:4317` goes through the proxy and silently fails:

```json
// In ~/.claude/settings.json "env" section
"no_proxy": "localhost,127.0.0.1"
```
