# Codex Structured Accounting Correlation Design

## Status

Approved in conversation on 2026-07-17.

This specification extends, rather than replaces,
`docs/superpowers/specs/2026-07-16-codex-event-retention-design.md`. The earlier
decision remains binding: live telemetry must not persist generic rows in
`codex_events`.

All source locations below refer to commit `d0eceb0` in
`D:\goProject\cc-otel` unless another repository and commit are named
explicitly. Each location includes both current line numbers and a stable
symbol or nearby text anchor so an implementer can relocate it after earlier
edits shift later lines.

## Objective

For every Codex model request, retain only structured accounting data while
correctly recording:

- total input tokens;
- uncached input tokens;
- cache-read tokens;
- cache-write tokens;
- output tokens;
- reasoning output tokens;
- upstream-reported total tokens;
- cost using four independently priced token categories;
- TTFT reported by Codex; and
- full model-call duration from request start through `response.completed`,
  excluding local tool execution.

Generic Codex diagnostic and streaming events remain transient. The receiver
may hold correlation state in memory, but it must remove completed state after
the structured database transaction succeeds and evict abandoned state after
30 minutes.

## Authoritative Upstream Contract

The local OpenAI Codex source checkout is
`D:\RustProject\codex`, commit `315195492c`.

### Completion token fields

`codex-rs/otel/src/events/session_telemetry.rs:926-944`, method
`SessionTelemetry::sse_event_completed`, emits:

| OTEL attribute | Codex value | cc-otel destination |
|---|---|---|
| `input_token_count` | `usage.input_tokens` | `input_tokens` |
| `output_token_count` | `usage.output_tokens` | `output_tokens` |
| `cached_token_count` | `usage.cached_input_tokens` | `cache_read_tokens` |
| `cache_write_token_count` | `usage.cache_write_input_tokens` | `cache_creation_tokens` |
| `reasoning_token_count` | `usage.reasoning_output_tokens` | `reasoning_tokens` |
| `tool_token_count` | `usage.total_tokens` | `total_tokens` |
| `ttft_ms` | measured TTFT or absent | `ttft_ms` |

Despite its upstream attribute name, `tool_token_count` is total token usage;
it is not a tool-token category. Do not rename the OTEL input attribute and do
not price it separately.

`codex-rs/codex-api/src/sse/responses.rs:121-153`, conversion from
`ResponseCompletedUsage` to `TokenUsage`, proves that
`cache_write_token_count` originates from
`response.usage.input_tokens_details.cache_write_tokens` and defaults to zero
for older payloads.

`codex-rs/protocol/src/protocol.rs:2046-2061`, struct `TokenUsage`, confirms
that `input_tokens`, cached input, cache-write input, output, reasoning output,
and total are independent structured fields.

### TTFT boundary

`codex-rs/core/src/client.rs:1942-2021`, function `map_response_events`, starts
the TTFT clock when response-stream processing starts, records TTFT on the
first `ResponseEvent::OutputItemAdded`, and passes the value to
`sse_event_completed` when `ResponseEvent::Completed` arrives. cc-otel must use
the emitted `ttft_ms` when it is positive. WebSocket `response.created`
timing may remain only as a compatibility fallback when direct TTFT is absent.

### Duration boundaries

The following upstream values are not the desired full model-call duration:

- `codex-rs/otel/src/events/session_telemetry.rs:512-529,580-586`,
  `SessionTelemetry::log_request` starts its timer immediately before the
  transport future and emits `codex.api_request` after that future returns;
  its `duration_ms` therefore reconstructs the transport-attempt start but
  does not include later stream consumption.
- `codex-rs/codex-api/src/telemetry.rs:68-97`,
  `run_with_request_telemetry`, measures `send(req).await`; for streaming HTTP
  this ends when the streaming response is established, before
  `response.completed`.
- `codex-rs/codex-api/src/sse/responses.rs:492-508`,
  `process_sse_with_treatment`, measures one wait/poll between SSE events.
- `codex-rs/codex-api/src/endpoint/responses_websocket.rs:855-883`,
  `send_websocket_request`, measures only the WebSocket send operation.

Therefore, cc-otel must reconstruct request start as:

```text
request_start_nanos = request_event_observed_nanos
                    - request_event_duration_ms * 1_000_000
```

and compute the final duration as:

```text
duration_ms = max((completion_observed_nanos - request_start_nanos) / 1_000_000, 0)
```

The final boundary is one model request through its own
`response.completed`. Local tool execution occurs after one model completion
and before the next model request, so it is excluded naturally.

