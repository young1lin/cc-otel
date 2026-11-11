## Changelog

[中文版本](./CHANGELOG.md)

This project follows a lightweight changelog format (Keep a Changelog inspired), optimized for a small, fast-moving tool.

---

## [v0.1.9] - 2026-11-11

### Fixed

- **dsh plugin align is upgrade-only**: the boot-time sync of `~/.cc-otel/` to the bundled binary no longer fires on mere version inequality (which once downgraded production, 2026-11-06: plugin 0.1.3 clobbered a newer manual deploy) but on a direction comparison (new `npm/dsh-cc-otel/lib/versions.js`): overwrite + daemon restart only when the bundled version is **strictly newer**; a newer installed binary (e.g. a manual dev deploy) is skipped with a hint; an unparseable version (anything beyond `X.Y.Z` / `vX.Y.Z-N-g<sha>[-dirty]`) is never touched. git-describe suffixes sort as "N commits after the base tag" (`0.1.6 < v0.1.6-1-g… < 0.1.7`). Spec: `docs/internal/design/align-never-downgrade.md`.
- **dsh plugin self-update**: at most one npm check per boot (24h throttle by default, marker file in the system temp dir); when a newer `@young1lin/dsh-cc-otel` is published it runs `dsh plugin --profile <p> update @young1lin/dsh-cc-otel` automatically, effective next boot; failures log one line and never affect telemetry or the daemon. New plugin row config: `pluginAutoUpdate` (default on; `false` warns without installing) and `updateCheckHours` (default 24; `0` disables the check).

## [v0.1.8] - 2026-11-11

### Fixed

- **Import-dialog background-job progress freeze (live incident 2026-11-07)**: since 01952a5 gated the polling, nothing ever restarted the chain on click — with the dialog opened after the boot tick, the card froze on the POST's 0% snapshot forever (observed live: a 15,807-row import that finished server-side in 4 seconds displayed "Importing 0 / 15,807 rows (0%)" indefinitely). The cursor import card inherited the flaw and the Codex migration card had shipped with it; both are fixed — a successful POST restarts polling immediately, and reopening the dialog (MutationObserver on `style`/`aria-hidden`) resumes it too, so the terminal state (Done / failure) always reaches the card.

### Added

- **Clients panel per-model drill-down**: clicking a client row expands an inline table of **every model under that client** (Requests / Uncached / Cache Read / Cache Create / Output / Total / Share / Cache Hit / Cost), **sorted by total tokens DESC**, Share being the model's percentage of the client (denominator = the fetched model sum, always 100%); clicking again collapses. Expanding lazy-loads fresh data for the **current range selector** (`GET /api/clients/models?service_name=&from=&to=` — GROUP BY model over the `claude_pool_requests` view, same source and semantics as the parent row: empty/NULL service folds onto `claude-code`, same int64→float cost conversion; live reconciliation: all 10 clients x All Time/7-day ranges match parent rows exactly on tokens/requests/cost). After an SSE push or range switch re-renders the parent table, expanded rows automatically re-fetch for the new range and stay open. The chevron button carries `aria-expanded` + a 90-degree rotation (120-180ms) and works via keyboard Enter; column definitions mirror the parent table (pinned input-side semantics). Also fixes three stale texts (panel hint + two comments still claiming the view excludes codex — the opposite of the unified pool).

