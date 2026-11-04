# Hotspots — where correctness is most fragile

Files where a small change silently corrupts money, attribution, or boot.
Review hunks here at double depth; verify the listed invariants explicitly.

## HS-01 internal/db/db.go — schema, views, pragmas

Invariants:
- Pool views (`claude_pool_requests` / `claude_pool_agg`) mirror the FULL unified
  pool, no service filter; they are DROP+CREATE every boot (name kept for compat).
- Column additions only via the idempotent `ensureColumn` list — never raw ALTER
  in a migration path that can run twice.
- Per-connection pragmas ride the DSN (dsn.go), not one-shot `db.Exec` on pool open.
- Boot maintenance must obey DP-01 (`PRAGMA optimize`, not `ANALYZE`).

## HS-02 internal/receiver/codex_parser.go — the ingestion state machine

Invariants:
- `codexSpanTracker` bounded (10000 entries, eviction), with (sessionID, model)
  fallback for missing span ids — safe only because requests are serial per conversation.
- Every `case` branch in dispatchCodexLog states its return contract; new event kinds
  must be archived (unknown-event table), never dropped silently.
- Dual `response.completed` gating per DP-07, both arrival orders tested.
- TTFT writer precedence per DP-11; failed rows (`error_message != ''`) receive
  no TTFT from any path.

## HS-03 internal/db/codex_repository.go — fill UPDATEs

Invariants:
- `UpdateCodexRequestTTFT` / `UpdateCodexTurnTTFT` are fill-only and exclude failed
  rows; completion finalize is the only overwrite path.
- All long-running UPDATEs go through `execWithBusyRetry` (WAL contention).

## HS-04 Pricing predicate — three call sites must agree

`pricing.IsUpstreamAuthoritative(service_name, model)` is applied at THREE sites:
receiver `Export`, the Codex sse path, and `internal/recompute` backfill.
A change at one site only = live rows and backfilled rows disagree forever.
Grep all three on any pricing diff.

## HS-05 internal/dbmerge/ + cmd/cc-otel/autoimport.go — migration engine

Invariants:
- `CodexToAllPlans()` is the single mapping source for both autoimport and the
  manual button — never fork the list.
- Autoimport runs BEFORE servers bind (single-writer row-id ranges); failure writes
  no marker (next boot retries); purge deletes only recorded ranges; the frozen
  archive is never mutated.
- The global production DB (~/.cc-otel) is never written by any dev tool (HS-07).

## HS-06 internal/web/static/app.js — boot sequence

`initX(...)` factory calls around lines 296-320 must all run before
`applyStateFromURL(); loadModelFilter(); loadAll(); initSSE(...)`. An edit here
ate four calls once (DP-08). After touching boot, `node --check` + full web suite
are mandatory.

## HS-07 Dual-instance topology — ports and write isolation

- Production `~/.cc-otel` (4317/4318/8899): never stopped for a copy (iron rule
  `.claude/rules/db-copy-no-stop.md`); binary swap per DP-02.
- Dev `bin/` (14317/14318/18899): the only instance tests may write to.
- Rehearsal instances: `serve` on 25xxx ports (DP-09), temp config, cleaned up after.
Any change that blurs which instance a tool targets is a P0.

## HS-08 Unified-view semantics (user-pinned)

- ONE dashboard for every client; no source tabs; `?source=` silently rewritten away.
- `/api/codex/*` mirrors are shape-identical to base endpoints and filtered to
  service_name='codex' — external scripts only, the UI never routes to them.
- `service_name` is an attribution label, not a partition; missing names normalize
  to 'claude-code' at every entry (receiver, backfill, agg rebuild, dbmerge).