Timing calculations must use a new helper beside
`internal/receiver/codex_parser.go:61-71`, named
`codexLogObservedUnixNanos`. It returns `ObservedTimeUnixNano` first,
`TimeUnixNano` second, and zero when neither exists. Keep the existing
`codexLogUnixNanos` ordering for the persisted event timestamp; changing
historical timestamp semantics is out of scope. All `request_start_nanos`,
TTFT fallback, and full-duration calculations in this design use the new
observed-time helper and fall back to `ts.UnixNano()` only when it returns
zero.

## Token and Pricing Semantics

Codex `input_token_count` already includes both cache-read and cache-write
input. The receiver must never add either cache category to Codex input again.

```text
input_side     = input_token_count
cache_read     = cached_token_count
cache_write    = cache_write_token_count
uncached_input = max(input_side - cache_read - cache_write, 0)
output         = output_token_count
reported_total = tool_token_count
fallback_total = input_side + output
```

Cost must use all four price categories:

```text
cost = uncached_input * input_price
     + output         * output_price
     + cache_read     * cache_read_price
     + cache_write    * cache_create_price
```

The pricing engine already implements this contract in
`internal/pricing/pricing.go:114-129`, method `Entry.Calc`, and
`internal/pricing/registry.go:210-217`, method `sqlRegistry.Calc`. Those files
do not require behavioral changes.

## Architecture

### Receiver-owned in-memory correlation

The existing receiver owns one process-wide tracker at
`internal/receiver/receiver.go:47-53`, field
`logsServiceServer.codexTracker`, initialized at
`internal/receiver/receiver.go:156` by `newCodexSpanTracker` and passed to every
Codex log dispatch at `internal/receiver/receiver.go:66`.

Keep this ownership model. Do not add a database pending table or a second
background service.

Extend `internal/receiver/codex_parser.go:74-154`, types
`codexSpanTracker`/`spanInfo` and their methods, so one entry can carry:

```go
type spanInfo struct {
    rowID      int64
    sessionID  string
    model      string
    startNanos int64
}
```

The tracker contract is:

1. Primary lookup key: OTLP `span_id` returned by `spanIDFromLog` at
   `internal/receiver/codex_parser.go:53-59`.
2. `recordRequest` must also retain entries whose OTLP span ID is empty. Under
   the tracker mutex, increment a tracker-local `uint64` sequence and use a
   non-base64 key such as `fallback:<sequence>`; this key is internal and is
   never persisted.
3. Compatibility fallback: newest eligible entry matching
   `sessionID + model`, not newer than completion, within 10 minutes.
4. A lookup for completion must not delete state before the structured
   database write succeeds.
5. Successful finalization deletes exactly the matched internal map key.
6. Entries older than 30 minutes are evicted during every subsequent record
   operation, even when the map has 100 or fewer entries; retain the existing
   mutex protection.

Replace the current destructive API with these explicit operations (the plan
and implementation must use these signatures unless compilation requires only
a mechanical type-name adjustment):

```go
func (t *codexSpanTracker) recordRequest(spanID string, info spanInfo) string
func (t *codexSpanTracker) lookupRequest(
    spanID, sessionID, model string,
    observedNanos int64,
    maxAge time.Duration,
) (key string, info spanInfo, ok bool)
func (t *codexSpanTracker) removeRequest(key string)
```

`recordRequest` returns the internal map key. `lookupRequest` tries the exact
span key first, then the newest fallback candidate, and never mutates the map.
`removeRequest` is a no-op for an empty or missing key. Delete current
`peekRequest`, `popRequest`, and `popLatestRequest` at lines 110-153 after all
callers move to this API.

### `codex.api_request`

Modify `internal/receiver/codex_parser.go:188-213`, switch case
`"codex.api_request"`.

1. Parse the currently reported `duration_ms` into a local
   `reportedDurationMs`.
2. Define a pending successful stream as:

```text
error.message is empty AND
(http.response.status_code is 0 OR status is in [200, 299])
```

3. For a pending successful stream, insert `codex_api_requests.duration_ms = 0`.
   The reported duration is setup/header time, not the accepted final metric.
4. For a failed request, preserve `reportedDurationMs` in the structured row,
   because no completion event is expected.
5. Capture the row ID returned by `InsertCodexAPIRequest`.
6. For a pending successful stream and a non-nil tracker, record correlation
   state with:

```text
rowID = inserted row ID
startNanos = event observed nanos - reportedDurationMs * 1_000_000
```

