## Claude Code OpenTelemetry (OTEL) Configuration

Claude Code has built-in OpenTelemetry support for metrics, logs/events, and traces.
Users can self-host an OTEL Collector and receive all telemetry data including token usage and cost.

### Quick Start (Console Debug)

```bash
export CLAUDE_CODE_ENABLE_TELEMETRY=1
export OTEL_METRICS_EXPORTER=console
export OTEL_LOGS_EXPORTER=console
claude
```

### Self-Hosted OTEL Collector

```bash
export CLAUDE_CODE_ENABLE_TELEMETRY=1
export OTEL_METRICS_EXPORTER=otlp
export OTEL_LOGS_EXPORTER=otlp
export OTEL_EXPORTER_OTLP_PROTOCOL=grpc
export OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4317
claude
```

### Key Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `CLAUDE_CODE_ENABLE_TELEMETRY` | Enable OTEL data collection | Disabled |
| `CLAUDE_CODE_ENHANCED_TELEMETRY_BETA` | Enable distributed tracing (beta) | Disabled |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | OTEL Collector endpoint | - |
| `OTEL_EXPORTER_OTLP_PROTOCOL` | Protocol: `grpc`, `http/json`, `http/protobuf` | - |
| `OTEL_EXPORTER_OTLP_HEADERS` | Auth headers: `Authorization=Bearer token` | - |
| `OTEL_METRICS_EXPORTER` | Metrics exporter: `otlp`, `prometheus`, `console`, `none` | - |
| `OTEL_LOGS_EXPORTER` | Logs exporter: `otlp`, `console`, `none` | - |
| `OTEL_TRACES_EXPORTER` | Traces exporter: `otlp`, `console`, `none` | - |
| `OTEL_LOG_USER_PROMPTS` | Log user prompt content | Disabled (redacted) |
| `OTEL_LOG_TOOL_DETAILS` | Log tool parameters | Disabled |
| `OTEL_LOG_TOOL_CONTENT` | Log tool input/output (truncated at 60KB) | Disabled |

### Available Metrics

- `claude_code.token.usage` - Token usage with `type` attribute: `input`, `output`, `cacheRead`, `cacheCreation`
- `claude_code.cost.usage` - Cost in USD, with `model` attribute
- `claude_code.session.count` - Session count
- `claude_code.lines_of_code.count` - Lines of code modified
- `claude_code.commit.count` - Git commits created
- `claude_code.pull_request.count` - Pull requests created
- `claude_code.active_time.total` - Active time in seconds

### Key Events (Logs)

- `claude_code.api_request` - Contains `cost_usd`, `input_tokens`, `output_tokens`, `cache_read_tokens`, `cache_creation_tokens`, `duration_ms`
- `claude_code.user_prompt` - User submitted prompt
- `claude_code.api_error` - API request failure
- `claude_code.tool_result` - Tool execution result

### Telemetry Failed Events

When Claude Code cannot send telemetry to Anthropic's endpoint, events are stored locally at:
`~/.claude/telemetry/1p_failed_events.*.json`

These files contain detailed data including:
- `messageTokens`, `inputTokens`, `outputTokens`
- `cachedInputTokens`, `uncachedInputTokens`
- `costUSD`, `durationMs`, `ttftMs` (time to first token)
- `model`, `requestId`, `stop_reason`

### Community Resources

- SigNoz: https://signoz.io/docs/claude-code-monitoring/
- Grafana + VictoriaMetrics: https://tcude.net/how-i-monitor-my-claude-code-usage-with-grafana-opentelemetry-and-victoriametrics/
- SigNoz GitHub: https://github.com/SigNoz/Claude-Code-OpenTelemetry


# cc-otel

Claude Code token usage monitoring service. Receives OTEL telemetry data, provides a web dashboard to view token consumption and cost.

## Deployment

Production directory: `~/.claude/cc-otel/` (binary, config, database, and logs all in one directory)
Development directory: `./bin/` (make build output, for local debugging)

Environment variables are configured in `~/.claude/settings.json` under `env`, automatically effective for all Claude Code sessions.

## Commands

