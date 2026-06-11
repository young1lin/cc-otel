# Gemini CLI Telemetry Setup

Gemini CLI supports OTLP telemetry natively. Point it at the same cc-otel gRPC endpoint to see token usage in the "Gemini" tab.

## Prerequisites

- cc-otel is running (`cc-otel status` should show active)
- Default OTLP gRPC port is `4317`

## Option 1: Global Config (recommended)

Edit or create `~/.gemini/settings.json` (merge with existing keys):

```json
{
  "telemetry": {
    "enabled": true,
    "useCollector": true,
    "otlpEndpoint": "http://localhost:4317",
    "otlpProtocol": "grpc"
  }
}
```

Location varies by OS:
- Windows: `%USERPROFILE%\.gemini\settings.json`
- macOS / Linux: `~/.gemini/settings.json`

## Option 2: Environment Variables (temporary)

```powershell
# PowerShell
$env:GEMINI_TELEMETRY_ENABLED="true"
$env:GEMINI_TELEMETRY_USE_COLLECTOR="true"
$env:GEMINI_TELEMETRY_OTLP_ENDPOINT="http://localhost:4317"
$env:GEMINI_TELEMETRY_OTLP_PROTOCOL="grpc"
gemini "Hello"
```

```bash
# Bash / Zsh
GEMINI_TELEMETRY_ENABLED=true \
GEMINI_TELEMETRY_USE_COLLECTOR=true \
GEMINI_TELEMETRY_OTLP_ENDPOINT=http://localhost:4317 \
GEMINI_TELEMETRY_OTLP_PROTOCOL=grpc \
gemini "Hello"
```

## Verify

1. Start a Gemini CLI session and make a request
2. Open `http://localhost:8899/?source=gemini` or click the "Gemini" tab
3. Check that token counts and cost appear in the dashboard

## How cc-otel Identifies Gemini Data

Gemini is detected by OTLP Resource `service.name` exactly equal to `gemini-cli`. All data is routed to `gemini_*` tables automatically.

## Differences from Claude Code

- **No cache creation tokens**: Gemini CLI does not emit `cache_creation_tokens`. The "Cache Create" column is hidden in the Gemini tab.
- **Extra token fields**: Gemini reports `thoughts_token_count`, `tool_token_count`, and `total_token_count` (stored but not yet shown in the main dashboard).
- **No cost data**: Gemini CLI does not report `cost_usd`. cc-otel recomputes cost using the local pricing table.
- **Separate tables**: All Gemini data is stored in `gemini_*` tables, completely isolated from Claude Code and Codex data.

## Data Fields

Gemini CLI sends these token fields per request (event: `gemini_cli.api_response`):

| Field | OTLP Attribute |
|-------|---------------|
| Input tokens | `input_token_count` |
| Output tokens | `output_token_count` |
| Cache read tokens | `cached_content_token_count` |
| Thoughts tokens | `thoughts_token_count` |
| Tool tokens | `tool_token_count` |
| Total tokens | `total_token_count` |
| Duration | `duration_ms` |
| Model | `model` |
| Session | `session.id` (from Resource) |

## Troubleshooting

### No data in Gemini tab

1. Check `~/.gemini/settings.json` has correct JSON syntax
2. Confirm `otlpEndpoint` matches cc-otel's OTLP port (default `4317`)
3. Gemini CLI may need a restart after config changes

### Cost shows $0

Gemini CLI does not report `cost_usd`. cc-otel recomputes cost using the local pricing table. If pricing data is missing for the model, cost stays at $0. Check with:

```
GET /api/pricing/lookup?model=<your-model>
```

### Proxy warning

If you use `http_proxy` / `https_proxy` (Clash, V2Ray, etc.), you **must** add `no_proxy` to exclude localhost — otherwise OTLP gRPC traffic to `localhost:4317` goes through the proxy and silently fails:

```json
// In ~/.claude/settings.json "env" section
"no_proxy": "localhost,127.0.0.1"
```
