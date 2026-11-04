# cc-otel coding conventions (enforced by the implement skill)

Companion to CLAUDE.md (which stays authoritative on semantics). This file is
about how code is written here.

## Go

- Errors wrap with context: `fmt.Errorf("opening db: %w", err)`. Never swallow;
  a deferred close error on a WRITE path is logged.
- Long UPDATEs against WAL go through `execWithBusyRetry` — never a bare Exec.
- Connection pragmas live in dsn.go only.
- Schema changes are additive via the `ensureColumn` idiom and must state the
  upgrade cost for a 500MB DB (DP-01).
- New ingestion writers to ttft_ms / duration_ms / cost_usd carry a precedence
  comment naming their tier (DP-11) and a pairwise test.
- Comments that record live verification use the marker `live-verified` with the
  date and evidence; expectations discovered by wire dumps cite the dump.
- Tests are regression tests: name them after the incident they pin
  (e.g. `TestDispatchCodexLog_SSECreatedFillsPerRequestTTFT`).

## Web (internal/web/static — ESM, no build tools)

- One responsibility per module under js/; new pure helpers land in utils.js /
  theme.js WITH a `.test.mjs` companion.
- DOM modules expose `initX({ openPopover, closePopover, ...callbacks })`;
  app.js wires dependencies at boot. Never import vendor globals — read
  `window.echarts` / `window.flatpickr`.
- The three chart commandments (no stack/axis-trigger/series-focus) and the
  Input-semantics definitions are pinned in CLAUDE.md — re-read before touching
  any chart or KPI code.
- `fetch()` only inside js/api.js.
- After edits: `node --check` the file, run the suite, bump `?v=` when CSS or
  the root module changed (plus the pinned test assertion).

## Process

- Static files are embedded (go:embed): proving a UI change requires rebuild +
  restart (or CC_OTEL_STATIC_DIR) — a stale-binary "doesn't work" conclusion is
  invalid (DP-12).
- Dev instance only on 14317/14318/18899; production 4317/4318/8899 is never a
  test target; rehearsals on 25xxx via `serve` (DP-09).
- Multi-line edits: re-read the region afterward + syntax check (DP-08).
- Windows paths inside harness payloads: doubled backslashes or Join-Path (DP-05).
