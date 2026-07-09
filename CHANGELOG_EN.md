## Changelog

[中文版本](./CHANGELOG.md)

This project follows a lightweight changelog format (Keep a Changelog inspired), optimized for a small, fast-moving tool.

---

## [Unreleased]

> Note: No official release has been published yet. This section describes what exists on the main branch today.

### Features

- **OTLP gRPC receiver**: ingest Claude Code telemetry over OTLP/gRPC (logs, metrics, traces).
- **TTFT (Time To First Token)**:
  - Receive `ttft_ms` from OTLP trace spans.
  - Backfill `api_requests.ttft_ms` so TTFT is queryable and visible in the UI.
  - Add a pending queue to handle out-of-order delivery (trace arrives before `api_request` log).
- **Request Log enhancements**:
  - Per-model summary table: **Avg Duration**, **Out tok/s**, **Avg TTFT** (only shown when data exists), **Min**, **Max**.
  - Sortable columns for the summary table.
  - Per-request list includes **TTFT** column.
  - Tooltips: TTFT column header + per-cell TTFT detail on hover.
- **Latency metrics**:
  - `/api/durations` endpoint for per-model duration/throughput stats.
  - Output throughput: **Out tok/s** (derived from output tokens and duration).
- **Live updates**: UI auto-refresh via SSE (`/api/events`) when new telemetry is ingested.
- **Raw OTLP backups**: persist raw OTLP payload snapshots (`raw_otlp_events`) for debugging/forensics.
- **Local storage**: SQLite stores request-level facts and aggregates for fast UI queries.
- **Theme & date ranges**: dark/light theme; Today/7 Days/30 Days/All Time/custom date range picker.

### Notes

- TTFT requires Claude Code trace export to be enabled (e.g. `OTEL_TRACES_EXPORTER=otlp`) and tracing to be turned on (Enhanced Telemetry Beta gate/override).

---

## [0.1.0] - TBD

> Planned: the first public release will snapshot the current “Unreleased” features into `0.1.0`, plus packaging and upgrade notes.

