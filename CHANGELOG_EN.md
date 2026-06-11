## Changelog

[中文版本](./CHANGELOG.md)

This project follows a lightweight changelog format (Keep a Changelog inspired), optimized for a small, fast-moving tool.

---

## [Unreleased] · 0.1.0 Preview

> **Status: preview, not yet released.** This section describes the full set
> of features on the main branch today. Iterations ship as `v0.1.0-preview.N`
> tags via the GoReleaser pipeline (latest: `v0.1.0-preview.10`). Once behavior
> stabilizes, the contents below will fold into `v0.1.0`.

### Proxy compatibility fix

- **Auto-inject `no_proxy`**: `/cc-otel:setup` now automatically adds `"no_proxy": "localhost,127.0.0.1"` to `settings.json` `env`. When `http_proxy` / `https_proxy` is set (e.g. Clash, V2Ray, corporate proxies), OTEL gRPC traffic to `localhost:4317` is routed through the proxy and silently dropped. `no_proxy` ensures the OTEL exporter connects directly, bypassing the proxy. README and setup docs updated with prominent proxy warning.

### Sources & ingestion

- **OTLP gRPC receiver**: ingest logs / metrics / traces over OTLP/gRPC. Code default port is `4317` (see the Daemon / CLI section below for port details).
- **Claude Code + Codex + Gemini CLI as three sources**: routed by the OTLP Resource attribute `service.name` —
  - Names containing `codex` (case-insensitive) → `codex_*` tables, exposed via `/api/codex/*`; the frontend switches views with `?source=codex`.
  - Name equal to `gemini-cli` → `gemini_*` tables (independent schema: `thoughts_tokens` / `tool_tokens` / `total_tokens`), exposed via `/api/gemini/*`; frontend `?source=gemini`. Setup guide: `skills/otel-setup/gemini-setup.md`.
  - Everything else (including missing `service.name`) → the existing Claude tables / routes for back-compat.
- **TTFT (Time To First Token)**:
  - Extracted from OTLP trace spans and back-filled into `api_requests.ttft_ms` / `codex_api_requests.ttft_ms`.
  - Built-in pending queue handles out-of-order delivery (trace before `api_request` log).
  - Historical backfill: `tools/backfill_claude_ttft`.
- **Codex duration backfill** (two paths):
  - **Online** (in-receiver): when an `api_request` log lacks a matching span ID, fall back to the most recent `codex.websocket_request` span by `(sessionID, model)` within a 10-minute window and fill `duration_ms` at ingest time.
  - **Offline**: `tools/backfill_codex_duration` scans the `codex_raw_otlp_events` table for paired `codex.websocket_request` / `codex.websocket_event` spans and writes the derived `duration_ms` back into `codex_api_requests`.
- **Live push**: `/api/events` SSE — `Broker.Notify()` fires after each successful insert, all open browsers auto-refresh.

### Pricing & cost recompute (`internal/pricing`)

- **Cost recompute for non-Claude models**, single rule:
  - `model` starts with `claude-` (case-insensitive) → keep the upstream `cost_usd`.
  - Otherwise → recompute from local price table using token counts and overwrite.
- **Three-layer lookup priority**:
  1. `cc-otel.yaml` `pricing:` user overrides (highest).
  2. SQLite `model_pricing` table (durable store).
  3. Embedded `internal/pricing/embed/seed.json` (derived from BerriAI/litellm).
- **Daily price-table refresh**: diff writes from LiteLLM + OpenRouter into `model_pricing` (see `internal/pricing/refresher.go`); can be disabled via `pricing_refresh.enabled: false`.
- **Two real-world bugs fixed**: Codex never reports `cost_usd`; GLM/DeepSeek/Kimi behind Anthropic-compatible reverse proxies were priced as Anthropic.
- **Ops endpoint**: `GET /api/pricing/lookup?model=glm-4.6` returns matched key, source, prices, and `is_claude`.
- **Status panel**: top-right `live` → **Pricing Table** row colored green (<48h), yellow (<7d), or red (>7d / error) by last refresh.
- **Historical recompute**: `go run ./tools/recompute_cost --db <path> [--config <yaml>] --table both [--apply]` (defaults to dry-run).

### Web UI · data presentation

