## Changelog

[中文版本](./CHANGELOG.md)

This project follows a lightweight changelog format (Keep a Changelog inspired), optimized for a small, fast-moving tool.

---

## [v0.1.0-preview.15] - 2026-07-09

> Summarizes the 4 commits after `v0.1.0-preview.14`: Gemini removal, repo hygiene, manual pricing + per-provider suggest, Token Rate control redesign.

### Removed Gemini CLI source

- Removed the full Gemini CLI integration: `gemini_*` tables and schema, `/api/gemini/*` routes, the frontend `?source=gemini` view, `skills/otel-setup/gemini-setup.md`, and its dedicated pricing.
- **Why**: real usage is concentrated on Claude Code and Codex. Maintaining a separate schema / API / docs / pricing branch for a rarely-used source cost more than it returned. The project now converges on a clean two-source model (the stale "three sources" wording still lingering in `[Unreleased]` below is superseded by this section).

### Repo hygiene

- `.gitignore` ignores tool scratch dirs like `docs/superpowers/`; `.gitattributes` enforces LF line endings.
- **Why**: keep tool-generated scratch out of git and remove line-ending churn between Windows and Linux contributors.

### Pricing: manual management + per-provider suggest (enhanced)

- **Manual management**: removed the 24h auto-refresher; Status popup → Pricing Table now CRUDs `model_pricing` (immediate effect), with 💡 on-demand OpenRouter suggest and ↻ server-side full recompute. The `pricing_refresh:` YAML key is retired.
- **Official-provider price by default**: `/api/pricing/suggest` defaults to the model's first-party official quote (e.g. `z-ai/glm-5.2` → Z.AI official, not the cheapest blended promo); expand to see every provider (uncapped, official pinned first), including `cache_create`.
- **Claude read-only reference price**: Claude models intentionally skip the local table (recompute short-circuits); they now show a read-only reference price pulled from the OpenRouter catalog on demand, so the table isn't blank.
- **Why**: the auto-refresh was opaque and uncontrollable; manual management hands pricing authority back to the user (for uncatalogued models like glm-5.x, local is the only source of truth). The default is the official price because OpenRouter's blended price is often a discounted/quantized variant from the cheapest provider — not comparable to the official list price; we give a sensible default while still letting users pick between promo and official.

### Token Rate control redesign (Apple style)

- **Controls match the app's design language**: Weighted/Avg is now a segmented control (mirrors `.pf-seg`); the time bucket is a custom rounded dropdown (native `<select>` option popups can't be rounded on Windows Chromium, so it's a trigger + `.rate-menu` popover); buttons mirror `.pf-btn`.
- **Dropped Total tok/s**: the Output/Total toggle wasn't meaningful (Total conflates cached and uncached); the chart is always Output tok/s.
- **Added a 10-min bucket**: backend `ValidRateBucketMinutes` and frontend `BUCKET_CHOICES` / dropdown item now include 10 (5 / 10 / 15 / 30 / 60).
- **Fixed segmented-control active loss**: `filters.js` `.gran-btn` queries are scoped to `#granularity-switch`, otherwise every date-range switch stripped the `.active` from Weighted/Avg (the class is shared with the toolbar Day/Month toggle).
- **Why**: the controls were unstyled native OS widgets, clashing with the polished UI, and the open dropdown was square; the custom dropdown makes both the closed and open states consistent with the app.

## [Unreleased] · 0.1.0 Preview

> **Status: preview, not yet released.** This section is the historical
> accumulation of main-branch features; iterations ship as `v0.1.0-preview.N`
> tags, latest **`v0.1.0-preview.15`** (see the top of this file). The most
> recent release notes live in the versioned section at the top; once behavior
> stabilizes, everything folds into `v0.1.0`.

### Manual pricing management

- **Manual pricing management**: removed the 24h LiteLLM/OpenRouter auto-refresher; the Web UI (Status popup → Pricing Table) now CRUDs model_pricing with immediate effect; added 💡 on-demand OpenRouter single-model suggest and ↻ server-side full recompute (status tracked, survives refresh). The `pricing_refresh:` YAML key is retired.

### Proxy compatibility fix

- **Auto-inject `no_proxy`**: `/cc-otel:setup` now automatically adds `"no_proxy": "localhost,127.0.0.1"` to `settings.json` `env`. When `http_proxy` / `https_proxy` is set (e.g. Clash, V2Ray, corporate proxies), OTEL gRPC traffic to `localhost:4317` is routed through the proxy and silently dropped. `no_proxy` ensures the OTEL exporter connects directly, bypassing the proxy. README and setup docs updated with prominent proxy warning.

### Sources & ingestion