7. Clamp impossible negative start values to the event timestamp rather than
   storing a timestamp before the Unix epoch.
8. Record the entry even when `spanIDFromLog` returns empty, so later
   session/model fallback can still produce the accepted full duration.

### `codex.websocket_request`

Modify `internal/receiver/codex_parser.go:330-339`, switch case
`"codex.websocket_request"`.

Record the same reconstructed start boundary using the event's
`duration_ms`. A WebSocket request may not yet have an inserted request row, so
`rowID` may be zero. Preserve session/model fallback correlation.

If an entry for the same non-empty span already has `rowID > 0`, recording the
WebSocket event must merge timing metadata without replacing that row ID with
zero. If no entry exists, create one normally, including for an empty span via
the internal fallback key.

Do not insert a generic event row and do not insert a duration-only request row
at request-send time.

### `codex.sse_event:response.completed`

Modify `internal/receiver/codex_parser.go:215-267`, switch case
`"codex.sse_event"`.

Build `db.CodexTokenUpdate` from these exact attributes:

```text
InputTokens         <- input_token_count
OutputTokens        <- output_token_count
CacheReadTokens     <- cached_token_count
CacheCreationTokens <- cache_write_token_count
ReasoningTokens     <- reasoning_token_count
TotalTokens         <- tool_token_count
TTFTMs              <- ttft_ms
```

When a tracker match exists, retain the returned tracker map key, set
`RequestRowID`, and compute full `DurationMs`. Calculate cost only after every
token category is populated:

```go
uncachedInput := upd.InputTokens - upd.CacheReadTokens - upd.CacheCreationTokens
if uncachedInput < 0 {
    uncachedInput = 0
}
upd.CostUSD = pricer.Calc(
    ctx,
    upd.Model,
    uncachedInput,
    upd.OutputTokens,
    upd.CacheReadTokens,
    upd.CacheCreationTokens,
)
```

Call one repository operation that writes tokens, cache-write usage, cost,
direct TTFT, and full duration in one transaction. Remove tracker state only
after this operation commits successfully, using the exact map key returned by
`lookupRequest`. If it fails, return failure and retain the entry.

If no tracker match exists, still persist tokens, cache-write usage, cost, and
direct TTFT through the existing newest-pending-row/fallback-insert behavior.
Set full duration to zero; do not substitute per-event SSE poll duration.

### `codex.websocket_event`

Modify `internal/receiver/codex_parser.go:341-390`, switch case
`"codex.websocket_event"`.

- `response.created` may populate a fallback TTFT only when direct TTFT has not
  yet been received. When the tracker entry has `rowID > 0`, update that exact
  row; otherwise retain the current session/model-within-10-minutes fallback.
  Direct `response.completed.ttft_ms` is authoritative and may overwrite this
  fallback.
- `response.completed` may compute and persist a full-duration candidate, but
  when `rowID > 0` it must target that exact session/model row before using the
  current session/model fallback. It must not delete tracker state. The later
  structured SSE completion needs the same state for exact row correlation and
  authoritative token/TTFT write.
- Never insert into `codex_events`.

## Repository Contract

### Update payload

Modify `internal/db/codex_types.go:43-54`, type `CodexTokenUpdate`, adding:

```go
RequestRowID        int64
CacheCreationTokens int64
TTFTMs              int64
```

`CodexAPIRequest` already contains `CacheCreationTokens`, `DurationMs`, and
`TTFTMs` at `internal/db/codex_types.go:9-24`; no schema-facing request field is
missing.

### Exact-row-first update

Modify `internal/db/codex_repository.go:98-177`, method
`UpdateCodexAPIRequestTokens`.

Selection rules:

1. If `RequestRowID > 0`, attempt the exact row first.
2. Query the exact row with `id = RequestRowID AND session_id = SessionID AND
   model = Model`, returning its timestamp and all six token columns. A missing
   or mismatched exact row continues to rule 4; it must never update a row for
   another session or model.
3. The exact row update must only apply token/aggregate deltas once. If any of
   `input_tokens`, `output_tokens`, `cache_read_tokens`,
   `cache_creation_tokens`, `reasoning_tokens`, or `total_tokens` is non-zero,
   commit without mutation and return `true, nil`; do not add daily aggregate
   deltas again and do not create a fallback row.
4. If there is no usable exact row, retain the current newest zero-token row
   match on `session_id + model` within five minutes at current lines 117-124.
5. If no pending row exists, retain the current fallback insert at lines
   126-148.

