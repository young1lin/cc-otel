# Database Import Performance Design

## Context

The online SQLite importer is correct but too slow at production-scale row
counts. A representative uploaded database contains roughly 550,000 importable
rows after `codex_events` is recognized and ignored. The current implementation
uses a maximum batch size of 500, but batching only controls transaction
boundaries; SQL execution remains row-oriented.

Each unique source row currently causes most of the following work:

- inspection writes one digest to a scratch database and runs one target
  `SELECT ... LIMIT 1`;
- import runs one target existence query, one detail insert, and one ledger
  insert;
- completion verification scans the source again, writes another scratch
  digest, and runs another target existence query.

For approximately 550,000 rows this produces several million separately
prepared/executed SQLite statements and three complete source scans. Raising
the old 500-row limit alone would reduce commits but would not remove this main
cost.

## Goals

- Preview approximately 550,000 importable rows in at most 2 minutes on the
  current development machine.
- Import and verify approximately 550,000 rows in at most 5 minutes on the
  current development machine.
- Preserve exact natural-key, NULL, target-wins, aggregate, and ledger
  semantics.
- Keep the running target database online in WAL mode while importing.
- Keep the uploaded SQLite database immutable and never attach it to the
  writable target connection.
- Keep retries safe after any committed batch or current-batch failure.

These are end-to-end gates rather than microbenchmark-only targets. The final
report must include measured preview, all-new import, and all-duplicate retry
times using a disposable copy, never the production database.

## Non-goals

- No production cleanup, `VACUUM`, database replacement, or service stop.
- No change to the 2 GiB upload limit or accepted schema variants.
- No pricing recomputation.
- No persistent identity/hash columns or new natural-key indexes in user data
  tables.
- No `ATTACH` of an uploaded database to the main writable connection.

## Decision

Use homogeneous, set-oriented staging batches with two simultaneous limits:

- at most 10,000 source rows; and
- at most 128 MiB of estimated row payload.

This decision supersedes every 500-row maximum in the original database-import
design and implementation. Atomic detail, aggregate, and ledger semantics remain
unchanged; only the logical batch boundary and execution strategy change.

The first limit reached flushes the batch. A table boundary also flushes the
batch, so one logical batch always contains rows from one registry table. A
single row whose payload exceeds 128 MiB is accepted as a one-row batch because
the existing importer has no independent row-size limit.

The 128 MiB value is a batch-payload limit, not a hard process-RSS limit. Row
size estimation includes string/blob byte lengths plus conservative fixed
overhead for the row, map entries, and scalar values. This prevents normal
batches from growing without bound while retaining compatibility with a single
large `raw_json` value.

## Components

### Batch builder

`internal/dbmerge` owns a table-aware batch builder. It:

1. receives normalized `Row` values from any `RowSource`;
2. computes the row identity digest once;
3. tracks source-row count and estimated payload bytes;
4. flushes before a table change, the 10,000-row limit, or the 128 MiB limit;
5. retains duplicate occurrences for statistics while staging one canonical
   candidate per digest.

Identity construction remains authoritative in `IdentityFor`; the performance
path must not reimplement natural keys.

### Temporary staging

Each batch is staged in a temporary table on a dedicated target `*sql.Conn`.
The temporary table contains:

- source ordinal;
- identity digest;
- occurrence count; and
- an `is_new` marker populated by the target identity join; and
- the registry columns for that table.

Staging-table creation and population happen before `BEGIN IMMEDIATE`, so row
encoding and parameter binding do not hold the main-database writer lock.
Multi-row `VALUES` statements are split according to the connection's runtime
`SQLITE_LIMIT_VARIABLE_NUMBER`; these are SQL transport chunks only and do not
change the 10,000-row atomic target batch.

Temporary table and column identifiers come only from the compile-time schema
registry. Uploaded object names or SQL text are never executed.

### Batched identity lookup

One registry-driven predicate describes target identity equality for each table
kind. It uses SQLite `IS` semantics for every value and preserves the existing
special cases:

- non-empty Claude `request_id` wins over its fallback key;
- non-empty pending-TTFT `request_id` wins over its fallback key;
- Codex request identity excludes `cost_usd`; and
- ordinary and raw tables use their existing full canonical identity.

The predicate is used in set-oriented joins between the temporary candidates
and the target table. No request constructs SQL identifiers from uploaded data.

## Preview flow

Preview keeps strict header, `quick_check`, schema validation, ignored-table,
and warning behavior unchanged. It then processes each homogeneous batch:

1. bulk-add digests to the scratch seen store and identify first occurrences;
2. stage only first-occurrence candidates;
3. run one set-oriented target existence query for the staged candidates;
4. derive exact source, new, and duplicate counts per table; and
5. emit monotonic progress once per transport chunk or completed logical batch,
   rather than once per source row.

