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