`UpdateCodexAPIRequestTokens` keeps its current `(bool, error)` signature.
`true` means the completion was handled by an exact/fallback UPDATE or was an
idempotent exact-row no-op; `false` means the method performed the fallback
INSERT. Either successful value allows the receiver to remove matched tracker
state.

The request-row update at current lines 154-163 must write the following SQL
columns in this exact logical order:

```text
input_tokens
output_tokens
cache_read_tokens
cache_creation_tokens
reasoning_tokens
total_tokens
cost_usd
duration_ms
ttft_ms
```

For positive completion values, full duration and direct TTFT are
authoritative:

```sql
duration_ms = CASE WHEN ? > 0 THEN ? ELSE duration_ms END,
ttft_ms     = CASE WHEN ? > 0 THEN ? ELSE ttft_ms END
```

Do not keep the current `duration_ms = 0` guard at line 158, because an older
setup/header duration must be replaceable by the accepted full duration.

### WebSocket fallback timing helpers

Modify `internal/db/codex_repository.go:221-267`, current methods
`UpdateCodexRequestDurationBySession` and `UpdateCodexRequestTTFT`, to accept a
leading `requestRowID int64` argument. Rename the duration method to
`UpdateCodexRequestDuration` because it is no longer session-only.

For `requestRowID > 0`, each helper first targets:

```sql
WHERE id = ? AND session_id = ? AND model = ?
```

The duration fallback only replaces zero duration; the TTFT fallback only
replaces zero/null TTFT. If the exact identity exists but already has a
positive value, treat it as handled and do not update a different row. If the
exact identity does not exist, retain the current newest-row lookup within 10
minutes. A duration helper returns `true` when an exact identity was handled or
a fallback row was updated; only a genuine no-match permits the existing
WebSocket duration-only fallback insert in `codex_parser.go:369-381`.

### Daily aggregate parameter order

Modify `internal/db/repository.go:246-256`, prepared statement
`stmtUpsCodexAggToks`.

Replace the literal cache-creation zero in current line 249 with a bound
parameter and add this conflict update:

```sql
cache_creation_tokens = cache_creation_tokens + excluded.cache_creation_tokens
```

The bound token-delta order must be:

```text
date, model,
input, output, cache_read, cache_creation,
reasoning, total, cost
```

Update the call at `internal/db/codex_repository.go:167-173` to pass the same
order. The request count remains zero for this token-delta upsert.

No table or column migration is required; `codex_api_requests` and
`codex_daily_model_agg` already contain `cache_creation_tokens`.

## Frontend Contract

### Shared token math

Modify `internal/web/static/js/token-math.js:3-30`, function `tokenParts`.

The Codex branch at current lines 10-19 must return:

```js
const uncachedInput = Math.max(input - cacheRead - cacheCreate, 0);
return {
    inputSide: input,
    uncachedInput,
    cacheRead,
    cacheCreate,
    output,
    total: reportedTotal > 0 ? reportedTotal : input + output,
};
```

Keep `cacheHitParts` at lines 33-45 using Codex denominator
`input_tokens`; cache write does not change the cache-hit numerator.

### Stop hiding Cache Create for Codex

The HTML already has Cache Create headers at:

- `internal/web/static/index.html:140-152`, daily detail;
- `internal/web/static/index.html:169-181`, intraday detail; and
- `internal/web/static/index.html:230-242`, request detail.

Do not change those headers. Remove source-specific hiding/skipping at these
locations:

- `internal/web/static/app.js:120-135`, `syncSourceTabsUI`: do not hide
  `.col-cache-create` and keep Input `colspan="3"` for both sources.
- `internal/web/static/js/chart-main.js:23-27`, `buildBarTooltip`: always render
  Cache Create.
- `internal/web/static/js/panel-daily.js:83-89`, intraday tooltip: always render
  Cache Create.
- `internal/web/static/js/panel-daily.js:145-155`, intraday rows: always emit
  the Cache Create cell.
- `internal/web/static/js/panel-daily.js:422-430`, daily rows: always emit the
  Cache Create cell. This block must call `tokenParts(r)` and render
  `parts.uncachedInput`, `parts.cacheRead`, `parts.cacheCreate`, and
  `parts.output`; rendering raw `r.input_tokens` under the `Uncached` header is
  incorrect for Codex because that field is the total input side.
- `internal/web/static/js/panel-requests.js:121-130`, request rows: always emit
  the Cache Create cell.

