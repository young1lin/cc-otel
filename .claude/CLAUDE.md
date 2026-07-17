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

## Frontend Taste (Mandatory)

Before any frontend design, implementation, modification, or visual review,
read `.claude/harness/FRONTEND_TASTE.md` completely. This file is the
authoritative taste guide for this project's frontend and its instructions are
mandatory for every UI change.

Functional and data-semantic rules pinned in this `CLAUDE.md` remain
authoritative. `FRONTEND_TASTE.md` governs visual hierarchy, color, spacing,
controls, interaction, motion, accessibility, and the standard of finish.

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

### Frontend module layout (ESM, no build tools)

`internal/web/static/app.js` is a thin entry module (~230 lines) that imports
focused ES modules under `internal/web/static/js/`. The page loads them via
`<script type="module" src="app.js?v=NN">`; vendor scripts (ECharts, Flatpickr)
load before it as classic globals.

| Module | Responsibility |
|---|---|
| `state.js` | Central mutable state object + paging counters. Cross-module reads/writes go through `state.*`. |
| `utils.js` | Pure formatters: fmtNum / fmtUSD / fmtTime / fmtHourRange / escapeHtml / toYMD / rangeToFromTo, etc. |
| `theme.js` | Theme toggle, palette, per-model branded colors (Claude / GLM / GPT / etc.). |
| `api.js` | Thin wrappers around every `/api/*` endpoint; the only place `fetch()` is called. |
| `filters.js` | Date range tabs, day-dropdown, Flatpickr, range/metric/granularity/panel button listeners, URL ↔ state sync. |
| `sse.js` | SSE live-stream + status modal. |
| `breakdown.js` | KPI breakdown modal (cost / input / output / cache hit / requests pie). |
| `insights.js` | Insights bar + modal. |
| `chart-main.js` | Main bar chart + `buildBarTooltip` (shared with hourly chart). |
| `panel-daily.js` | Daily by-day table + by-hour hourly chart. |
| `panel-sessions.js` | Sessions table. |
| `panel-requests.js` | Request log + duration stats sort. |
| `pagination.js` | Pagination renderer shared by panels. |

Tests live under `internal/web/static/tests/*.test.mjs` and import directly from
the modules (`import { fmtNum } from '../js/utils.js';`). Run with
`node --test internal/web/static/tests/*.test.mjs` (Node ≥ 18 built-in).

When extracting more code or adding a new view, follow the same pattern:
- Pure helpers go in `utils.js` / `theme.js` and get a `*.test.mjs` companion.
- DOM-bound modules expose an `initX({ openPopover, closePopover, ...callbacks })`
  factory; cross-module dependencies are injected by `app.js` at boot.
- Never `import` ECharts or Flatpickr — read them from `window.echarts` / `window.flatpickr`.

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
| 4317 | OTLP gRPC Receiver (production) |
| 8899 | Web UI + REST API (production) |

### Development / Testing Ports (bin/ directory)

**NEVER use production ports (4317 / 8899) for testing.** The production instance at `~/.claude/cc-otel/` must stay running. Always use separate ports for the bin development instance:

| Port | Service |
|------|---------|
| 14317 | OTLP gRPC Receiver (development) |
| 18899 | Web UI + REST API (development) |

To start the dev instance with separate ports, create a temp config:
```bash
cat > bin/cc-otel-dev.yaml << 'EOF'
otel_port: 14317
web_port: 18899
db_path: ./bin/cc-otel-dev.db
model_mapping: {}
EOF

./bin/cc-otel.exe start -config bin/cc-otel-dev.yaml
```

When sending Codex test telemetry to the dev instance, use `http://localhost:14317` as the OTLP endpoint.

All build artifacts (`make build`) go to `./bin/` — binary, config, database, logs. Never write test data to the production directory `~/.claude/cc-otel/`.

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

### Source routing (Claude Code vs Codex)

cc-otel reads OTLP Resource attribute `service.name` to decide which storage path applies. Names containing `codex` (case-insensitive) go to `codex_*` tables and `/api/codex/*` routes; everything else (including missing service.name) goes to the existing Claude tables for back-compat. Frontend tabs key on `state.source` (`claude` default, `codex` via `?source=codex` URL param).

### Pricing (non-Claude cost recompute)

The `internal/pricing` package owns per-model `cost_usd` recompute. Single rule (case-insensitive on the trimmed model id):