```bash
cc-otel start                # Start as background daemon
cc-otel restart              # Stop then start (hot-swap binary after local go build)
cc-otel stop                 # Stop the daemon
cc-otel status               # Show status (PID, ports, today's stats)
cc-otel serve                # Run in foreground (for debugging)
cc-otel start -config path   # Start with a custom config file
```

## Configuration

`~/.claude/cc-otel/cc-otel.yaml` (production) or `./bin/cc-otel.yaml` (development):

```yaml
otel_port: 4317
web_port: 8899
db_path: ~/.claude/cc-otel/cc-otel.db
model_mapping: {}
```

## Development

```bash
make build
go test ./...
```

### How frontend changes take effect (important)

By default, `internal/web/static/*` is embedded into the binary via `go:embed` (compile-time snapshot).

- **After editing frontend static files** (HTML/CSS/JS/SVG under `internal/web/static/`): you must **rebuild and restart** for changes to appear in the browser.

```bash
make build
cc-otel restart
```

- **Skip rebuilding during local development (optional)**: set `CC_OTEL_STATIC_DIR=internal/web/static` and the web UI will read static files directly from disk. After editing, usually just refresh the page.

### Change verification checklist (mandatory for Agent)

When modifying code that **affects the web UI** (especially `internal/web/static/`, chart/table field semantics), the Agent must complete the following steps before reporting back to the user. Do NOT tell the user to "refresh and check yourself".

1. **Rebuild and restart** (use `./bin/` for development, `~/.claude/cc-otel/` for production):

```bash
make build
./bin/cc-otel.exe stop
./bin/cc-otel.exe start
```

2. **Agent self-verification of UI/data (must do it yourself)**: Open `http://localhost:8899/?v=...` (with cache-busting param) via browser automation. Switch between **Yesterday / 7 Days** and other ranges with data. Confirm that the chart and **Daily Detail** table use **consistent definitions** for the same model and date (e.g., **Input** = input-side total, **Cache Read** as a separate column; tooltip matches table headers). If no browser tool is available, at least use `curl` to request `/api/dashboard` and `/api/daily` and verify the returned JSON against the display logic.

3. **Screenshot evidence**: Take at least one screenshot of the current page (including the date range). **No screenshot = verification not complete.**

## Web UI Status and Debugging

### Top-right `live` indicator (clickable status panel)

The green dot + `live` in the top-right corner indicates the **Web UI SSE push stream** (`/api/events`) is connected.

Clicking `live` opens the **Server Status** panel showing:

- **UI Stream (SSE)**: connection status and current SSE client count
- **DB / API**: database connectivity (backend pings the DB)
- **OTLP gRPC Receiver**: whether the OTLP gRPC port is listening (default `:4317`, checked via TCP dial)
- **Last Update**: the most recent time new OTEL data was written and triggered a UI refresh
- **Endpoints**: `OTLP gRPC` / `Web UI` addresses (with copy buttons)

Backend endpoint: `GET /api/status`

### Serving static files from disk (development option)

By default, `internal/web/static/*` is embedded into the binary via `go:embed` (compile-time snapshot).
If you frequently edit HTML/CSS/JS locally and don't want to `go build` each time, set:

`CC_OTEL_STATIC_DIR=internal/web/static`

The web UI will then read static files directly from the disk directory (only for local development).

## Ports

| Port | Service |
|------|---------|
| 4317 | OTLP gRPC Receiver |
| 8899 | Web UI + REST API |

## Frontend Development Rules

- **Chart data granularity: time + model is the minimum**. Each (date, model) pair gets an independent bar.
  - **`stack: 'total'` is forbidden** - no stacked bar charts
  - **`trigger: 'axis'` is forbidden** - tooltip must use `trigger: 'item'`; hovering a bar shows only its own info (date + model + value), not all models for that day
  - **`emphasis: { focus: 'series' }` is forbidden** - no highlighting the entire series on hover
- These three rules come from past mistakes and must never be violated. Always confirm all three before touching chart code.

## Web UI Data and Display Rules (pinned)

The following are the conventions for the **current correct version**. Changing semantics without user confirmation is **forbidden** (e.g., making bar height exclude `cache_creation`, flattening the tooltip, splitting the gradient into three segments for uncached/cache/output, or changing Daily table header grouping).