- **Cursor billing import (dashboard)**: the database-import dialog gains a **Cursor usage import** card (Cursor's official 2D mark, inline SVG, theme-following `currentColor`). Upload one or many Cursor `usage-events` CSV exports (Settings → Usage → Export usage events); the four token columns map 1:1 (`Input (w/o Cache Write)→input_tokens`, `Input (w/ Cache Write)→cache_creation_tokens`, `Cache Read→cache_read_tokens`, `Output→output_tokens`). Exports carry no request id, so each row derives a content hash `cursor-<sha256[:16]>` that rides the partial unique index on `api_requests(request_id)` — duplicate downloads, overlapping month exports, and repeated imports all collapse idempotently (re-runs only count duplicates). Model names are stored as their **base identity** (`NormalizeCursorModel`): Cursor's tier suffixes (`fast` = speed, `max`/`thinking`/`xhigh`/`high`/`medium`/`low` = thinking level) and the `cursor-` prefix are stripped (`gpt-5.1-codex-max`→`gpt-5.1-codex`), with whole-segment whitelist matching that leaves real names intact (`grok-code-fast-1`, `gemini-3-flash-preview` untouched); the raw slug still feeds the dedup hash, so mapping tweaks never break idempotency. Rows are attributed `service_name='cursor'` (unified view / Clients panel pick them up automatically); Cloud Agent rows get a `cursor-agent-<id>` session; zero-token rows are skipped by default (opt-in keep). **Cost semantics (user-directed 2026-11-08)**: plan-included rows (CSV `Included/Free/'-'` = $0) are **estimated at list price** — `cursorimport.Import` takes an injected Pricer and Calcs on the base model name at import time, history is backfilled by ↻ recompute, and a POSITIVE CSV cost (User API Key money actually charged) is stored verbatim, never overwritten (recompute skips cursor rows with `cost_usd > 0`; rehearsal: cursor totals $1467.55, 31 of 35 models priced). Endpoint `GET/POST /api/cursor/import` (same-origin guard; shares the one-writer gate with the DB import and Codex migration, 409 on overlap), followed by aggregate rebuild + `import_ledger` audit + WAL TRUNCATE. `pricing.IsUpstreamAuthoritative` drops its blanket cursor arm for this: $0 rows estimate, paid rows stay. Rehearsal (18899, the real 11-file 20683-row export): first run +18890 (= 20683 − 1793 zero-token rows), all four token sums match the CSV side exactly, re-run +0 new / 18890 duplicates, pool == agg == ledger reconciliation passes. **Import UX increment (2026-11-08)**: ① tolerates the new export format (`Automation ID` and `Max Mode` columns added; name-based mapping just works) and attributes rows carrying only an Automation ID to `cursor-automation-<id>` sessions (neither ID feeds the dedup hash — current exports ship the column empty, so this is forward capability). ② the status payload gains `history` (per-file rollup of past imports from `import_ledger`, computed per GET, survives reloads) and `superseded_hint` (interactive-model rows sharing an exact timestamp+model — the visible footprint of Cursor re-exporting a revised row; background families grok-bot-\*/Bugbot/agent_review fire genuine same-millisecond parallels — 320 of 333 on the rehearsal pool — and are excluded; the UI words it "possibly revised"); the card appends the hint to the Done line and renders the last 5 imports. ③ fixes a `:memory:` pool hazard: `db.Init` pins in-memory DBs to one connection (file DBs keep 4) — SQLite memory state is per-connection, and a GET query racing an import once handed the import a blank pooled connection (`no such table`). **Card polish (2026-11-08, option A)**: the whole card is now a CSV drop zone (reuses the DB import's dashed highlight language, outline-based so layout never shifts); a read-only preview endpoint `POST /api/cursor/import/dry-run` (same parse and guards, never writes, skips the writer gate) runs automatically once files are chosen and shows "N file(s) · +X new · Y duplicates · date range · new models" (other errors stay quiet, no_usable_rows is surfaced; counts are approximate if a real import runs concurrently — it is a confirmation hint); after a clean Done a "View in Clients panel →" link closes the modal and switches the panel. Rehearsal (18899): already-imported file previews `+0 new / 2651 duplicates`, pre-import `+2 new / 0 duplicates`. **Tab layout (2026-11-08, user-directed)**: the import modal now has **Database / Cursor** tabs at the top (quiet segmented control in the range-tabs language; every open resets to Database). The Cursor tab leads with the **same `.dbi-drop` drop zone the Database tab uses** ("Drop Cursor usage CSVs here" — click to pick, drag to highlight), turning drag from a hidden capability into an explicit entry point; the modal title becomes "Import data" with a subtitle covering all three sources; the Database tab (db merge + Codex migration card) is unchanged. **Fix**: the DB import action bar (Cancel / Choose database) originally sat outside both panes and leaked into the Cursor tab; it now lives at the bottom of the Database pane. Same-day fix (user-directed): the Cursor import's running state switched from a plain text line to the **same `.dbi-progress` fill bar the DB merge uses** (exact fraction drives the width, rounded % is the text; the meta line carries `processed / total · inserted · duplicates`), with the status line slimmed to "Importing…". Same-day narrow-viewport fix: `@media (max-width:640px)`'s `.dbi-primary/.dbi-secondary { flex:1 1 140px }` was designed for the DB footer pair and the single Codex merge button — the Cursor card's two same-class buttons inherited the stretch; the rule is now scoped to `.dbi-footer` and `#codex-migrate-start`, and the Cursor action buttons keep natural width and may wrap. Follow-up fix ("pre-import scrolling looks broken"): the Cursor tab's drop zone borrowed the DB empty-state's 190px hero height — at 640×700 the pane was 581px vs a 553px viewport, overflowing by just 28px yet spawning a scrollbar. It is now a compact secondary entry (96px, 20px icon); measured scroll need at 640×700 drops **28px→0** (the DB tab was already 0 at that size). At 640×560 the Cursor tab needs 33px — less than the DB tab's 116px. Verification: headless Edge + CDP forced-viewport geometry measurement (`bin/tmp/geom2.mjs`, gitignored).

- Note: the offline pricing snapshot (`internal/pricing/embed/seed.json`) was not refreshed for this release — upstream unreachable (offline) at tag time; the existing snapshot remains the fallback.

## [v0.1.7] - 2026-11-05

### Fixed

- **Usage Calendar All Time layout**: `calendarLayoutMode` always received the range argument but never used it, choosing a layout by week count alone — with under 18 weeks of history, All Time fell into 'medium' (22px centered cells, plus a horizontal scrollbar on narrow screens). `range=all` now unconditionally uses 'long' (13px compact cells, left-aligned); fixed windows (Today / 7 Days / 30 Days / Custom) keep the original ladder. Pinned in `insights-calendar.test.mjs` with the production data shape (69 days / 11 weeks, starting 2026-08-28).
- **Month-label overlap**: for a history starting near month-end, the first and the next month's labels sit one column apart — a 3-char label at 11px spans roughly two compact tracks, so the texts collided. `calendarMonthLabels` gains `minColGap` (default 2, the same trick as GitHub's contribution graph) and drops labels that start too close to the previous one; the wide 30px 'short' layout passes 1 and is unaffected.

## [v0.1.6] - 2026-11-04

### Changed

- **Agent lifecycle skill suite**: six English skills under `.claude/skills/` (junction-visible at `.agents/skills/`), all custom-built for this repo — design (requirement triage + specs land in `docs/internal/design/` + user approval gate), implement (task-by-task execution under coding guards), document (bilingual doc/memory parity gates), pre-commit (forbidden-path + privacy + build/test gates, then executes the commit itself), code-review (12 repo-specific defect patterns DP-01..12, 8 hotspot invariants, mechanical checklist), release (npm/GitHub checklist plus the production rollout runbook: rehearsal -> hash-verified swap -> smoke matrix -> rollback). Every pattern and hotspot cites the incident it came from.
- **`.agents` mirror layout**: `AGENTS.md` hardlinks `.claude/CLAUDE.md`; `rules/`, `skills/`, `harness/` are per-entry junctions — any agent tool honoring the `.agents` convention reads the same rules.
- **Repository hygiene**: tightened gitignore (local settings, throwaway plans, and review material stay out).
- **Docs**: added the Clients panel screenshot (`assets/images/clients.png`, every client in the unified view), referenced from both READMEs.

## [v0.1.5] - 2026-11-03

### Fixed

- **Codex history auto-import + 30-day purge**: startup imports the `codex_*` archive inside the single-writer window with exact row-id ranges recorded (already-migrated installs take a zero-cost fast path); 30 days later the imported rows are purged and the aggregate rebuilt, the frozen archive untouched. Pinned by TestAutoImport_*.
- **Import dialog accepts historical Codex backups**: legacy files (pre-AllClient unified schema) route through the codex→unified mapping, modern files append the archive mapping; missing columns become typed constants and the ancient tool_tokens spelling is aliased. Pinned by TestImportLegacyCodexBackupLandsInUnifiedTables (live 18899 flow: 500-incl-cache→300 disjoint, tool_result defaults, idempotent re-upload).
- **turn_ttft race**: OTLP export reordering let the turn event beat its anchor row, so the one-shot UPDATE missed — observations are now stashed (10min TTL) and retried on anchor insert; live 1-of-2 loss before, 3/3 captured after. Pinned by TestCodexE2E_TurnTTFTBeforeAnchorIsStashedAndRetried.
- **Codex TTFT backfill**: the dedicated `codex.turn_ttft` event (turn-level time to first output) is now modeled — the completion's `ttft_ms` attr is an Option (absent for non-streamed/instant responses; measured 27% of 30,735 rows carry it), so the turn event fills `ttft_ms` on the newest zero-TTFT anchor of the same (conversation, model) at or before the event time, fill-only; an authoritative completion ttft still wins (all four orderings pinned by TestCodexE2E_TurnTTFTFillsAnchor).
- **Duplicate-anchor redelivery ErrTxDone** (R5 rollback P1): insertCodexRowInTx committed inside the duplicate branch, so the caller's own commit raised sql.ErrTxDone and routine at-least-once redelivery answered 500. The helper never commits now (pinned by TestCodexE2E_DuplicateAnchorRedeliveryStaysOK: three identical anchors 200/200/200).
- **bin→global merge schema-ensure wiring** (R5 rollback P1): import_global now calls ensureTargetSchema before importing, so an AllClient bin export merges into a still pre-AllClient production global instead of failing mid-merge on a missing column.

- **OTLP delivery contract**: a batch where EVERY record fails storage now answers gRPC Internal / HTTP 500, so the batch is no longer acknowledged as delivered (retry behaviour is exporter-dependent; since nothing landed, a retry that does happen cannot duplicate rows); partial failures return PartialSuccess (rejected records are terminal per OTLP semantics — visible, not re-sent — so partially-landed rows cannot be duplicated).
- **Codex completion replay double-count**: the replay guard matched timestamps exactly, but tracker-finalized rows keep the api_request's earlier timestamp — genuine redelivery slipped past and inserted a duplicate. Identity is now (session, model, full token vector) with a window match; e2e pins replay as a no-op.
- **Retention-scoped migration sanity check**: daily_model_agg is retention-pruned while api_requests is not; the all-time comparison false-failed every re-run after the first post-migration prune. New `dbmerge.Options.RetentionDays` (handler injects config) + prune→re-migrate regression test.
- **DSN pool pragmas**: busy_timeout/cache_size/mmap_size/temp_store/journal_size_limit now reach EVERY pooled connection via the DSN (previously only the first).
- **WAL high-water reclamation**: post-migration PASSIVE never shrinks the -wal file (measured frozen at 340MB in production); added a TRUNCATE checkpoint + journal_size_limit(64MB).
- **recompute apply rebuilds agg on 0 changes**: the apply button is the only repair path for agg drift; skipping the rebuild left drift permanent.
- **Repository.Prepare hard-fails**: the logged per-call fallback never existed; schema drift panicked on first request. Startup now panics naming the statement.
- **Config guards**: db_path ':memory:', otel_port/web_port <= 0 and negative retention_days are rejected at Load (port 0 bound a random ephemeral port while /api/status kept reporting the configured number; only otlp_http_port: 0 keeps its explicit disable semantics).
- **Same-origin guard**: mutating /api/* routes (pricing, import, codex/migrate) reject cross-origin browser requests (Origin check; curl/CLI unaffected).
- **pricing POST body capped at 1MB** (MaxBytesReader, matching import).
- **pending_ttft_spans purge**: processed/stale rows ride the raw TTL sweep (previously grew forever); raw archive future timestamps clamp to now.
- **Codex cache-hit denominator aligned**: includes cache_creation like the Claude view and frontend token-math (Codex reports large cache_creation; the old formula overestimated).

- **DSH path clamps both ends** (R5 P0): integer fields in DSH session-ledger body JSON bypassed parseAttrInt and could land raw as MaxInt64 counters / year-2262 timestamps; both sides now clamped.
- **recompute agg rebuild is always full-range** (R5 P1, wording fix): daily_model_agg is no longer retention-pruned (matching never-pruned api_requests); INSERT-only rebuilds cannot resurrect anything and All-time views span the detail exactly. RetentionDays does not scope the rebuild; the post-migration sanity check alone stays conservatively window-scoped.
- **`/api/requests` gains five unified-pool fields** (R5 API P1): reasoning_tokens/total_tokens/http_status/endpoint/conversation_id are now selected for real.
- **OTLP/HTTP receiver cross-origin guard** (R5): cross-origin browser POSTs (even with simple content types) are rejected 403 via Origin checks; real exporters never send Origin and are unaffected.
- **405 responses carry `Allow`** (RFC 9110): on /api/pricing, /api/pricing/recompute, /api/pricing/suggest.
- **Docs corrections**: raw-table write direction fixed (raw_otlp_events receives unmodeled codex events under TTL; codex_raw_otlp_events is a frozen archive with no writers).
- **Boot ANALYZE replaced by `PRAGMA optimize`** (R4 boot perf): full-DB ANALYZE measured 85-95% of every boot (1.4-1.85s on the 535MB DB); `PRAGMA optimize` with fresh stats measured 1ms. Boot drops from 1.46s to ~0.2s.
- **`temp_store` switched to FILE** (R4 memory): TEMP b-trees and migration staging tables used to live in the wasm linear heap that never returns memory; on-disk temp stops the permanent RSS ratchet.
- **BUSY retry coverage for write paths** (R4 concurrency): 7 bare `ExecContext` call sites now share `execWithBusyRetry` like the receiver path.
- **recompute mutually exclusive with import/migration** (R4 concurrency): recompute, import and codex-migrate gate on `writerBusMu`; heavy writers no longer fight over the busy_timeout window (conflicts answer 409 `writer_busy`).
- **SSE refresh coalescing + durations on demand** (R4 frontend): SSE refresh rounds carry an in-flight guard; Request Log paging no longer re-issues the full-table durations GROUP BY.
- **Static assets ETag/304** (R4/R5 frontend): embedded assets ship content-hash ETags and answer If-None-Match with 304 (~500KB gzip saved per refresh on the echarts bundle); `/` shares the index.html hash; the CC_OTEL_STATIC_DIR dev mode skips ETags.
- **Auto-import progress logs + timestamp-based rollback probe** (R4/R5 boot): large imports log progress every 2s (15s+ of silence looked like a hang); the rollback probe compares timestamps so fast-path DBs stop printing a spurious WARNING every boot.
- **`/api/codex/intraday` rejects invalid dates with 400** (R5 consistency): the mirror used to drop both dates when either was missing and silently fall back to 200 + today's data; it now matches the base endpoint.
- **API 4xx/5xx unified as structured JSON** (R5 consistency): plain-text `http.Error` replies across the dashboard family converge on the `writeAPIError` envelope with snake_case codes.
- **Codex Sessions gains the User column and aligned ordering** (R5 display): the SELECT missed `MAX(user_id)` (column always empty); ordering aligns with the pool side (`SUM(cost_usd) DESC`).
- **`/api/codex/requests` selects 8 more fields** (R5 API): event_name/event_sequence/terminal_type/service_version/host_arch/os_type/os_version/error_message were in the payload shape but never selected.
- **Codex durations throughput columns computed** (R5 display): Out tok/s (avg/wt) were hardcoded 0.0; they now use the pool-side formulas and exclude duration_ms=0 rows.
- **Migration card empty state** (R5 UX): after a daemon restart the idle card no longer claims no archive exists; it now says not-inspected-yet and idempotent.
- **SSE refresh rule restored to main's** (R5 regression): claude-tagged events no longer refresh the Codex tab (every codex endpoint is service-filtered; it was pure over-refresh).
- **TTFT tooltip line breaks fixed** (R5 display): literal backslash-n in the template became two visible characters; now a real newline.
- **Docs rewritten for the two-tab semantics** (R5 docs): both READMEs wrongly described a single unified page and claimed `?source=codex` was removed; rewritten as All Clients (Codex excluded) + Codex tabs, plus a `bind_host` config section with upgrade notes and the unified-pool storage map in otel-events.md.
### Changed
- **Fixed missing per-request Codex TTFT**: the HTTP-SSE transport (codex exec) never reconstructed per-request TTFT — the turn-level codex.turn_ttft could only fill the first request of a tool-loop turn, leaving every later request at 0 forever (live-measured: 4 of 6 rows per turn at ttft=0). response.created observed time minus the pending api_request startNanos anchor now rebuilds TTFT for each request, mirroring the websocket path (post-fix live run: 6/6 rows populated).
- **ONE unified view (user-directed final state)**: the claude_pool_* views no longer exclude codex — KPIs, charts, Sessions, Request Log, Clients, Hour of Day and Rate cover every client including Codex; the All Clients | Codex source tabs are removed and legacy ?source= deep links are silently rewritten to the plain URL; Hour of Day, Rate and Clients are always available (they used to be hidden under the Codex tab); SSE refreshes on any source event. The /api/codex/* mirror endpoints remain for external scripts — the dashboard itself no longer calls them.
- **`bind_host` defaults to loopback (upgrade note)**: all three listeners (4317/4318/8899) tighten from `0.0.0.0` to `127.0.0.1` with a Host allowlist (127.0.0.1/localhost/[::1] only). Deployments that received cross-machine traffic must set `bind_host: 0.0.0.0` explicitly (the allowlist then disables itself; note there is no built-in auth). See the README listen-address section.

- `/api/status` gains a `version` field (ldflags -X; "dev" for local builds).
- Codex migration job logs start/done/fail with scanned/inserted/duplicates/verified counts; no-op re-runs skip the staging re-inspect (~3s saved).
- Log rotation opens eagerly at serve startup: a read-only data dir no longer silently swallows logs and falls back to stderr.
- `tools/migrate_codex_data` marked deprecated in favour of the built-in AllClient migration.

---

## [v0.1.4] - 2026-11-02

### Fixed

- **Codex environment lands in the User column**: Codex CLI reports `[otel] environment` (`~/.codex/config.toml`; dev / company / staging / prod, default dev) as the OTLP Resource attribute `env` (`ENV_ATTRIBUTE` in codex-rs `otel/src/provider.rs`); cc-otel used to drop it, leaving the User column a permanent em-dash. It now maps onto the same `user_id` column as Claude Code's `user.id`, so the Request Log User column answers "which environment drove this request"; an explicit `user.id`, if a future Codex build sends one, wins. Affects newly inserted rows only — history keeps its empty value.

### Fixed

- **OpenRouter suggest hardened**: the official base URL is unchanged (`https://openrouter.ai/api/v1`, still referenced by Oct-2026 docs) — real failures were edge/WAF misroutes or wrong paths. The one-click lookup now sends a product `User-Agent` + `Accept: application/json` (the bare `Go-http-client` default is filtered by some CDN edges); non-200 responses surface OpenRouter's own reason (e.g. `HTTP 404 (Not Found)`) in the UI toast instead of a bare status; new `openrouter_base_url` config key + `CC_OTEL_OPENROUTER_BASE_URL` env var point at a mirror/self-hosted gateway, with empty/default resetting to the official endpoints.

### Added

- **Clients panel**: per-client (OTLP `service.name`) totals — tokens (uncached input / cache read / cache create / output / total), requests, cost, hit rate and active span, ranked by consumption with a share bar. Reads the deliberately never-pruned `api_requests` ledger for full history; the panel hides under the Codex source (separate ledger).
- **Hour of Day sub-view (client x model dimensions)**: third subtab in Daily Detail folds the whole selected range into 24 hour-of-day buckets plus two dropdowns: unpinned draws grouped bars by model (top 8 + Other, never stacked); pinning a client shows its model mix; **pinning a model flips the axis to clients** ("who burned Claude Opus at 01:00"); pinning both yields a single series ("Claude Code's Opus at 13:00") with the full token composition in the tooltip. Follows the Tokens/Cost/Requests switch; dropdown changes re-pivot cached rows without a refetch. The backend `/api/hours` now returns (hour, service_name, model) detail cells. Complements Intraday (chronological per-bucket detail, ≤7 days) and works for any span; the subtab hides under the Codex source until a codex mirror endpoint exists. The chart obeys the project's pinned rules: grouped bars and a trigger:'item' own-bar tooltip (hover shows only that bar plus its cross-dimension breakdown).
- **Pagination jump box**: the shared pagination bar (Daily/Sessions/Request Log/Pricing) gains a page-number input + Go (Enter works too); out-of-range input snaps to the nearest valid page. Fixed the Go-click blur race (the browser blurs the input before the click fires, and the cosmetic blur reset made every click a silent no-op — the handler now reads the last typed value cached from input events); `pagination.test.mjs` reproduces and pins the exact click sequence.
- **Client names identical to the Request Log**: badge rendering converges on a shared `formatSourceTag` (utils.js); unknown `service_name`s always render verbatim (e.g. `deepseek-harness-plugin`) — no per-panel invented aliases.

### Development

- Integration-test data imported from the global DB via online `VACUUM INTO` snapshot per the iron rule (global daemon never stopped); the bin `daily_model_agg` turned out to be retention-trimmed after rebuild, so Clients/Hour of Day read `api_requests` directly to match the ledger.

---

## [v0.1.3] - 2026-09-10

### Added

- **dsh telemetry adapter (continuous reporting)**: the `@young1lin/dsh-cc-otel` runtime plugin gains a transparent `llm/stream` listener (`npm/dsh-cc-otel/lib/telemetry.js`) that maps each settled provider attempt onto one OTLP `api_request` record (Claude Code vocabulary — the generic ingestion path stores it with zero receiver changes) and pushes it to the local :4318 in real time — the proper source of continuous statistics now that dsh 0.1.5 removed FULL. Resource `service.name` is `deepseek-harness-plugin` (the exact string `deepseek-harness` routes records into the session-ledger parser, which drops them); `request_id = dshp-<uuid>`; batches flush at 3s/16 records, failures drop silently and never touch the model call. The bundle patch pins the native telemetry row to `DISABLED` (its feedback-time full-prefix export would double-count beside the adapter); configurable via `CC_OTEL_OTLP_URL` / the plugin row's `telemetry.{url,enabled}`. Note: adapter rows use a key namespace disjoint from `tools/dsh_ingest` — do not run the session-log backfill for sessions already covered by adapter rows.

## [v0.1.2] - 2026-09-10

> New npm distribution channel: one-command cc-otel onboarding for DeepSeek Harness, with the Go binary split into six per-platform npm packages that ship automatically alongside the main package. All existing channels (GoReleaser / GitHub Releases / Claude Code plugin marketplace / source builds) are untouched.

### Added

- **npm distribution**: main package `@young1lin/dsh-cc-otel` (the dsh profile bundle: telemetry patch + daemon-keepalive runtime plugin + a `cc-otel` CLI shim) plus six `@young1lin/cc-otel-{linux,darwin,win32}-{x64,arm64}` binary packages. Platform packages carry `os`/`cpu` fields and hang off the main package's `optionalDependencies`, so package managers install exactly the one matching the environment; no install scripts, nothing trips pnpm 10+'s build-script allowlist. Install: `dsh plugin --profile web add @young1lin/dsh-cc-otel` (any profile works).
- **dsh plugin runtime** (`npm/dsh-cc-otel/lib/index.js`): on profile boot it resolves the platform binary and version-syncs `~/.cc-otel` — `cc-otel install` runs only when versions differ (version-gated, a no-op in steady state, so a healthy shared daemon is never bounced); the daemon always runs from the stable `~/.cc-otel` path, so `dsh plugin update/remove` cannot fail on Windows just because the executable is in use; `cc-otel start` is idempotent ("already running" counts as success) and the shared daemon is never stopped on dsh exit unless `stopOnUnload` opts in. `autoStart` / `manageHomeBinary` / `binary` (`CC_OTEL_BIN`) overrides are supported.
- **Zero-config telemetry wiring**: the bundle's `cordis.patch.yml` overrides dsh-base's `session-telemetry-otel` row by `id` (whole-key replacement) to point at local `http://localhost:4318/v1/logs` (`mode` defaults to `FEEDBACK_ONLY` — dsh 0.1.5 removed `FULL` upstream and a config carrying it fails validation at startup; upload-cadence and privacy notes in the README). `DSH_TELEMETRY_MODE` / `DSH_TELEMETRY_OTLP_URL` / `DSH_TELEMETRY_DISABLED` and any hand-written `cordis.patch.yml` user layers keep applying after the bundle — fully compatible and coexistent with the manual onboarding methods.
- **DSH 0.1.5 adaptation**: the receiver's `request_id` is now `dsh-v<session.format_version>-<session.id>-<event.seq>` (falling back to the legacy versionless key when the attribute is missing), so v3-generation renumbered seqs can no longer collide with stored v0 rows; new `tools/dsh_ingest` — with `FULL` telemetry removed, ordinary conversation uploads nothing, so this tool backfills usage idempotently straight from the local session logs (`~/.dsh/sessions/**/session.v3.jsonl.zstd`), deleting the session's old rows in both key shapes first to avoid double-counting; every `mode: FULL` example (Chinese/English READMEs, otel-setup skill, npm package) now reads `FEEDBACK_ONLY`, and the troubleshooting table gains "boot fails with `mode expected …`" and "chatted but no data" rows.
- **Release pipeline**: a new `npm` job in the release workflow — downloads the artifacts GoReleaser just published, assembles the seven npm packages with `scripts/build-npm.mjs` (binaries byte-identical to the GitHub release; each platform package keeps its binary at the package root, deliberately not in `bin/`, to dodge cc-otel's dev-mode data-dir rule), and publishes each with `npm publish --access public` (prerelease versions go to the `next` dist-tag). Authentication is **npm Trusted Publishing (GitHub Actions OIDC)** — no `NPM_TOKEN` secret anywhere, same scheme as dsh-cc's publish.yml; each package is bootstrapped once (first publish by hand with OTP, then bind user/repo/workflow on npmjs.com). In-repo versions stay `0.0.0` placeholders; the tag version is injected at assembly time.
- **Docs**: Chinese/English READMEs gain npm install + update sections, an npm-plugin entry point in the supported-clients table and the DSH onboarding chapter; `npm/README.md` records the package family, the publishing flow, and the design rationale.

### Verification

- `dsh --profile web --dump-config --patch` confirmed the patch semantics: the telemetry row is replaced whole-key with exactly one row pointing at the local endpoint, and the plugin row inserts cleanly.
- A real `dsh plugin --profile <scratch> add file:…` end to end: pnpm installed only the one matching platform package (the other five skipped by `os`/`cpu`), the bundle auto-registered into `dsh.profile.bundles`, and during a real host boot the loader imported the plugin and ran the full install/start flow.
- With an isolated `CC_OTEL_HOME` (propagated to child processes on both sides) verified version syncing, idempotent start, and that the real `~/.cc-otel` is untouched; `npm pack --dry-run` checked file lists and sizes for all seven packages (main 7.6 kB, platform 9–10 MB).

---

## [v0.1.1] - 2026-08-23

> Patch release fixing stale Web-panel data, asynchronous response races, and write stability while SQLite is busy.

### Fixed

- **Refresh on panel activation**: Daily Detail, Sessions, Request Log, and Rate now share one activation path and re-query when reopened, instead of exposing DOM data left stale while another panel was active.
- **Async race protection**: Dashboard, main chart, Daily/Intraday, Sessions, Request Log, duration stats, model options, and Rate use latest-request generations. Older date/page/source/SSE/manual-refresh responses can no longer overwrite newer UI state.
- **Source switching**: Claude/Codex changes synchronously clear the previous model selection and options; context changes also invalidate in-flight requests belonging to hidden panels.
- **Honest Codex Rate behavior**: Rate currently has no Codex backend, so it is hidden for Codex. Switching to Codex while Rate is open returns to Daily without a duplicate fetch, and the loader refuses to read `/api/rate` under Codex.
- **SQLite write stability**: `InsertRequest` retries `SQLITE_BUSY`, reducing transient ingestion failures under concurrency; CI tests avoid unnecessary fsync.

### Verification

- Frontend Node tests pass **102/102**; the complete Go test suite passes.
- The embedded frontend was rebuilt and exercised with real Chromium on isolated development ports, covering Rate → Sessions / Request Log re-entry, Codex Rate hiding/redirect, and the 7 Days view.

---

## [v0.1.0] - 2026-08-17

> First stable release. cc-otel is no longer coupled to Claude Code: standalone install directory, four clients sharing one instance.
> Summarizes the main line after `v0.1.0-preview.16`: service_name attribution, OTLP/HTTP ingress + opencode, service-aware pricing, DeepSeek Harness ingestion, and the code-review fix batch.
> As the first stable release it also folds in every commit since the initial one — all 46 are indexed at the end of this section.

### v0.1.0 capability overview

| Area | What v0.1.0 supports |
|------|----------------------|
| **Clients** | Claude Code, Codex CLI, opencode, and DeepSeek Harness (DSH) share one instance; routing and attribution by the OTLP Resource `service.name` |
| **Ingestion** | OTLP gRPC `:4317` + OTLP/HTTP `:4318`, all three signals through one ingestion path; transparent `Content-Encoding: gzip` decompression; idempotent dedup of exporter retries |
| **Cost & pricing** | Manual price table (in-UI CRUD, instant effect) + on-demand OpenRouter suggest (first-party provider price by default) + full historical recompute; upstream `cost_usd` authoritative only for Claude Code on Claude models |
| **Web dashboard** | KPI cards + breakdown modals, Tokens/Cost/Requests bar chart, Daily / Sessions / Request Log / Token Rate / Intraday panels, GitHub-style usage calendar, SSE live refresh, dark/light themes, browser history navigation, online DB import |
| **Storage** | Single-file SQLite (WAL + busy_timeout); live `daily_model_agg` pre-aggregates (< 3 ms queries); two-tier TTL cleanup; raw OTLP events no longer written, keeping the DB bounded |
| **Ops CLI** | `start` / `stop` / `restart` / `status` / `serve` / `install` / `init` / `cleanup`; `/api/status` health panel; the 9-step cross-machine DB merge pipeline + online `VACUUM INTO` snapshots |
| **Distribution** | Single binary (`go:embed` web UI, zero runtime deps); Windows / macOS / Linux × amd64 / arm64 via GoReleaser; GitHub Actions three-OS tests + race + coverage; Claude Code plugin slash commands |
| **Upgrade** | A pre-v0.1.0 `~/.claude/cc-otel/` install is migrated wholesale to the standalone `~/.cc-otel/` on the new binary's first run, with zero data loss |

### Standalone install directory `~/.cc-otel/` (breaking, auto-migrated)

- The install/data directory moves from `~/.claude/cc-otel/` to **`~/.cc-otel/`** — cc-otel serves multiple clients and no longer lives inside Claude Code's data directory. Binary, config, DB, PID and log still share one directory.
- **Automatic migration**: when the new binary finds `~/.cc-otel/` missing and `~/.claude/cc-otel/` present on first run, the whole directory is renamed (config, database, logs included). Safety property: the only outcomes are "whole directory moved" or "nothing changed" — no path ever produces a fresh empty DB with the old data stranded.
  - Old daemon still running (files locked) → keep using the legacy directory this run with a log line; once stopped, the next run migrates.
  - Running the new binary from inside the legacy directory → migration skipped with a hint (Windows cannot rename a directory containing the running exe); `cc-otel install` re-resolves the directory after stopping the daemon so the migration retries.
  - A stray file at `~/.cc-otel` is never clobbered (Windows rename would silently replace files — explicitly guarded and pinned by tests).
- Upgrade: old binary `cc-otel stop` → new binary `cc-otel install && cc-otel start`.

### service_name attribution (new dimension)

- `api_requests` / `daily_model_agg` gain a `service_name` column (agg PK `(date, model, service_name)`); the startup migration rebuilds from existing rows and backfills empty values to `claude-code`. A partial index backs the backfill so startups no longer full-scan.
- `db.NormalizeServiceName` is the single normalization rule, applied identically by the receiver, the agg rebuild, InsertRequest, and dbmerge — a bare `''` is never stored.
- Frontend: request-log **Source column** with per-client brand badges. Deliberately **no** per-source query filter or chart dimension — non-Codex clients share one pool, and the main tab is renamed **All Clients**.

### OTLP/HTTP ingress on `:4318` + opencode (new client)

- `internal/receiver/http.go` serves the same three OTLP services over OTLP/HTTP via the identical Export methods — a protocol shim, not a second ingestion path. It answers in the encoding the client sent (JSON in → JSON out) and never returns 2xx for unstored data. `Content-Encoding: gzip` is decompressed transparently. Config `otlp_http_port` (default 4318; `0` = explicitly disabled, reported honestly by `/api/status`).
- **opencode** (Vercel AI SDK): usage spans are reshaped into `api_request` log records replayed through `logs.Export`, inheriting the same cost recompute, attribution, dedup and SSE. Only the leaf span is mapped (the parent repeats usage — mapping both double-counts); `request_id` is the hex span id, making retries idempotent; spans without `service.name` are attributed to `opencode` rather than the global default.

### Service-aware pricing

- Upstream `cost_usd` is authoritative only for **Claude Code on a Claude model** (`pricing.IsUpstreamAuthoritative`). Claude Code behind a GLM/DeepSeek proxy is mispriced upstream, and other clients report no cost at all. The receiver, the Codex path, and offline recompute share one rule, so backfills never rewrite rows the receiver deliberately kept.
- The registry stops special-casing Claude: Lookup/Upsert/the `pricing:` YAML all accept Claude entries — the only way to price a Claude model driven by a client that reports none.
- Embedded offline seed (manual_seed) refreshed to **2026-08-16 OpenRouter first-party quotes**: adds kimi-k3 ($3/$15), kimi-k2.7-code, glm-5.2 ($1.4/$4.4), glm-5.3 (inferred from 5.2 — no published price yet), qwen3.8-max, minimax-m3, dated DeepSeek V4 variants (flash-0731 / pro-0813); **corrects deepseek-v4-pro after DeepSeek's official cut** ($1.74 → $0.435/Mtok, 4x cheaper).

### DeepSeek Harness (DSH) ingestion (new client)

- DSH exports its whole session ledger over OTLP/HTTP. `internal/receiver/dsh.go` turns only usage-bearing `assistant/message` records into `api_requests`; everything else is dropped (FULL-mode bodies carry prompts, tool output, file contents). Step lifecycle rebuilds `duration_ms` / `ttft_ms`; `request_id` is `dsh-<session>-<seq>`, retry-idempotent.
- Detection is per **ResourceLogs** (scope prefix as the primary signal, `service.name` as fallback), so a collector batching several clients into one export cannot misroute.

### Code-review fix batch (max-level review, 12 confirmed findings fixed)

- **Data loss**: mixed-batch DSH routing became per-ResourceLogs dispatch (pinned by a mixed-batch-stores-both test); a missing `event.seq` falls back to the record timestamp for `request_id` instead of collapsing a whole session onto `-0`.
- **Tooling**: `tools/otlp_dump` reuses `receiver.ReadOTLPBody` (gzip decompression + size cap), unbreaking the DSH debugging workflow; the `AGENTS.md` symlink is now relative (the absolute target dangled on every clone); `import_global`'s `daily_model_agg` DDL matches `applyAggDeltas` again.
- **Honest behavior**: the ai-sdk clamp warning text corrected (the clamped value is already correct; the note is for onboarding confirmation); `otlp_http_port: 0` explicitly disables and reports truthfully.
- **Efficiency/maintainability**: DSH body decoding walks only the keys it reads (skipping multi-MB content); `'claude-code'` literals unified behind `db.DefaultServiceName`; the three copies of the aggregation SQL collapsed into one constant.
- **Frontend**: Source badge background follows `var(--accent-dim)` (light theme no longer paints the dark tint); tab active state unified to the graphite pattern; dead code and formatting damage cleaned up.

### All commits folded into v0.1.0 (46)

> Every commit from the initial one to the tagged one, grouped by iteration, newest first within each group. Note: mainline history was reworked once at the preview.15 release, so the old `v0.1.0-preview.1` – `.14` tags still point at pre-rework hashes; the hashes below are the current (reworked) mainline. Feature details per iteration live in the sections further down; the `merge: bring zip snapshot …` commits were the cross-machine collaboration workflow of the time and keep their original subjects for the record.

#### v0.1.0 stable (after preview.16, 4 commits)

| Commit | Subject | Notes |
|--------|---------|-------|
| `591959f` | chore(pricing): refresh manual seed from OpenRouter 2026-08-16 quotes | Offline seed refreshed to 2026-08-16 first-party quotes; deepseek-v4-pro corrected after the official cut ($1.74 → $0.435/Mtok) |
| `3d35df3` | feat(release): v0.1.0 — standalone ~/.cc-otel home, auto-migration, multi-client docs | Standalone `~/.cc-otel/` home + first-run auto-migration + four-client docs & changelog |
| `4a2d195` | feat(receiver): ingest DeepSeek Harness session telemetry | DSH session-ledger ingestion (fourth client), OTLP/HTTP + gzip |
| `4b754c4` | feat: service_name attribution, in-process OTLP/HTTP ingress, service-aware pricing | service_name attribution, `:4318` OTLP/HTTP ingress, opencode support, service-aware pricing |

#### v0.1.0-preview.16 (9 commits)

| Commit | Subject | Notes |
|--------|---------|-------|
| `3023d18` | docs(release): preview.16 — changelog + README for online DB import, Codex accounting, intraday bar chart | Release notes + README |
| `829be30` | feat(ui): rebuild Intraday as a per-bucket bar chart | Intraday rebuilt: one bar per (bucket, model), 5–60 min selector |
| `d1034d5` | feat(ui): graphite segmented control for chart granularity | Day/Month switch as a graphite segmented control (a11y included) |
| `36f0c19` | docs: add mandatory frontend taste guide | FRONTEND_TASTE.md visual guide |
| `b5d0661` | chore(db): clean legacy Codex events on startup | Startup background cleanup of legacy `codex_events` |
| `0be4537` | feat(codex): add structured accounting correlation | Codex streaming events correlated into per-request accounting rows |
| `1d7a8b4` | feat: add efficient online database import | Online DB import (`internal/dbmerge`, no process stop) |
| `dc664ec` | refactor(logrotate): extract closeFile helper to dedup close logic | Logrotate close-logic dedup |
| `b9daba9` | fix(pricing): guard redirectable URL vars to fix -race data race | Data-race fix on the suggest path |

#### v0.1.0-preview.15 (4 commits)

| Commit | Subject | Notes |
|--------|---------|-------|
| `498edfb` | docs(release): preview.15 — changelog summary + repo hygiene | Release notes + repo hygiene (.gitignore / .gitattributes) |
| `e1e8e5f` | feat(rate): Apple-style controls — custom rounded dropdown, drop Total tok/s, 10-min bucket | Rate controls redesigned; Total tok/s dropped; 10-min bucket added |
| `c4c12e2` | feat(pricing): manual pricing table with provider-aware OpenRouter suggest | Manual price-table management + provider-aware suggest |
| `45da52d` | cleanup: remove gemini cli telemetry integration and pricing | Gemini CLI integration removed |

#### v0.1.0-preview.14 (1 commit)

| Commit | Subject | Notes |
|--------|---------|-------|
| `a9cfadf` | feat(rate): preview.14 — Token Rate panel + rate APIs + weighted tok/s | New Rate panel, rate APIs, weighted tok/s |

#### v0.1.0-preview.13 (4 commits)

| Commit | Subject | Notes |
|--------|---------|-------|
| `c5d5342` | docs(changelog): preview.13 | Release notes |
| `4d79a7b` | feat(pricing): split hand-maintained manual seed out of regenerable seed.json | Manual prices split into `embed/manual_seed.json`, surviving regen |
| `bd4a8ac` | feat(pricing): prefer OpenRouter first-party provider price over blended | First-party provider price preferred over blended promo |
| `71786b3` | feat(pricing): basename fallback for provider-prefixed model keys | Bare model names match provider-prefixed keys by basename |

#### v0.1.0-preview.12 (2 commits)

| Commit | Subject | Notes |
|--------|---------|-------|
| `9e2a733` | docs(changelog): preview.12 | Release notes |
| `fb13ae0` | fix(web): align usage-calendar weekday labels with cell rows | Calendar weekday labels aligned with cell rows |

#### v0.1.0-preview.11 (2 commits)

| Commit | Subject | Notes |
|--------|---------|-------|
| `9cd2351` | docs(changelog): preview.11 | Release notes |
| `44fda62` | merge: bring zip snapshot cc-otel-9e41afc into main | Snapshot merged into main |

#### v0.1.0-preview.10 (3 commits)

| Commit | Subject | Notes |
|--------|---------|-------|
| `c4fb0a1` | docs(changelog): preview.10 — gemini source, merge data-loss fix, 24h time | Release notes |
| `82e1873` | fix(merge): stale import_ledger entries must not drop rows; verify per-row | Stale ledger entries no longer drop rows; per-row verification |
| `c4627ca` | merge: bring zip snapshot cc-otel.zip into main | Snapshot merged into main |

#### v0.1.0-preview.9 (1 commit)

| Commit | Subject | Notes |
|--------|---------|-------|
| `09afbdb` | fix: add no_proxy to OTEL config for proxy compatibility | `no_proxy` auto-injected; proxy users no longer lose telemetry |

#### v0.1.0-preview.8 (3 commits)

| Commit | Subject | Notes |
|--------|---------|-------|
| `4ee38c1` | merge: bring zip snapshot cc-otel-202f900 into main | Snapshot merged into main |
| `aef2685` | docs(changelog): split Codex duration backfill into online + offline paths | Docs: dual backfill paths |
| `db85e14` | merge: overlay zip snapshot cc-otel-202f900 onto main | Snapshot overlay merge |

#### v0.1.0-preview.7 (1 commit)

| Commit | Subject | Notes |
|--------|---------|-------|
| `e9593d3` | feat: pricing table & non-Claude cost recompute | Price table + non-Claude cost recompute |

#### v0.1.0-preview.6 (2 commits)

| Commit | Subject | Notes |
|--------|---------|-------|
| `668ba1d` | fix: skip Windows-only merge test on non-Windows CI | CI platform fix |
| `64fbc52` | docs: fix Codex config keys (trace-exporter / metrics-exporter) | Codex config-key docs fix |

#### v0.1.0-preview.5 (3 commits)

| Commit | Subject | Notes |
|--------|---------|-------|
| `f778c57` | chore: remove one-shot migration doc with local paths | Removed doc with machine-local paths |
| `039f18c` | merge: integrate enhanced head from secondary workstation | Secondary-workstation head merged |
| `f31b691` | docs: update dashboard screenshots | Screenshots updated |

#### v0.1.0-preview.4 (1 commit)

| Commit | Subject | Notes |
|--------|---------|-------|
| `69acbd0` | fix: remove legacy fallback in defaultDataDir | Removed legacy fallback in data-dir resolution |

#### v0.1.0-preview.3 (3 commits)

| Commit | Subject | Notes |
|--------|---------|-------|
| `6510f04` | feat: add hourly view, model family colors, insights modal, URL sync | Hourly view, model brand colors, insights modal, URL sync |
| `231f797` | docs: add dashboard screenshots | Screenshots |
| `a546051` | docs: add dashboard screenshots to README | README screenshots |

#### v0.1.0-preview.1 – .2 (3 commits)

| Commit | Subject | Notes |
|--------|---------|-------|
| `7291b00` | fix(setup): query GitHub API for actual download URL | Setup queries the GitHub API for the real download URL |
| `70060ca` | docs: add /reload-plugins step before /cc-otel:setup | Docs: /reload-plugins step |
| `23c25c3` | chore: initial commit | Initial commit: OTLP gRPC receiver + web dashboard |

## [v0.1.0-preview.16] - 2026-07-17

> Summarizes the 6 commits after `v0.1.0-preview.15`: online database import, Codex structured accounting correlation, startup cleanup of legacy Codex events, a frontend taste guide, a granularity segmented control, and Intraday rebuilt as a per-bucket bar chart.

### Online database import (new)

- Merge a `.db` file into the live primary database **without stopping the process or replacing any file**. The target stays online in WAL mode; the uploaded database is opened read-only and is never attached to the writable connection.
- The merge is **additive only** — existing rows are never deleted, overwritten, or updated; only logical records missing from the target are inserted, and duplicates keep the target's version. An import ledger makes re-upload / retry safe with no double writes.
- Adds `internal/dbmerge` as the single implementation for schema detection, natural-key identity, staging, batching, aggregation, and verification; the offline `tools/merge_bin_global` CLIs now delegate to it, so the offline and online paths can no longer drift apart.
- Performance: set-based work over staged digests, each batch bounded by 10,000 rows or ~128 MiB. End-to-end gates — preview ~550k rows within 2 minutes, import plus verify within 5 minutes. `codex_events` is recognized and skipped.
- Frontend: `js/import-db.js` drives upload → precheck → preview → confirm → progress → result over `/api/import{,/inspect,/start,/status}`.

### Codex structured accounting correlation

- Correlates Codex streaming events into **one structured accounting row per model request**: uncached input, cache-read, cache-write, output, and reasoning tokens are recorded separately, plus the upstream-reported total, TTFT, and the full model-call duration from request start through `response.completed` (excluding local tool execution). Cost is computed from four independently priced token categories.
- Correlation state is held in memory, dropped once the structured transaction commits, and abandoned state is evicted after 30 minutes.
- The field mapping is pinned against the upstream Codex source: note that `tool_token_count` carries total token usage (not a tool-token category) and must not be priced separately; `cache_write_token_count` comes from `input_tokens_details.cache_write_tokens` and defaults to 0 on older payloads.

### Startup cleanup of legacy Codex events

- After each daemon start, a background goroutine deletes legacy rows from the compatibility-only `codex_events` table in batches (≤ 10,000 rows per batch) until empty; on any DB error it logs and stops, leaving the remainder for the next start. The receiver no longer writes this table and imports skip it, so every remaining row is legacy by definition.
- Deliberately temporary: remove it in a later release once deployed databases have had enough starts to clean themselves.

### Frontend taste guide (FRONTEND_TASTE)

- Adds `.claude/harness/FRONTEND_TASTE.md` as the authoritative visual taste guide, required reading before any frontend design, implementation, modification, or review. It codifies the project's existing visual language: graphite / neutral gray by default, hierarchy through spacing and elevation before saturated color, compact controls over browser-native defaults, and dark / light as one system.

### Chart granularity segmented control

- The toolbar Day / Month granularity switch is restyled from browser-native buttons into a compact macOS-style graphite segmented control matching the app; styling is scoped to `#granularity-switch` so the Token Rate panel's Weighted / Avg control is untouched.
- Accessibility: adds `type="button"`, `role="group"`, and `aria-label`, and keeps `aria-pressed` in sync with the active state across all three paths (init / URL restore / click).
- Extracts `syncGranularityButtons` into `js/granularity.js` so the three call sites in filters.js share one implementation, eliminating the prior `aria-pressed` / visual drift.

### Intraday rebuilt as a per-bucket bar chart

- The Intraday view moves from a per-model line chart plus detail table to **one bar per (bucket, model)** with a 5 / 10 / 15 / 30 / 60-minute selector, spanning up to 7 days; the detail table is gone.
- Backend: all four intraday bucket whitelists (Claude repo + handler, Codex repo + handler) previously hardcoded 15 / 30 / 60 and silently coerced 5 and 10 to 30; they now reuse `db.ValidRateBucketMinutes` (the SQL already supported any bucket size — purely a validation-layer fix), with the first tests for this path.
- Frontend: three blocks that each had one hand-rolled copy are extracted into shared modules — `bucket-dropdown.js`, `makeTokenBarColor` in `chart-main.js`, and `legend-focus.js`; `intraday-slots.js` gap-fills empty buckets so a category axis doesn't place disjoint timestamps side by side. The default zoom window is now a duration (16h, right-anchored) instead of drifting with the bucket size.
- Obeys the chart rules pinned in CLAUDE.md: no stacking, `trigger: 'item'`, no series-focus emphasis, one bar per (time, model); the x-axis ascends (a time-of-day axis reads left → right, deliberately opposite to chart-main's newest-first ordering).

## [v0.1.0-preview.15] - 2026-07-09

> Summarizes the 4 commits after `v0.1.0-preview.14`: Gemini removal, repo hygiene, manual pricing + per-provider suggest, Token Rate control redesign.

### Removed Gemini CLI source

- Removed the full Gemini CLI integration: `gemini_*` tables and schema, `/api/gemini/*` routes, the frontend `?source=gemini` view, `skills/otel-setup/gemini-setup.md`, and its dedicated pricing.
- **Why**: real usage is concentrated on Claude Code and Codex. Maintaining a separate schema / API / docs / pricing branch for a rarely-used source cost more than it returned. The project now converges on a clean two-source model (the stale "three sources" wording still lingering in the historical section below is superseded by this section).

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

## [Pre-v0.1.0] · 0.1.0 Preview era (historical record)

> **Status: historical record, folded into the v0.1.0 release** (see the section
> at the top). This section is the accumulated feature description of the preview
> era (`v0.1.0-preview.1` – `.15`), kept for reference; where it conflicts with
> the versioned sections above, those win. The install dir `~/.claude/cc-otel/`,
> the 24h price auto-refresh, and the three-source (Gemini) model mentioned here
> were the behavior of their time and were each superseded later (since v0.1.0
> the dir is `~/.cc-otel/`, auto-refresh is gone, Gemini was removed).

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
