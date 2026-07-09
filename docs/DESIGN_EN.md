## Design Notes (Why / How to change safely)

[中文版本](./DESIGN.md)

This document focuses on:

- **What cc-otel is** and what it intentionally does *not* do.
- **Why the architecture looks like this** (single binary, SQLite, pre-aggregation, SSE, TTFT backfill).
- **Where to extend** when you add fields/metrics or debug missing data.

---

## 1. Goals and Non-goals

### Goals

- **Zero external dependencies**: no Grafana/Prometheus/Collector required; single binary runs locally.
- **Claude Code optimized**: treat `api_request` as the primary fact record.
- **Traceability**: keep raw OTLP snapshots (`raw_otlp_events`) to survive field drift.
- **Fast UX**: live refresh + fast range switching even with large local DBs.

### Non-goals

- A generic OTLP backend that supports every OTLP semantic and protocol variant.
- Multi-tenant auth and remote production deployment concerns (current default is localhost).
- Perfect 1:1 correlation between every span and every request (we do best-effort backfill).

---

## 2. Data flow: Claude Code → cc-otel → UI

Two primary sources:

1. **Logs / Events (primary facts)**: `claude_code.api_request` carries token/cost/duration → `api_requests`.
2. **Traces (supplemental)**: TTFT is often only present on span attributes → TraceService → backfill into `api_requests.ttft_ms`.

UI reads:

- Dashboard/Daily/Sessions prefer aggregates for speed.
- Request Log uses `/api/durations` (per-model aggregates) + `/api/requests` (request list).
- Live refresh uses SSE (`/api/events`) triggered after successful ingestion.

Event schema reference: `docs/otel-events.md`

---

## 3. Storage model (Why SQLite + pre-aggregation)

### Why SQLite

- Single-file DB + WAL mode is stable and perfect for local tooling.
- Easy to copy/backup.
- With indexes + selective pre-aggregation, query latency stays low.

### Why `daily_model_agg`

`api_requests` grows quickly. Recomputing charts from raw requests on every UI refresh will slow down over time.

So we upsert `daily_model_agg` in the same transaction as inserting `api_requests`:

- Charts and daily tables read from `daily_model_agg` (rows ~ days×models).
- Detail views read `api_requests` with timestamp/model/session indexes.

---

## 4. TTFT design (Why traces + backfill)

### What TTFT is

- **TTFT** = Time To First Token (ms)
- The latency from request start to receiving the first token

### Why not rely on `api_request` logs

Claude Code does not consistently include `ttft_ms` on `api_request` log attributes.
Trace span attributes (e.g. `claude_code.llm_request`) are a more reliable source.

### Why backfill into `api_requests`

This keeps queries and UI simple:

- Aggregates like Avg TTFT and per-request TTFT read directly from `api_requests.ttft_ms`.
- No need to join trace tables or parse raw JSON in the UI.

### Backfill strategy (layered)

1. **Strict match**: `(session_id + prompt_id + model) + nearest time`
2. **Fallback**: if span has no `prompt_id`, use `(session_id + model)` within a bounded window (±120s)
3. **Out-of-order healing**: if trace arrives before the `api_request` log row, enqueue into `pending_ttft_spans` and apply when the request is inserted

The goal is “high coverage with low risk”, not perfect correlation.

---

## 5. Web UI (Why SSE + two-part Request Log)

### Why SSE

- Minimal implementation cost; one-way push is enough.
- Native `EventSource` support.
- Avoids polling overhead.

### Why Request Log is split

Two user tasks:

- Fast model-level overview (Avg Duration / throughput / TTFT).
- Drill into individual requests (tokens/cost/duration/TTFT).

So the top table is per-model aggregates (sortable), the bottom table is a request list (extensible columns + tooltips).

---

## 6. How to change the system safely (common edits)

### A) Add a request-level field and show it in UI

1. Receiver: parse OTLP attributes in `internal/receiver/receiver.go` → fill `APIRequest`
2. DB schema: add column/index in `internal/db/db.go`
3. Repository: write the column in `InsertRequest`
4. API: ensure `/api/requests` returns it
5. UI: add `<th>` in `index.html` + render in `app.js`

### B) Add a per-model aggregate column (like Avg Duration)

1. Repository: extend `GetDurationStatsByModel` SQL
2. API: `/api/durations` returns the new field
3. UI: add `data-sort-key` header + render + sorting handler

### C) Debug “0 / missing” values

Recommended order:

1. Receiver logs: did we receive the signal? (`OTEL traces received`, logs received, etc.)
2. Raw backups: does `raw_otlp_events` contain the payload? (field drift?)
3. Backfill logs: updated / no match / missing keys
4. DB rows: does the request exist and fall within the window?