- **KPI cards**: Cost / Input / Output / Cache Hit / Requests. **Input = `input_tokens` + `cache_read_tokens` + `cache_creation_tokens`** (matches Anthropic's official prompt-caching docs).
- **KPI breakdown modal**: click any KPI card for a per-model pie.
- **Main bar chart (Tokens / Cost / Requests)**:
  - One bar per `(date × model)` (no stacking, no series-focus emphasis, no axis-triggered tooltip).
  - The Tokens bar is a single series with a two-segment vertical gradient: bottom = input-side total, top = Output; when Output is tiny, the light segment is still ~6% tall (`minVis`) so it remains visible.
  - Tooltip hierarchy: **Total** → **Input** (bold parent) → Uncached / Cache Read / Cache Create (indented children) → **Output** → Requests / Cost.
- **Daily Detail table**: two-row header; Input spans 3 columns (Uncached / Cache Read / Cache Create), other columns use `rowspan=2`.
- **Usage heatmap calendar** (new): the Insights bar is now a GitHub-style daily heatmap supporting tokens / cost / requests; clicking a cell jumps the dashboard to that single day. Backed by `/api/calendar` and `/api/codex/calendar`.
- **Intraday line chart**: 1-day ranges render a 30-minute, per-model line chart (replacing the bar chart), capped at 7 days; hover-on-segment tooltip.
- **Sessions panel**: per-session Cost / Token aggregates with pagination.
- **Request Log panel**:
  - Per-model summary table: **Avg Duration**, **Out tok/s**, **Avg TTFT** (only when data exists), **Min**, **Max**; sortable columns.
  - Per-request list with a **TTFT** column and hover detail; paginated.
- **Latency API**: `/api/durations` returns per-model duration / throughput; **Out tok/s** is derived from `output_tokens` and duration.
- **Uniform 24-hour timestamps** (preview.10 fix): new `fmtDate24` / `fmtDateTime` replace `toLocaleString()`; midnight no longer renders as the 12-hour "12:xx AM" — everything is local-time `YYYY-MM-DD HH:mm:ss`.

### Web UI · interaction & controls

- **Date ranges**: Today / Yesterday / 7 Days / 30 Days / All Time / custom (Flatpickr dual-month, local-tz parsing, DST-safe).
- **Day dropdown**: quick switch among the last 7 days (Today / Yesterday / weekday labels).
- **Granularity switch (All Time only)**: day / month.
- **Metric switch**: Tokens / Cost / Requests.
- **Source tabs**: Claude / Codex top-level (URL `?source=codex`).
- **Theme**: dark / light, auto-following system; Flatpickr redraws on theme change.
- **Browser history navigation** (new): range buttons, source tabs, day-dropdown, custom range picker, and calendar cell clicks all use `pushState`; Back / Forward work as expected. Boot and `popstate` canonicalize the URL with `replaceState` so they don't pollute history.
- **Cross-day auto-refresh** (new): when the local day rolls over and the current view is Today / single-day pinned to today, customFrom/customTo are bumped to the new date, the URL is refreshed (`replace`), and the data is reloaded.
- **Pagination**: shared component used by Daily / Sessions / Requests panels.

### Web UI · status & ops

- **Top-right `live` indicator**: green dot = SSE push connection healthy.
- **Server Status modal**: SSE client count, DB / API health, OTLP gRPC listening state (TCP-dial probed), last-update timestamp, OTLP gRPC + Web UI endpoints (with copy buttons), Pricing Table freshness row.
- **`/api/status`**: backend endpoint surfacing all of the above as JSON.

### Web UI · architecture

- **Modular ESM frontend, no build tools**: `app.js` is now a ~230-line thin entry; the rest is split into focused modules under `internal/web/static/js/`:
  - `state.js` / `utils.js` / `theme.js` / `api.js` / `filters.js` / `sse.js`
  - `breakdown.js` / `insights.js` / `chart-main.js`
  - `panel-daily.js` / `panel-sessions.js` / `panel-requests.js` / `pagination.js`
- **Pure-function unit tests**: `internal/web/static/tests/*.test.mjs`, run via the built-in `node --test` runner (Node ≥ 18, zero deps).
- **Dev mode without rebuild**: set `CC_OTEL_STATIC_DIR=internal/web/static` and the web UI reads static files from disk; the default is `go:embed`-bundled.
- **Hard chart rules** (never violated): `stack: 'total'`, `trigger: 'axis'`, and `emphasis: { focus: 'series' }` are all forbidden.

### Storage & lifecycle

- **Storage slim-down (write path)**: `raw_otlp_events` / `codex_raw_otlp_events` / `otel_metric_points` tables are still in the schema for back-compat with existing backfill tools, but no new rows are written. The DB stays bounded under continuous use.
- **Live pre-aggregates**: `daily_model_agg` / `codex_daily_model_agg` are maintained on the insert path; Web UI queries stay < 3 ms.
- **Two-tier TTL cleanup**:
  - `raw_ttl_days` (new; default 5 days) — hourly sweeper for `*_raw_otlp_events` and stale codex websocket-event rows.
  - `retention_days` (existing) — overall retention threshold honored by `cc-otel cleanup` and the periodic prune.
- **SQLite**: WAL mode + `busy_timeout`, single-file deployment.

### Daemon / CLI

- **Subcommands**: `start` (background) / `stop` / `restart` (hot-swap binary) / `status` (PID + ports + today's stats) / `serve` (foreground) / `install` (copy binary to `~/.claude/cc-otel/`) / `init` (write default config) / `cleanup` / `-v` / `-config <path>`.
- **Data directory resolution**: if the executable is in a directory named `bin` (dev mode) → use that `bin/`; otherwise use `~/.claude/cc-otel/` (auto-mkdir); final fallback is `.`. `~/.claude/` itself is not an intermediate lookup step.
- **Env-var overrides**: `CC_OTEL_OTEL_PORT` / `CC_OTEL_WEB_PORT` / `CC_OTEL_DB_PATH` / `CC_OTEL_STATIC_DIR`.
- **Co-located files**: `cc-otel(.exe)` + `cc-otel.yaml` + `cc-otel.db` + `cc-otel.pid` + `cc-otel.log` in the same directory.
- **Default ports**: `otel_port = 4317`, `web_port = 8899` (`DefaultOTELPort` / `DefaultWebPort` constants in code). This repo's `bin/cc-otel.yaml` switches the dev instance to `14317 / 18899` to avoid clashing with a production instance, but that's a YAML override, not a code default.

### Claude Code plugin

Ships as a marketplace plugin with slash commands:

| Command | Description |
|---------|-------------|
| `/cc-otel:setup` | Download binary, configure OTEL env vars, start service |
| `/cc-otel:start` | Start the background daemon |
| `/cc-otel:stop` | Stop the daemon |
| `/cc-otel:status` | Status + today's cost summary |
| `/cc-otel:open` | Open the Web dashboard in a browser |
| `/cc-otel:report [today\|7d\|30d\|all]` | Generate a cost report |

### Cross-machine data & ops tooling

- **Cross-machine DB merge**: `tools/merge_bin_global/run_merge` orchestrates 9 steps: backup-bin-db-files → stop-bin-process → snapshot-local-db → snapshot-global-db → export-local-jsonl (`export_bin`) → import-jsonl-into-global-copy (`import_global`) → repair-daily-agg (`repair_daily_agg`) → verify-merged-copy (`verify_merge`) → replace-bin-db. `verify_union` is a related but standalone manual-verification tool — **not invoked by run_merge**. Direction is fixed: `~/.claude/cc-otel/` → `bin/cc-otel.db`. Auto-backups and PID-checked safe stop of the bin daemon are included.
- **Historical backfill tools**: `tools/recompute_cost` (rewrite `cost_usd` per table) / `tools/backfill_claude_ttft` / `tools/backfill_codex_duration` / `tools/prune_before` (date-bounded prune) / `tools/migrate_codex_data`.
- **Pricing snapshot tool**: `tools/dump_pricing_snapshot` regenerates `seed.json` from BerriAI/litellm before a release.
- **Process docs**: `docs/MERGE_AND_RECOMPUTE.md` — the canonical merge + recompute flow with load-bearing details (e.g. `--config` is mandatory).
- **Merge data-loss fix** (preview.10): `import_global` used to skip a row on a ledger UUID hit without checking the actual table — stale ledger entries (rows pruned after an earlier merge) silently dropped data. The ledger now only records; existence is decided by a natural key (`request_id` for api_requests; codex/gemini keys exclude `cost_usd`, which recompute rewrites). `verify_merge` now does per-row NOT EXISTS containment on the request tables: source-side duplicates deduped by the merge print a NOTE, cost-sum asymmetry prints a WARN, and only genuinely missing rows FAIL.
- **Online snapshot tooling**: `tools/snapshot_db` (`VACUUM INTO` copy of a live WAL db without stopping the daemon); `tools/otlp_dump` (OTLP traffic debugging).

### Distribution

- **Single binary**: web UI bundled via `go:embed`, zero runtime dependencies.
- **Cross-platform**: Windows / macOS / Linux, amd64 + arm64 (built by GoReleaser).
- **GitHub Actions**: `test.yml` runs a three-OS matrix + race + coverage upload to Codecov; `release.yml` triggers GoReleaser on `v*` tag push.

### Notes

- TTFT requires Claude Code trace export (`OTEL_TRACES_EXPORTER=otlp`) and the tracing flag enabled (Enhanced Telemetry Beta gate).
- Codex integration requires `~/.codex/config.toml` to point at the OTLP gRPC endpoint — see the README's "Codex CLI" section.
- Codex CLI doesn't report `cost_usd`; cc-otel computes it from the price table and writes it to `codex_api_requests.cost_usd`.

---

## [0.1.0] - TBD

> The first non-preview public release will snapshot the Unreleased / Preview
> content above into `0.1.0`, plus packaging / upgrade notes and release
> binaries.