- **OTLP gRPC receiver**: ingest logs / metrics / traces over OTLP/gRPC. Code default port is `4317` (see the Daemon / CLI section below for port details).
- **Claude Code + Codex as two sources** (Gemini CLI removed in preview.15 — see top): routed by the OTLP Resource attribute `service.name` —
  - Names containing `codex` (case-insensitive) → `codex_*` tables, exposed via `/api/codex/*`; the frontend switches views with `?source=codex`.
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
- **Bare-name basename fallback** (preview.13): a bare model name reported by a reverse proxy (e.g. `glm-5.2`) didn't match the provider-prefixed key upstream catalogs use (`z-ai/glm-5.2`). Added a 5th match strategy: fall back to the basename (segment after the last `/`). Collision tiebreak: fewest segments (direct provider) → source rank (user>litellm>openrouter>seed) → lexicographic.
- **OpenRouter first-party provider price** (preview.13): `/api/v1/models` returns the cheapest provider's blended price, frequently a lower-precision quantized variant (fp4) — not comparable to the first-party fp8 list price. For `z-ai/*` models we now also fetch `/endpoints` and override with the Z.AI first-party provider price (alnum-normalized: `Z.AI` ~ `z-ai`). glm-* no longer need manual YAML overrides.
- **Split hand-maintained manual seed** (preview.13): curated prices for models no upstream catalog carries (Xiaomi MiMo / StepFun / not-yet-listed DeepSeek V4) moved from `seed.json` to `embed/manual_seed.json`; `seed.go` merges both (manual wins on conflict) and `dump_pricing_snapshot` only rewrites `seed.json`, so manual entries survive the pre-release regen.

### Token throughput & Rate chart (preview.14)

- **Rate panel (new)**: new **Rate** tab — per-model **Token Rate over Time** line chart (Claude source; up to 7 days). Controls: **Weighted / Avg**, **Output / Total tok/s**, **5 / 15 / 30 / 60 min** buckets. Switching the date range defaults the bucket to **Today → 5 min**, **multi-day → 30 min** (dropdown still allows 5 / 15 manually).
- **Rate API**: `GET /api/rate?from=&to=&bucket=&model=` returns per-`(bucket, model)` throughput buckets. `duration_ms` is API request time only (excludes local tool execution). Each bucket exposes **weighted throughput** `SUM(tokens)×1000/SUM(duration_ms)` and **arithmetic mean** `AVG(per-request tok/s)`.
- **Session recent-rate API**: `GET /api/session/rate?session_id=` returns the latest non-empty **1-minute** bucket for that session (weighted + average Out tok/s); 404 when no data.
- **Request Log dual tok/s columns**: per-model summary adds sortable **Out tok/s (avg)** and **Out tok/s (wt)** with header tooltips documenting the formulas; same semantics as the Rate chart.
- **Rate chart UX**:
  - **Legend solo**: click a legend item to show only that model; click again or **All models** to restore all curves.
  - **Whole-line hover**: tooltip follows the cursor along the line (bucket-scoped matching avoids wrong-model tips across idle gaps).
  - **Line policy**: smooth continuous curves within a single day; **break at calendar-day boundaries** on multi-day ranges with straight intra-day segments — no overnight diagonal bridges (sparse-bucket Intraday style + Grafana-like day breaks).
- **Dev rule**: `.cursor/rules/sync-global-db-to-bin.mdc` — phrases like “sync / copy global db to bin” trigger `snapshot_db` → stop bin daemon → replace DB → restart (global daemon stays up; see `.claude/rules/db-copy-no-stop.md`).

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
- **Cache hit-rate redefinition** (preview.11 fix): the cache hit rate changed from `cache_read / (cache_read + cache_creation)` to `cache_read / input-side total` (`input_tokens + cache_read + cache_creation`). The old formula collapsed to a constant 100% for reverse-proxied providers (GLM, mimo) that report `cache_read` but never `cache_creation`; using the full input side as the denominator fixes that and aligns with the Codex / Gemini definition (`cache_read / input_tokens`, where input already includes the cached portion). Backend `GetDashboardForRange` / `GetDailyStats` and frontend `token-math.js` are updated, with a new `token-math.test.mjs` and a GLM no-`cache_creation` regression test.
- **Usage-calendar alignment fix** (preview.12 fix): in multi-week views the left-hand `Sun`–`Sat` row labels ignored the month-label header above the grid (16px + 4px margin), so they sat ~20px too high — labels fell between rows and the bottom row spilled past `Sat`. The weekday column is now offset down by the month-header height (`--usage-months-h`), aligning the 7 labels with the 7 cell rows; strip (single-day) mode is unaffected.

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
  - `panel-daily.js` / `panel-sessions.js` / `panel-requests.js` / `panel-rate.js` / `pagination.js`
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