Where intraday empty/error rows currently use `colspan="9"` while the visible
table has ten columns (`panel-daily.js` current lines 120, 138, and 405), change
them to `10`. Daily and request empty-row colspans already match their visible
column counts and should remain unchanged.

## Failure and Compatibility Behavior

- Older Codex clients omit `cache_write_token_count`; parse it as zero.
- Missing/non-positive direct TTFT leaves the existing fallback or zero.
- Missing request correlation never blocks token/cost/TTFT persistence.
- Missing request correlation yields `duration_ms = 0`; never invent a full
  duration from one SSE poll.
- Failed API requests retain the upstream request duration and do not wait for
  completion.
- Database failure retains tracker state; a successful structured commit
  removes it.
- Tracker expiry is 30 minutes.
- Structured tool results continue to use
  `codex_tool_result_events.duration_ms`; no tool duration is added to request
  duration.
- Existing historical rows are not rewritten. Historical cache-write values
  cannot be reconstructed once their source telemetry is unavailable.
- `codex_events` remains an empty compatibility table and remains ignored by
  database import.

## Documentation Changes

Update `docs/otel-events.md:153-284`, the Codex event reference:

- add `cache_write_token_count` and `ttft_ms` to completion attributes;
- change uncached input to
  `max(input - cache_read - cache_write, 0)`;
- document four-category pricing;
- document direct completion TTFT as authoritative;
- document full duration reconstruction and transient tracker lifecycle; and
- preserve the statement that `codex_events` is compatibility-only.

## Test Design

All implementation must follow test-driven development: add the failing test,
run it and observe the expected failure, implement the smallest production
change, then rerun it green.

### Receiver tests

Extend `internal/receiver/codex_parser_test.go`, whose current receiver tests
occupy lines 34-283.

1. Replace/expand `TestDispatchCodexLog_SSECompletedUpdatesTokens`
   (current lines 63-107) with a completion containing:

```text
input_token_count       = 100
cached_token_count      = 40
cache_write_token_count = 20
output_token_count      = 10
reasoning_token_count   = 5
tool_token_count        = 110
ttft_ms                 = 250
```

Assert the request row has `100, 40, 20, 10, 5, 110, 250` in the matching
columns and that the aggregate contains cache creation `20`.

2. Add a recording pricer and assert its arguments are exactly:

```text
input=40, output=10, cacheRead=40, cacheCreate=20
```

3. Add an HTTP correlation test: API event observed at `T+300ms` with
`duration_ms=300`, completion observed at `T+2300ms`, same span. Assert stored
duration is `2300`, not `300` or `2000`, and TTFT is the direct completion
value.

4. Add a successful-header-only test: after only a successful API event,
stored duration is `0`.

5. Add a failed-request test: non-2xx/error API event retains its reported
duration.

6. Add tracker lifecycle tests proving lookup is non-destructive, explicit
removal deletes only the matched key, and 30-minute eviction works even below
100 entries. Add a dispatch failure test by closing the in-memory test database
after recording a request and before completion; assert the completion returns
false and `lookupRequest` still finds the same key. No production interface is
widened for fault injection.

7. Retain `TestDispatchCodexLog_DiagnosticEventsAreNotPersisted` at current
lines 255-283 and assert `codex_events` remains zero.

8. Adjust existing WebSocket timing tests at current lines 109-244 so
`response.completed` no longer prematurely consumes state before structured
SSE completion.
9. Add an empty-span correlation test proving a successful API event is kept
under an internal fallback key and its completion receives full duration by
`sessionID + model`.

### Repository tests

Extend `internal/db/codex_repository_test.go`:

1. `TestUpdateCodexAPIRequestTokens_UpdatesNewestZeroTokenRow`, current lines
   92-142: add cache creation, TTFT, and full duration; assert the exact row.
2. `readCodexAgg`, current lines 213-222: return
   `cache_creation_tokens` in addition to existing fields.
3. `TestCodexAgg_InsertAndUpdate_PopulatesAggExactlyOnce`, current lines
   224-274: assert cache creation is included and request count stays `1`.
4. Add an exact-row test with two pending rows sharing session/model. Supply
   `RequestRowID` for the older row and assert only that row receives tokens.
5. Add a repeated exact completion test proving daily aggregate deltas are
   applied once and no fallback row is inserted.
6. Preserve fallback-insert coverage at current lines 276-300, adding
   cache-write and direct TTFT assertions.
7. Add exact-row WebSocket timing coverage for the helpers at current lines
   221-267: two rows share session/model, and duration/TTFT update only the
   supplied row ID.

### Frontend tests