The scratch seen store persists for the preview duration, so duplicates that
cross batch boundaries remain duplicates. Its writes use prepared/multi-row
operations inside coarse transactions instead of one autocommit per digest.

## Import and verification flow

Import no longer performs a separate third source scan. For every logical
batch it performs the following:

1. stage candidates outside the target writer transaction;
2. execute `BEGIN IMMEDIATE` with the existing BUSY retry policy;
3. mark candidates that do not already exist in the target;
4. insert all marked details with `INSERT ... SELECT`;
5. read only marked request candidates back from staging, calculate their local
   aggregate date with the existing Go `time.Location` logic, and apply the
   resulting grouped aggregate deltas;
6. insert ledger rows with `INSERT OR IGNORE ... SELECT`;
7. commit the target transaction; and
8. run one set-containment query for the committed candidate set.

A batch is complete only after step 8 succeeds. `VerifiedIdentities` counts
unique source identities using a seen store that spans the import. Later source
duplicates therefore verify successfully without inflating that count.

The target remains authoritative. A target row found during the set lookup is
never updated from the source, including a differently priced row. Cost is
copied only for genuinely new request rows and is never recomputed.

## Concurrency and lock duration

The uploaded source is still read through its immutable read-only connection.
Only temporary staging occurs before `BEGIN IMMEDIATE`. The main writer lock is
held only for the set lookup, target inserts, aggregate deltas, ledger insert,
and commit.

The target pool retains its existing four-connection limit, so holding one
connection for staging does not prevent readers or receivers from acquiring a
different connection. SQLite still serializes writers. A concurrent OTLP write
that wins first causes the import batch to retry on BUSY; an import batch that
wins first releases the writer lock at its single commit.

The large logical batch must not be converted back into 10,000 row-oriented
target statements while the writer lock is held. If measured lock duration is
unacceptable, the implementation must optimize the set SQL rather than silently
changing the approved 10,000-row boundary.

## Failure handling and retries

- Failure before `BEGIN IMMEDIATE` leaves the target unchanged.
- Failure between begin and commit rolls back detail, aggregate, and ledger
  changes for the current batch only.
- BUSY and BUSY_SNAPSHOT retry the entire current logical batch with the
  existing bounded delays.
- Failure after commit but during containment verification returns a retryable
  error. Re-running is safe because the committed identities are then detected
  as target duplicates.
- Previously committed batches are retained.
- Cancellation closes rows, rolls back an open transaction, closes scratch
  databases, and removes their files.

## Progress and result compatibility

The API response shapes and UI phases do not change. Progress remains monotonic
and reports the same source, inserted, duplicate, and verified semantics.
Updates are coalesced to batch/chunk boundaries to avoid hundreds of thousands
of mutex acquisitions and status writes.

`Inspection`, `Result`, and per-table statistics remain compatible with the
existing frontend and CLI. JSONL imports use the same optimized `Import` path.

## Testing

### Correctness tests

- Flush at 10,000 rows, at 128 MiB, and on table change.
- Process one row larger than 128 MiB alone.
- Preserve duplicates within a batch, across batches, and already in target.
- Preserve Claude request-id and pending-request-id fallback semantics.
- Preserve NULL-versus-empty/zero identity behavior.
- Preserve target-wins cost behavior and exact aggregate deltas.
- Bulk ledger writes remain idempotent.
- An injected second-batch failure leaves exactly the first 10,000-row batch
  committed and retry completes without duplicate details or aggregates.
- A concurrent repository writer succeeds while a multi-batch import runs.
- Preview and integrated verification clean every scratch/temp resource on
  success, cancellation, and error.
- Progress is monotonic and final statistics match the current implementation.

### Performance tests

- Add deterministic benchmarks for preview, all-new import, and all-duplicate
  import using representative table mixes.
- Record logical batches, transport statements, target statements, and writer
  transaction duration so a regression cannot hide behind total wall time.
- Run the final implementation against the existing approximately 550,000-row
  analysis/source copy and a disposable target copy.

## Acceptance criteria

1. Logical batches contain at most 10,000 rows and approximately 128 MiB of row
   payload, with single-oversized-row compatibility.
2. Preview uses batch scratch writes and set-oriented target lookup.
3. Import uses set-oriented detail, aggregate, and ledger SQL inside one atomic
   transaction per logical batch.
4. Completion verification is integrated per committed batch; there is no
   third complete source scan.
5. Natural keys, target-wins behavior, NULL semantics, aggregation, retry, and
   progress results remain unchanged.
6. The uploaded database remains immutable and is never attached to the target
   connection.
7. Approximately 550,000 rows preview in at most 2 minutes and import plus
   verification in at most 5 minutes on the current development machine.
8. Full Go, race, frontend, vet, and development-instance checks pass.
9. The final executable is rebuilt into `bin/cc-otel.exe`, deployed only on
   ports 14317/18899, and the feature remains one commit relative to
   `origin/main`.