### Definitions: Input Tokens / Input Side (pinned)

- **Input Tokens (aggregate definition)** = **Uncached** + **Cache-related**. Specifically: `input_tokens` (uncached) + `cache_read_tokens` (cache read) + `cache_creation_tokens` (cache write/creation). **All three are required**; do not interpret "Input" as "only the `input_tokens` column".
- The table splits into **Uncached | Cache Read | Cache Create** columns for **breakdown display**; the sum of all three columns is the "input-side total". The top-bar KPI **Input**, `/api/dashboard`'s `total_input_tokens`, and the **Input** parent row in chart tooltips **all use this sum**.
- **Output** (`output_tokens`) is NOT part of "Input Tokens"; the common total token breakdown is **input-side total + Output**.
- **External reference (Anthropic)**: The official [Prompt caching](https://docs.anthropic.com/en/docs/build-with-claude/prompt-caching) docs state under *Tracking cache performance*: `input_tokens` only represents the non-cached portion after breakpoints; **total input tokens** should be `total_input_tokens = cache_read_input_tokens + cache_creation_input_tokens + input_tokens` (fields map 1:1 to the above). Therefore **Cache Create (cache creation) belongs to input-side statistics**, not "an extra unrelated item".

### Terms and Backend Fields

| Concept | Meaning |
|---------|---------|
| **Uncached** (`input_tokens`) | Only uncached input. The Daily table **Uncached** column shows only this. |
| **Cache Read / Cache Create** | `cache_read_tokens` / `cache_creation_tokens`. |
| **Input-side total `inputSide`** | `input_tokens + cache_read_tokens + cache_creation_tokens`. |
| **Top-bar KPI "Input"** | `GET /api/dashboard`'s `total_input_tokens` = **`SUM(input_tokens + cache_read_tokens + cache_creation_tokens)`**, same definition as the chart tooltip **Input** parent row and the **dark segment (entire input side)** of Token bars. |
| **Output** | `output_tokens`. |

Note: the **Uncached** column in the table is only a component of `inputSide`; **do NOT change the top-bar Input to only sum `input_tokens`**.

### Token Bar Chart (`chartMetric === 'tokens'`)

- **Granularity**: one bar per **(date x model)**; ECharts `stack: 'total'` multi-series stacking is **forbidden**.
- **Bar height**: `inputSide + output_tokens` (consistent with **Total** below).
- **Gradient (single series, two vertical segments)**: the bar body is **one** series; **bottom dark segment** = entire input side (sum of uncached + cache read + cache create); **top light segment** = Output proportion. When Output is tiny relative to total, the light segment uses **at least ~6%** of bar height (`minVis`) so it remains visible; tooltip still shows exact values.
- **Forbidden**: splitting the input side into multiple gradient segments (e.g., a third segment for uncached only), or using axis-triggered merged tooltip (must still use `trigger: 'item'`).

### Chart Tooltip (`trigger: 'item'`)

Display order and hierarchy (**must be preserved**):

1. **Total** = `inputSide + output_tokens`
2. **Input** (parent row, bold) = `inputSide`
3. Child rows (indented, smaller font): **Uncached**, **Cache Read**, **Cache Create**
4. **Output**
5. **Requests**, **Cost**

### Daily Detail Table

- Table header is **two rows**: first row has **Input** with `colspan=3` spanning three columns; second row has **Uncached | Cache Read | Cache Create**; **Output**, **Cost**, etc. use `rowspan=2`.
- Column order in rows matches the header; empty data rows use `colspan="8"`.

### Date Range Picker

- Custom range uses **Flatpickr** (CDN CSS/JS included in `internal/web/static/index.html`, initialized via `initCustomRangePicker` in `app.js`): **range** mode, `disableMobile: true`, dual-month on wide screens; do not replace with native dual `<input type="date">` unless the user requests it.

### Agent UI verification on changes

When changes touch any of the above rules, in addition to the standard "build -> restart -> API -> browser `?v=` -> screenshot" flow, verify in the browser: **hover a bar in Token mode**, confirm **Total = Input (parent row) + Output**, and **Input (parent row) = sum of three child rows**.