1. Update `internal/web/static/tests/token-math.test.mjs:28-36` and import
   `tokenParts` as well as `cacheHitParts`. For Codex input `100`, cache read
   `40`, cache create `20`, output `10`, total `110`, assert:

```text
inputSide=100, uncachedInput=40, cacheRead=40,
cacheCreate=20, output=10, total=110
```

2. Extend `internal/web/static/tests/panel-requests.test.mjs:62-121` so the
Codex request fixture has non-zero `cache_creation_tokens` and the rendered row
contains it.

3. Add `internal/web/static/tests/panel-daily.test.mjs` around an exported pure
row-rendering helper. With Codex input `100`, cache read `40`, cache create
`20`, and output `10`, assert the daily row renders Uncached `40` rather than
raw input `100`, and renders Cache Create `20`.

4. Add `internal/web/static/tests/chart-main.test.mjs` for the already-exported
`buildBarTooltip`, and extend `panel-daily.test.mjs` for an exported intraday
tooltip helper. Both Codex outputs must contain `Cache Create` and its formatted
value. Keep these DOM-free, following the current ESM testing convention in
`.claude/CLAUDE.md`.

## Verification Commands and Expected Evidence

Run from `D:\goProject\cc-otel` unless a command says otherwise.

1. Focused red/green tests during implementation:

```powershell
go test ./internal/receiver -run Codex -count=1
go test ./internal/db -run Codex -count=1
node --test internal/web/static/tests/token-math.test.mjs internal/web/static/tests/panel-requests.test.mjs internal/web/static/tests/panel-daily.test.mjs internal/web/static/tests/chart-main.test.mjs
```

2. Race-sensitive receiver/repository verification:

```powershell
go test -race ./internal/receiver ./internal/db
```

3. Full repository gates:

```powershell
go test ./...
go vet ./...
node --test internal/web/static/tests/*.test.mjs
make build
```

Every command must exit `0` with no failed tests. `make build` must produce
`bin/cc-otel.exe`.

4. Development runtime only; never use production ports for this validation:

```powershell
bin/cc-otel.exe stop -config bin/cc-otel-dev.yaml
bin/cc-otel.exe start -config bin/cc-otel-dev.yaml
```

Verify `bin/cc-otel-dev.yaml` uses OTLP `14317`, Web `18899`, and a `bin/`
database before starting.

5. API and browser verification:

- `GET http://localhost:18899/api/status` reports healthy DB/API and OTLP
  listener.
- `GET http://localhost:18899/api/codex/requests?...` returns
  `cache_creation_tokens`, direct `ttft_ms`, and full `duration_ms`.
- Open `http://localhost:18899/?source=codex&v=<cache-buster>`.
- Confirm Daily, Intraday, Request, and chart tooltip views all show Cache
  Create.
- Confirm `Input = Uncached + Cache Read + Cache Create` for a synthetic Codex
  row and `Total = Input + Output` when reported total follows the upstream
  contract.
- Capture at least one screenshot including the Codex source tab, selected
  date range, and visible Cache Create values.
- Query the development database and confirm `SELECT COUNT(*) FROM
  codex_events` is `0` after synthetic telemetry.

Production deployment or production database mutation is not part of this
specification and requires a separate explicit instruction.

## Acceptance Criteria

The change is complete only when all of the following are true:

1. New Codex cache-write telemetry reaches both request and daily aggregate
   structured columns.
2. Cost receives uncached input, output, cache read, and cache write as four
   independent quantities.
3. `tool_token_count` remains mapped to reported total tokens.
4. Positive completion TTFT is stored directly.
5. Successful request duration spans reconstructed request start through
   completion and replaces setup/header duration.
6. Local tool execution time is not part of request duration.
7. Exact row ID correlation wins over session/model fallback.
8. Completion state is removed only after successful structured persistence;
   abandoned state expires after 30 minutes.
9. Older Codex clients without cache-write telemetry continue to work with a
   zero value.
10. Codex Cache Create is visible consistently in all relevant UI views.
11. `codex_events` remains empty during live telemetry and ignored during
    import.
12. No database schema migration or historical rewrite occurs.
13. All verification commands and the required browser screenshot succeed.

## Out of Scope

- Recovering cache-write usage from historical rows that never received it.
- Recomputing historical Codex costs.
- Changing pricing table values or pricing-source precedence.
- Persisting generic Codex streaming events.
- Adding a durable pending-correlation table.
- Changing Claude Code accounting semantics.
- Deploying to or restarting the production `4317/8899` instance.
