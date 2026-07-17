# Codex Event Retention Design

## Context

Codex usage accounting is assembled from multiple OTLP events. A
`codex.api_request` creates the request row, and
`codex.sse_event` with `event.kind=response.completed` fills in tokens, cost,
cache usage, and (when available) duration. That correlation must remain
unchanged.

The current receiver also persists non-completion SSE events and generic Codex
diagnostic events in `codex_events`. A consistent snapshot of the global
database contained 2,043,251 rows in that table. Of those, 2,025,914 were
non-completion `codex.sse_event` rows, dominated by streaming delta events.
These rows are not read by any dashboard or API endpoint.

On the snapshot, clearing only `codex_events` and running `VACUUM` reduced the
database from 664.85 MiB to 194.95 MiB. Checksums of request counts, tokens,
cost, duration, TTFT, and daily aggregates were unchanged, and SQLite
`quick_check` still returned `ok`.

## Decision

`codex_events` becomes a compatibility-only table:

- Keep the table and its schema so existing databases and migrations remain
  compatible.
- Stop writing new rows to it.
- Recognize but ignore it during database import.
- Continue receiving and processing all Codex telemetry needed for accounting.
- Do not automatically delete existing rows from any database.

This separates event processing from event retention. An event may still drive
an update to a structured request row without being stored as a generic event.

## Receiver Behavior

The following paths remain authoritative and unchanged:

- `codex.api_request` inserts `codex_api_requests` and increments the matching
  `codex_daily_model_agg` request count.
- `codex.sse_event:response.completed` updates or inserts a
  `codex_api_requests` row and applies token/cost deltas to
  `codex_daily_model_agg`.
- `codex.websocket_request` continues to seed the in-memory span tracker.
- `codex.websocket_event` continues to update TTFT and duration through the
  in-memory tracker.
- Structured user-prompt and tool events continue to use
  `codex_user_prompt_events`, `codex_tool_decision_events`, and
  `codex_tool_result_events`.

The following writes stop:

- Non-completion `codex.sse_event` rows are no longer inserted into
  `codex_events`.
- `codex.websocket_request` is no longer duplicated into `codex_events` after
  its tracker state is recorded.
- Other generic `codex.*` fallback events are no longer inserted into
  `codex_events`.

No accounting data is sourced from persisted `codex_events` at runtime. The
historical migration that once used stored `codex.websocket_event` rows remains
available for old databases, but current receiver behavior does not depend on
that migration during normal operation.

## Import Behavior

Remove `codex_events` from the importable detail-table registry and add it to
the recognized ignored-table registry. Schema validation still verifies its
known columns when it is present, and preview results list it under ignored
tables.

The importer continues to merge:

- `codex_api_requests`
- `codex_user_prompt_events`
- `codex_tool_decision_events`
- `codex_tool_result_events`

It continues to ignore `codex_daily_model_agg` because aggregate deltas are
derived from inserted request rows. `codex_raw_otlp_events` remains importable
under the existing database-import scope; changing raw OTLP retention is not
part of this design.

For the analyzed global snapshot, ignoring `codex_events` reduces candidate
detail rows from 2,586,395 to 543,144 without dropping request/accounting data.

## Existing Data

This change does not clean a live database. Existing `codex_events` rows remain
until the user explicitly authorizes a separate maintenance operation.

A future cleanup must:

1. operate on a WAL-consistent snapshot first;
2. compare Codex request and aggregate checksums before and after;
3. preserve the empty `codex_events` table for compatibility;
4. run `quick_check` after cleanup; and
5. require a separately approved production deployment or file swap before it
   can reclaim physical disk space.

## Error Handling

Stopping diagnostic persistence must not turn otherwise valid telemetry into
an error. Accounting-path errors retain their current behavior. Ignored generic
events return without a database write or dashboard notification.

Import validation still rejects unknown columns or unknown tables. A recognized
`codex_events` table with the expected schema is reported as ignored rather
than imported.

## Tests

Add regression coverage proving:

- a non-completion SSE event does not create a `codex_events` row;
- a completion SSE event still updates request tokens, cost, and aggregates;
- WebSocket timing still updates TTFT/duration without persisting a generic
  event;
- generic Codex fallback events do not create rows;
- `codex_events` is absent from `ImportSpecs` and present in ignored tables;
- inspection and import counts exclude `codex_events`; and
- existing merge, receiver, API, race, frontend, and vet suites still pass.

## Out of Scope

- Automatically deleting or vacuuming the production database.
- Disabling structured Codex request, prompt, or tool tables.
- Changing Codex token, cache, cost, duration, or TTFT correlation semantics.
- General batch-performance changes for the remaining import tables.
