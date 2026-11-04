# Defect patterns — earned in this repo's own incidents

Every pattern below has a real incident behind it (commit or live event cited).
When reviewing, ask: "can this diff reintroduce DP-xx?"

## DP-01 — Boot-time full-table maintenance on a 500MB DB

Incident: boot ran `ANALYZE` on every start — 1.4s of a 535MB DB, 85-95% of boot time.
Fix was `PRAGMA optimize` (skips fresh stats, still re-analyzes after schema changes).
Rule: anything unconditional at boot (`ANALYZE`, `VACUUM`, full recompute, backfill
scans) must state its worst-case cost on the biggest realistic DB and be either
bounded, gated, or deferred. (commit 8e165bd)

## DP-02 — Windows: overwriting a running exe fails silently

Incident: production rollout copied the new binary while the old daemon still ran.
`Copy-Item -Force` reported success, but Windows refuses the write of a running
image; the daemon restarted on the OLD binary. Detected only because
`/api/status.version` came back empty (old binary had no such field).
Rule: binary swap order is **stop -> copy -> SHA256-verify -> start**, always
hash-compare source vs destination, and treat an empty `version` as "wrong binary",
not "new version without a version string".

## DP-03 — PowerShell bare-JSON-array trap

Incident: an endpoint returning a JSON array was consumed via
`$r = Invoke-RestMethod ...` then `$r.Count` — PS unrolls single-element results
(or wraps the array) unpredictably; verification scripts reported nonsense
("hours rows: 1") and nearly flagged a working feature as broken.
Rule: in verification scripts, normalize with
`$rows = if ($r -is [array]) { $r } elseif ($r.data) { @($r.data) } else { @($r) }`
before counting/grouping.

## DP-04 — HTTP header case-sensitivity in match patterns

Incident: smoke script grepped `'etag: (\S+)'` but the server emits `Etag:`;
the empty capture made a working ETag feature look missing. curl is fine;
the *matching regex* was case-sensitive where HTTP headers are case-insensitive.
Rule: header assertions use case-insensitive matching and assert on the value
shape, not just presence.

## DP-05 — Backslash escapes eaten by the harness template string

Incident: a path `'.cc-otel\cc-otel.db'` written inside a TS template string
became `.cc-otelcc-otel.db` (`\c` is an unknown escape -> 'c'); three
"impossible" file-not-found failures in a row. Also seen: `Join-Path a '.x' + '\y'`
parsed as a *directory named* `+`.
Rule: in run_code/pwsh payloads, write Windows paths as \\ doubled, or build them
with `Join-Path` / `${env:USERPROFILE}` interpolation, and echo the resolved path
before using it.

## DP-06 — Fill-only updates can target the wrong row

Incident: the turn-level TTFT fill (newest zero-TTFT anchor) landed on a FAILED
request row — the per-request fix had already filled the succeeding rows, leaving
the failed row as the only zero anchor. Result: `ttft 6146ms > duration 5005ms`,
physically impossible.
Rule: any "fill the newest zero-value row" UPDATE must exclude rows that can never
legitimately hold the value (here: `error_message != ''`), and any derived metric
must assert its invariant (ttft <= duration) in tests. (commit 14405dd)

## DP-07 — Retry/replay double-counting at completion events

Incident class: exporters retry; a duplicate `response.completed` (with or without
usage) must not double-write tokens or insert a second row. Two arrival orders
(token-less first vs authoritative first) are pinned by unit tests.
Rule: every ingestion completion path needs (a) an idempotency key
(request_id partial unique index), (b) explicit gating per arrival order, with tests
for both orders. (commits 4656999, 72d0bfa, dced8fc)

## DP-08 — Multi-line edit eats adjacent load-bearing lines

Incident: a text edit intended to replace 3 lines in app.js also deleted four
boot-critical calls (`loadAll(); initSSE(...)` among them); the page went dead
until a re-read revealed the loss.
Rule: after any multi-line edit tool operation on code, immediately re-read the
region and run the cheapest syntax check (`node --check` / `go build`) before
anything else.

## DP-09 — Single-instance assumptions behind global PID files

Incident: `cc-otel start` refuses when the global PID file's process is alive —
even for a different config/ports; a rehearsal instance on ports 25317/25899 could
not start.
Rule: for multi-instance needs use `serve` (foreground, no PID file) under a
background job; and treat the PID file as advisory, verifying the process actually
matches the intended config before trusting "already running".

## DP-10 — Silent 2xx on rejected input

Incident: invalid dates (`from=2026-99-99`) returned 200 with empty data; clients
could not tell "no data" from "bad request". Fixed by the structured
`{"error":{"code":...}}` envelope (`writeAPIError`) across the dashboard family.
Rule: never return success-shaped responses for malformed input; new endpoints use
`rejectInvalidDateParams` + `writeAPIError` from day one. (commit 808f601)

## DP-11 — Precedence drift between fallback and precise writers

The TTFT stack has three writers (per-request SSE/websocket reconstruction,
turn-level fill, completion finalize). Correctness = precise value wins, fills never
overwrite, finalize may upgrade. Any new writer must state its precedence tier and
prove it cannot downgrade an existing value.
Rule: when multiple code paths can write the same column, document the precedence
order next to each writer and test the pairwise interactions.

## DP-12 — Embedded-assets staleness

`go:embed` snapshots static files at build time; testing a UI change against a
running binary serves the OLD UI. Either set `CC_OTEL_STATIC_DIR` or rebuild +
restart before verifying — a "fix doesn't work" conclusion is invalid until
freshness is proven (bump `?v=` in index.html for cache busting).