---

## 7. Design tradeoffs (the "why" for future maintainers)

- **Single binary**: easy to ship; frontend edits need a rebuild (`CC_OTEL_STATIC_DIR` bypasses it during development).
- **SQLite**: simple and reliable; concurrent writes need careful transaction boundaries and indexes (WAL + busy_timeout enabled).
- **TTFT backfill**: best-effort only; the "pending + time window" approach softens out-of-order arrivals.
- **Frontend modularity (vanilla ESM, no build tools)**: `internal/web/static/app.js` is split into a set of ES modules under `js/*.js`, loaded via the browser's native `<script type="module">`. **No** Node / Vite / bundler. `go:embed static/*` recurses, so the single-binary deployment shape is unchanged. Each module has one responsibility (state / utils / theme / api / filters / sse / breakdown / insights / chart-main / panel-* / pagination); pure helpers live in `utils.js` / `theme.js` / `insights.js` with `node --test` unit tests. Cross-module dependencies are injected via `initX({ ... })` callbacks so we avoid circular imports.

---

## 8. Pricing table & non-Claude cost recompute

### Why

Trusting upstream `cost_usd` blindly produces two known failure modes:

1. **Codex never reports cost**: `codex.api_request` and `codex.sse_event` carry no `cost_usd`, so the dashboard Cost KPI was permanently `—`.
2. **GLM/DeepSeek/Kimi via Anthropic-compatible reverse proxies**: Claude Code prices the call against Anthropic's table, so the recorded cost reflects Sonnet/Opus prices applied to GLM tokens — wildly inflated.

### Single rule

```
if strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "claude-")
    keep e.CostUSD as upstream-reported  // Claude is Anthropic's source of truth
else
    e.CostUSD = registry.Calc(...)       // every other model is recomputed locally
```

Derived effects: Codex (`gpt-5-codex`) is always recomputed; GLM via proxy is always recomputed; native Anthropic Claude is untouched. No `cost_mode` config exists — the rule is a one-liner.

### Three-layer lookup priority

```
1. cfg.Pricing (user override in cc-otel.yaml)   → in-memory, loaded at startup, highest
2. model_pricing table                           → SQLite, persistent across restarts
3. Daily refresh (LiteLLM + OpenRouter)          → 24h tick, diff-only writes
```

The query path only consults layers 1+2; remote sources contribute exclusively at write time, so runtime traffic never blocks on the network.

### Initialisation

`pricing.NewRegistry` calls `seedIfEmpty`. If `model_pricing` is empty, it bulk-inserts ~669 entries from `internal/pricing/embed/seed.json`, generated offline by `tools/dump_pricing_snapshot` from BerriAI/litellm. Anthropic / Claude entries are intentionally absent — registry lookups for Claude are designed to miss, and the receiver short-circuits before consulting it via `IsClaudeModel`.

### Refresher (diff writes)

`internal/pricing/refresher.go` runs a goroutine that ticks immediately and then every 24h:

1. Fetch all Sources concurrently (LiteLLM priority 10, OpenRouter priority 5).
2. Higher priority wins for shared model ids.
3. Hash each fetched entry (`fmt.Sprintf("%.12g|%.12g|%.12g|%.12g", input, output, cacheRead, cacheCreate)`) and compare with the current SQLite snapshot.
4. Only write rows whose hash changed; unchanged rows skip the UPDATE entirely so `updated_at` doesn't flap.
5. If every source fails the previous snapshot is preserved; partial failures degrade status to `partial` and surface via `/api/status`.

After applying changes the refresher calls `Reloader.Reload(ctx)` so the in-memory registry sees the new values, and `SetRefreshStatus(...)` so the popup shows the latest result.

### Model-name matching (`match.go`)

Resolving short names like `glm-4.6` to canonical entries like `openrouter/z-ai/glm-4.6` works through a four-step cascade:

1. Exact match.
2. Strip date suffix (`-20251028`) and tags (`-preview` / `-latest` / etc.), then exact again.
3. Alias reverse lookup (the seed populates the `aliases` column).
4. Longest-prefix match with a `-` / `.` / `:` boundary so `gpt-5` doesn't capture `gpt-50`.

### History backfill

`tools/recompute_cost` defaults to dry-run; `--apply` writes:

1. Build the same `pricing.Registry`.
2. SELECT rows in the date range, skipping Claude rows and rows without token signal.
3. Recompute and compare against the stored `cost_usd`; queue only differing rows.
4. After UPDATEs, DELETE + INSERT to rebuild `daily_model_agg` / `codex_daily_model_agg` from the corrected base rows.