```
if strings.HasPrefix(model, "claude-") → keep upstream cost_usd untouched
else                                    → cost_usd = pricing.Calc(...)
```

This applies to both the Claude log path (`internal/receiver/receiver.go` `Export`) and the Codex sse_event token-update path (`internal/receiver/codex_parser.go`). It corrects two real-world bugs: Codex never reports `cost_usd`, and GLM/DeepSeek/Kimi via Anthropic-compatible reverse proxies are priced by Anthropic (not by their own provider).

**Two-layer lookup priority** (highest first):

1. `cfg.Pricing` (user `pricing:` block in `cc-otel.yaml`) — in-memory, never persisted to SQLite.
2. `model_pricing` table — canonical store, seeded from `internal/pricing/embed/seed.json` on first boot. Edited via the Web UI (Server Status → Pricing Table): writes go straight to the table and reload immediately.
3. Empty result → registry returns `Found=false`. The receiver leaves the upstream-reported cost in place rather than zeroing it.

There is **no automatic network refresh**. To price a model: open the Pricing
Table modal → add/edit the row, or click 💡 to prefill from OpenRouter on
demand, then 保存. To backfill historical rows after a price change: click
↻ 重算历史 (server-side full recompute; status-tracked, survives page refresh).

The seed JSON is generated by `tools/dump_pricing_snapshot` from BerriAI/litellm — re-run it on release to refresh the offline fallback. **Claude entries are intentionally absent from the seed**; the registry is meant to miss for Claude and the receiver short-circuits before consulting it.

**Operations:**

- Inspect a model: `GET /api/pricing/lookup?model=glm-4.6` (debug) or the Pricing Table modal.
- Suggest a price from OpenRouter: `GET /api/pricing/suggest?model=glm-4.6`.
- Backfill historical rows: ↻ 重算历史 in the modal, or `go run ./tools/recompute_cost --db <path> --table both [--apply]`. Defaults to dry-run.
- Edit/add/delete: the Pricing Table modal, or `pricing:` YAML (restart-required).

When touching pricing code, the standard build → restart → API → browser flow still applies; additionally hit `/api/pricing?q=<test>`, `/api/pricing/suggest?model=<test>`, and `GET /api/pricing/recompute` to exercise the CRUD/suggest/recompute paths, and `/api/status` to confirm the popup data is populated.

### Merging another DB into bin + recomputing cost

When the user asks to "合并 A 和 B 数据库 + 清洗" (merge two databases and clean costs) — typically pulling `~/.claude/cc-otel/cc-otel.db` into `bin/cc-otel.db` then recomputing — **follow `docs/MERGE_AND_RECOMPUTE.md`**, do not improvise.

The standard sequence is:

```bash
go run ./tools/merge_bin_global/run_merge -yes                                            # merge
go run ./tools/recompute_cost --db bin/cc-otel.db --config bin/cc-otel.yaml --table both --apply  # clean
```

**Load-bearing details, easy to get wrong:**

- `--config bin/cc-otel.yaml` is **mandatory** for `recompute_cost`. Without it, user `pricing:` overrides (the only source of truth for models LiteLLM hasn't catalogued — glm-5.x, deepseek-v4-*, mimo-v2.5-pro, step-3.5-flash, etc.) are skipped and those rows keep their wrong upstream cost.
- `run_merge` does **not** recompute. Merge and recompute are two separate steps.
- `run_merge` direction is fixed: `~/.claude/cc-otel` → `bin/`. Result lands in `bin/cc-otel.db`.
- Both tools auto-create timestamped backups in `bin/`. The merge tool also auto-stops the bin daemon (PID-checked against `bin/cc-otel.exe` to avoid killing unrelated processes).
- For interactive verification after a clean: `go run bin/tmp/verify.go` prints the per-model cost rollup.

### Iron rule: copying the DB must not stop a running process

Copying / snapshotting `cc-otel.db` **must never stop the running daemon — the global instance at `~/.claude/cc-otel/` in particular is never stopped just to copy its DB.** The DB is WAL-mode, so a plain `cp` of the `.db` alone is inconsistent. Use the online snapshot `VACUUM INTO` (`go run ./tools/snapshot_db <src> <dst>`), which only read-locks the source. Only the **dev/bin daemon** (ports 14317/18899) may be stopped — and only because Windows can't replace a file it holds open. Full procedure: **`.claude/rules/db-copy-no-stop.md`**.
