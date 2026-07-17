# Set-Based Database Import Performance Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace millions of row-oriented SQLite statements with 10,000-row / 128 MiB set-oriented batches so approximately 550,000 rows preview within 2 minutes and import plus verification within 5 minutes.

**Architecture:** Stream every `RowSource` into homogeneous batches that compute each natural identity once. Bulk-stage each batch in a connection-local temporary table before taking the main writer lock, then use registry-generated set SQL for target lookup, detail insert, aggregate deltas, ledger, and containment verification. Keep a batched scratch seen store for cross-batch source duplicates, and integrate verification into import so the source is not scanned a third time.

**Tech Stack:** Go 1.25, `database/sql`, `github.com/ncruces/go-sqlite3` v0.33.3, SQLite WAL and TEMP tables, Go tests/benchmarks, Node test runner, PowerShell deployment.

## Global Constraints

- Logical batches contain at most 10,000 source rows and at most 128 MiB estimated payload; a table boundary flushes the batch.
- A single source row larger than 128 MiB is processed alone.
- A logical target batch remains one atomic detail + aggregate + ledger transaction even when TEMP-table transport uses multiple SQL chunks.
- Populate TEMP staging before `BEGIN IMMEDIATE`; do not perform 10,000 target statements while holding the writer lock.
- Never `ATTACH` an uploaded database to a writable target connection.
- Uploaded SQLite sources remain immutable/read-only and keep strict `quick_check` and schema validation.
- Existing `IdentityFor` natural keys and SQLite `IS` NULL semantics remain authoritative.
- Existing target rows always win; never recompute `cost_usd`.
- `codex_events` remains a compatibility table that is recognized but ignored.
- Preserve retry-safe partial progress, API response shapes, UI phases, and JSONL compatibility.
- Do not mutate, clean, vacuum, replace, or stop the production database/service under `C:\Users\Administrator\.claude\cc-otel\`.
- Build/deploy only `bin/cc-otel.exe` with `bin/cc-otel-dev.yaml` on OTLP 14317 and web 18899.
- Finish with one commit relative to `origin/main`; do not push.

## File Map

- Create `internal/dbmerge/batch.go`: payload estimation, homogeneous batch building, within-batch identity collapse.
- Create `internal/dbmerge/batch_test.go`: row/byte/table boundaries and oversized-row behavior.
- Modify `internal/dbmerge/inspect.go`: batched scratch seen operations and set-oriented preview.
- Create `internal/dbmerge/stage.go`: TEMP staging, runtime bind limit, identity predicates, set lookup/insert/ledger/verification.
- Create `internal/dbmerge/stage_test.go`: all identity variants, NULL semantics, transport chunking, and target-wins behavior.
- Modify `internal/dbmerge/import.go`: set-based atomic import and integrated per-batch verification.
- Modify `internal/dbmerge/verify.go`: remove MergeSQLite's third scan and batch the standalone verifier.
- Modify related dbmerge tests, API defaults, legacy CLI defaults, and old 500-row documentation.
- Create `internal/dbmerge/performance_test.go`: opt-in real-fixture performance gate and deterministic benchmarks.

---

### Task 1: Build Homogeneous 10,000-Row / 128 MiB Batches

**Files:**
- Create: `internal/dbmerge/batch.go`
- Create: `internal/dbmerge/batch_test.go`
- Modify: `internal/dbmerge/types.go`

**Interfaces:**
- Consumes: `RowSource.Scan`, `IdentityFor(Row) (Identity, error)`.
- Produces: `scanBatches(context.Context, RowSource, batchLimits, func(rowBatch) error) error`, `defaultBatchLimits(Options) batchLimits`, and `rowBatch.firstCandidates(map[string]struct{}) rowBatch`.

- [ ] **Step 1: Write failing boundary tests**

```go
func TestScanBatchesFlushesOnRowsAndTableChange(t *testing.T) {
	rows := sliceSource{
		requestRow("a", 1), requestRow("b", 1), requestRow("c", 1),
		{Table: "events", Values: map[string]any{"timestamp": int64(4), "event_name": "event"}},
	}
	var got []rowBatch
	err := scanBatches(context.Background(), rows, batchLimits{Rows: 2, Bytes: 1 << 20}, func(batch rowBatch) error {
		got = append(got, batch)
		return nil
	})
	if err != nil || len(got) != 3 || got[0].SourceRows != 2 || got[1].SourceRows != 1 || got[2].Table != "events" {
		t.Fatalf("batches=%+v err=%v", got, err)
	}
}

func TestScanBatchesFlushesBeforeByteLimitAndAllowsOversizedRow(t *testing.T) {
	row := Row{Table: "raw_otlp_events", Values: map[string]any{
		"timestamp": int64(1), "event_type": "log", "raw_json": strings.Repeat("x", 256),
	}}
	var got []rowBatch
	err := scanBatches(context.Background(), sliceSource{row, row}, batchLimits{Rows: 10, Bytes: 128}, func(batch rowBatch) error {
		got = append(got, batch)
		return nil
	})
	if err != nil || len(got) != 2 || got[0].SourceRows != 1 || got[1].SourceRows != 1 {
		t.Fatalf("batches=%+v err=%v", got, err)
	}
}

func TestScanBatchesCollapsesIdentityButCountsOccurrences(t *testing.T) {
	row := requestRow("same", 1)
	var got rowBatch
	err := scanBatches(context.Background(), sliceSource{row, row}, batchLimits{Rows: 10, Bytes: 1 << 20}, func(batch rowBatch) error {
		got = batch
		return nil
	})
	if err != nil || got.SourceRows != 2 || len(got.Candidates) != 1 || got.Candidates[0].Occurrences != 2 {
		t.Fatalf("batch=%+v err=%v", got, err)
	}
}
```

- [ ] **Step 2: Run Task 1 tests and verify RED**

```powershell
go test ./internal/dbmerge -run 'TestScanBatches' -count=1
```

Expected: compile failure because the batch APIs do not exist.

- [ ] **Step 3: Implement the limits and batch types**

In `types.go`:

```go
const (
	MaxBatchSize         = 10_000
	MaxBatchPayloadBytes = int64(128 << 20)
)

type Options struct {
	BatchSize    int
	SourceID     string
	Location     *time.Location
	TotalRows    int64
	Progress     ProgressFunc
	Window       *TimeWindow
	retryDelays  []time.Duration
	beforeCommit func(batch int) error
	batchBytes   int64
	metrics      func(batchMetrics)
}

type batchMetrics struct {
	LogicalRows int64
	TransportStatements int
	TargetStatements int
	WriterDuration time.Duration
}
```

Create `batch.go`:

```go
type batchLimits struct { Rows int; Bytes int64 }

type batchCandidate struct {
	Row Row
	Identity Identity
	Ordinal int64
	Occurrences int64
}

type rowBatch struct {
	Table string
	Candidates []batchCandidate
	SourceRows int64
	PayloadBytes int64
}

func defaultBatchLimits(options Options) batchLimits {
	rows := options.BatchSize
	if rows <= 0 || rows > MaxBatchSize { rows = MaxBatchSize }
	bytes := options.batchBytes
	if bytes <= 0 || bytes > MaxBatchPayloadBytes { bytes = MaxBatchPayloadBytes }
	return batchLimits{Rows: rows, Bytes: bytes}
}
```

`estimateRowBytes` must count 64 bytes per row, 32 bytes per map entry, key/table lengths, exact string/blob lengths, and 16 bytes per scalar. `scanBatches` flushes before a non-empty batch would exceed row/byte limits or change table; computes `IdentityFor` once; retains the first row per digest; increments `Occurrences` for a repeated digest; counts every source row; and accepts one oversized row alone. Check `ctx.Err()` before scanning and callbacks.

Add `digests()` and `firstCandidates(first)` helpers without recomputing identities.

- [ ] **Step 4: Run Task 1 tests and verify GREEN**

```powershell
gofmt -w internal/dbmerge/types.go internal/dbmerge/batch.go internal/dbmerge/batch_test.go
go test ./internal/dbmerge -run 'TestScanBatches' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit Task 1**

```powershell
git add internal/dbmerge/types.go internal/dbmerge/batch.go internal/dbmerge/batch_test.go
git commit -m "perf(dbmerge): build larger bounded batches"
```

---

### Task 2: Batch Scratch Digests and Add Registry-Only TEMP Staging

**Files:**
- Modify: `internal/dbmerge/inspect.go`
- Create: `internal/dbmerge/stage.go`
- Create: `internal/dbmerge/stage_test.go`

**Interfaces:**
- Consumes: `rowBatch`, `TableSpec`, ncruces driver `Conn.Raw().Limit`.
- Produces: `(*seenStore).AddBatch`, `newStagedBatch`, `markNew`, `countNew`, `insertNew`, `newRows`, `writeLedger`, `missingDigest`, and `Close`.

- [ ] **Step 1: Write failing scratch/staging tests**

```go
func TestSeenStoreAddBatchReturnsOnlyFirstDigests(t *testing.T) {
	store, err := newSeenStore(t.TempDir())
	if err != nil { t.Fatal(err) }
	defer store.Close()
	first, err := store.AddBatch(context.Background(), []string{"a", "b", "a"})
	if err != nil || !maps.Equal(first, map[string]struct{}{"a": {}, "b": {}}) {
		t.Fatalf("first=%v err=%v", first, err)
	}
	first, err = store.AddBatch(context.Background(), []string{"b", "c"})
	if err != nil || !maps.Equal(first, map[string]struct{}{"c": {}}) {
		t.Fatalf("first=%v err=%v", first, err)
	}
}

func TestStagedBatchMarksConflictsAndInsertsNewSet(t *testing.T) {
	target := importTarget(t)
	existing := requestRow("existing", 1)
	insertTestRow(t, target, existing)
	batch := mustBatch(t, sliceSource{existing, requestRow("new", 2)})
	stage, err := newStagedBatch(context.Background(), target, batch, stageOptions{})
	if err != nil { t.Fatal(err) }
	defer stage.Close(context.Background())
	if err = stage.markNew(context.Background()); err != nil { t.Fatal(err) }
	if got, err := stage.countNew(context.Background()); err != nil || got != 1 {
		t.Fatalf("new=%d err=%v", got, err)
	}
	if got, err := stage.insertNew(context.Background()); err != nil || got != 1 {
		t.Fatalf("inserted=%d err=%v", got, err)
	}
}

func TestIdentityPredicateUsesISForEveryKind(t *testing.T) {
	for _, table := range []string{"api_requests", "pending_ttft_spans", "codex_api_requests", "events"} {
		spec, _ := LookupSpec(table)
		predicate := identityPredicate(spec, "t", "s")
		if predicate == "" || !strings.Contains(predicate, " IS ") {
			t.Fatalf("%s predicate=%q", table, predicate)
		}
	}
}
```

Define `mustBatch` in `stage_test.go` by calling `scanBatches` with default limits and requiring exactly one callback. Add a low-variable-limit test using `stageOptions{variableLimit: 64}` that forces multiple transport statements while retaining one logical batch.

- [ ] **Step 2: Run Task 2 tests and verify RED**

```powershell
go test ./internal/dbmerge -run 'TestSeenStoreAddBatch|TestStagedBatch|TestIdentityPredicate' -count=1
```

Expected: compile failure for the new APIs.

- [ ] **Step 3: Implement `seenStore.AddBatch`**

Acquire one scratch `*sql.Conn`, read its runtime variable limit with the same ncruces raw-connection helper, and start one transaction on that connection. Split digests by the limit and execute multi-row:

```sql
INSERT OR IGNORE INTO seen(digest) VALUES (?),(?),(?) RETURNING digest
```

Scan returned digests into `map[string]struct{}` and commit once per logical batch. Roll back on cancellation or error. Keep `Add` as a one-digest wrapper until all callers migrate.

- [ ] **Step 4: Implement TEMP staging and identity predicates**

Use `batchMetrics` from Task 1 and create `stage.go` with:

```go
type stageOptions struct { variableLimit int }

type stagedBatch struct {
	conn *sql.Conn
	spec TableSpec
	batch rowBatch
	metrics batchMetrics
}
```

`newStagedBatch(ctx, target, batch, stageOptions)` reads `SQLITE_LIMIT_VARIABLE_NUMBER` with `conn.Raw`, casts to `github.com/ncruces/go-sqlite3/driver.Conn`, and calls `Raw().Limit(sqlite3.LIMIT_VARIABLE_NUMBER, -1)`. A positive test override lowers, but never raises, the runtime limit.

Create TEMP `_dbmerge_batch` with `_merge_digest TEXT PRIMARY KEY`, `_merge_ordinal`, `_merge_occurrences`, `_merge_new`, and registry columns. Populate it before `BEGIN IMMEDIATE` using variable-limit-sized multi-row `VALUES`; all identifiers come from `TableSpec`. Make `Close` idempotent with `sync.Once`.

Generate `identityPredicate` as follows:

- Claude request: non-empty `request_id`, otherwise timestamp/session/prompt/event-sequence/model/input/output/duration fallback.
- Pending TTFT: non-empty `request_id`, otherwise session/model/span-end/ttft fallback.
- Codex request: timestamp/session/model/input/output/duration.
- Other tables: every canonical spec column.

Every comparison is `target.column IS stage.column`. Mark candidates with one correlated set update. `insertNew` uses one `INSERT INTO main.<table> SELECT ... WHERE _merge_new=1`, verifies `RowsAffected == countNew`, and does not silently ignore mismatches. `newRows` scans only marked request candidates for existing Go aggregate helpers. `writeLedger` uses one `INSERT OR IGNORE ... SELECT _merge_digest,?,?,?`. `missingDigest` performs one inverse containment query after commit. `Close` always drops TEMP state and closes the dedicated connection.

- [ ] **Step 5: Run Task 2 tests and verify GREEN**

```powershell
gofmt -w internal/dbmerge/inspect.go internal/dbmerge/stage.go internal/dbmerge/stage_test.go
go test ./internal/dbmerge -run 'TestSeenStoreAddBatch|TestStagedBatch|TestIdentityPredicate' -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit Task 2**

```powershell
git add internal/dbmerge/inspect.go internal/dbmerge/stage.go internal/dbmerge/stage_test.go
git commit -m "perf(dbmerge): add set-based staging"
```

---

### Task 3: Convert Preview to Batched Set Lookup

**Files:**
- Modify: `internal/dbmerge/inspect.go`
- Modify: `internal/dbmerge/inspect_test.go`

**Interfaces:**
- Consumes: `scanBatches`, `seenStore.AddBatch`, staging `markNew` and `countNew`.
- Produces: unchanged `InspectSQLite` API with exact existing statistics and batch-level progress.

- [ ] **Step 1: Add a failing cross-batch preview test**

Create 10,001 source requests with the final identity equal to the first. Against an empty target assert `SourceRows=10001`, `NewRows=10000`, and `DuplicateRows=1`. Use the metrics hook to allow at most eight target statements for two logical batches. Retain non-mutation, scratch cleanup, warning, and monotonic progress tests.

- [ ] **Step 2: Run focused preview tests and verify RED**

```powershell
go test ./internal/dbmerge -run 'TestInspectSQLite' -count=1
```

Expected: the statement-bound assertion fails because preview still calls scratch insert and `RecordExists` per row. Add private `inspectSQLite(ctx context.Context, path string, target *sql.DB, progress ProgressFunc, metrics func(batchMetrics)) (Inspection, error)` and keep exported `InspectSQLite` as the nil-metrics wrapper so the public API does not change.

- [ ] **Step 3: Replace row-oriented preview with batch processing**

Keep validation and source counts. For every `scanBatches` callback:

1. call `seen.AddBatch(batch.digests())`;
2. filter `batch.firstCandidates(first)`;
3. stage only first candidates with `newStagedBatch(ctx, target, firstBatch, stageOptions{})`;
4. call `markNew` and `countNew`;
5. set `duplicates = batch.SourceRows - newRows`;
6. merge totals/per-table stats; and
7. emit one monotonic progress update.

Explicitly close each stage inside the callback; do not accumulate callback defers. Preview never begins a main write transaction.

- [ ] **Step 4: Run preview and race tests**

```powershell
go test ./internal/dbmerge -run 'TestInspectSQLite' -count=1
go test -race ./internal/dbmerge -run 'TestInspectSQLite' -count=1
```

Expected: PASS with no `.inspect-*` or TEMP leaks.

- [ ] **Step 5: Commit Task 3**

```powershell
git add internal/dbmerge/inspect.go internal/dbmerge/inspect_test.go
git commit -m "perf(dbmerge): batch import preview"
```

---

### Task 4: Set-Based Atomic Import With Integrated Verification

**Files:**
- Modify: `internal/dbmerge/import.go`
- Modify: `internal/dbmerge/import_test.go`
- Modify: `internal/dbmerge/verify.go`
- Modify: `internal/dbmerge/verify_test.go`

**Interfaces:**
- Consumes: staging APIs and existing `addAggregateDelta` / `applyAggregateDeltas`.
- Produces: optimized `Import` whose `Result.VerifiedIdentities` is complete, and `MergeSQLite` with no subsequent full `Verify` scan.

- [ ] **Step 1: Write failing 10,000-boundary tests**

```go
func TestImportCommitsAtTenThousandRows(t *testing.T) {
	target := importTarget(t)
	var commits int
	result, err := Import(context.Background(), target, sliceSource(makeRequests(10_001)), Options{
		BatchSize: MaxBatchSize, SourceID: "upload:test", Location: time.UTC,
		beforeCommit: func(int) error { commits++; return nil },
	})
	if err != nil || result.InsertedRows != 10_001 || result.VerifiedIdentities != 10_001 || commits != 2 {
		t.Fatalf("result=%+v commits=%d err=%v", result, commits, err)
	}
}
```

Update injected failure coverage to fail batch 2 of 10,001 rows and assert exactly 10,000 detail, aggregate, and ledger rows committed. Add within-batch and cross-batch duplicate tests asserting inserted, duplicate, and unique verified counts. Add a scan-counting `RowSource` and assert optimized import scans it exactly once.

- [ ] **Step 2: Run import tests and verify RED**

```powershell
go test ./internal/dbmerge -run 'TestImport' -count=1
```

Expected: old 500-row batching and absent integrated verification fail the new assertions.

- [ ] **Step 3: Implement set-oriented batch execution**

Replace the `[]Row` flush loop with `scanBatches` and create one import-duration seen store. Preserve committed statistics on a post-commit verification failure:

```go
type batchOutcome struct {
	stats batchStats
	verified int64
	committed bool
}
```

For every logical batch, call `newStagedBatch` once before the retry loop. Reuse that populated TEMP table and dedicated connection for every pre-commit BUSY retry. For each transaction attempt:

1. execute `BEGIN IMMEDIATE`;
2. `markNew` and `countNew`;
3. scan `newRows` only for request tables and feed existing aggregate helpers;
4. `insertNew` and require its result to equal `countNew`;
5. apply aggregate deltas;
6. bulk `writeLedger` from staged digests;
7. execute `beforeCommit`, then `COMMIT`;
8. set `committed=true` before any verification operation;
9. run one `missingDigest` containment query;
10. call import seen-store `AddBatch` and increment verified only by globally first digests.

Set scanned to `batch.SourceRows`, inserted to the inserted set count, and duplicates to `SourceRows-inserted`. Merge a committed outcome into `Result` even when later containment/scratch work fails. Retry BUSY/BUSY_SNAPSHOT only when `committed=false`. Populate `batchMetrics` and invoke the private metrics hook after closing stage state.

Remove `RecordExists`, per-row detail insert, and per-row ledger calls from the hot loop. Delete only private helpers proven unreferenced by `rg`.

- [ ] **Step 4: Remove MergeSQLite's third source scan**

```go
func MergeSQLite(ctx context.Context, target *sql.DB, path string, options Options) (Result, error) {
	schema, err := ValidateSQLite(ctx, path)
	if err != nil { return Result{}, err }
	return Import(ctx, target, NewSQLiteSource(path, schema, options.Window), options)
}
```

Keep public `Verify` for the independent verifier; Task 5 batches that path.

- [ ] **Step 5: Run import/merge tests and race tests**

```powershell
go test ./internal/dbmerge -run 'TestImport|TestMergeSQLite' -count=1
go test -race ./internal/dbmerge -run 'TestImport|TestMergeSQLite' -count=1
```

Expected: PASS; retry remains idempotent and the concurrent repository writer finishes in WAL mode.

- [ ] **Step 6: Commit Task 4**

```powershell
git add internal/dbmerge/import.go internal/dbmerge/import_test.go internal/dbmerge/verify.go internal/dbmerge/verify_test.go
git commit -m "perf(dbmerge): import and verify staged batches"
```

---

### Task 5: Batch Standalone Verification and Align API, CLI, and Docs

**Files:**
- Modify: `internal/dbmerge/verify.go`
- Modify: `internal/dbmerge/verify_test.go`
- Modify: `internal/api/import_jobs.go`
- Modify: `tools/merge_bin_global/import_global/main.go`
- Modify: `docs/superpowers/specs/2026-07-16-database-import-design.md`
- Modify: `docs/superpowers/plans/2026-07-16-ignore-codex-events.md`

**Interfaces:**
- Consumes: batch scanner, batched seen store, staging containment, integrated `Result.VerifiedIdentities`.
- Produces: batched public `Verify`, web/CLI default 10,000, and no stale 500-row requirements.

- [ ] **Step 1: Write a failing standalone verification batch test**

Use 10,001 rows with the final row duplicating the first and assert `Verify` returns 10,000. Use metrics to assert target queries grow by logical batches instead of unique rows. Add private `verifyBatches(ctx context.Context, target *sql.DB, source RowSource, progress ProgressFunc, metrics func(batchMetrics)) (int64, error)` and keep exported `Verify` as its nil-metrics wrapper. Retain repriced request, changed pending state, and missing-event errors.

- [ ] **Step 2: Run Verify tests and verify RED**

```powershell
go test ./internal/dbmerge -run 'TestVerify' -count=1
```

Expected: target-statement assertion fails because `Verify` still calls `RecordExists` per identity.

- [ ] **Step 3: Implement batched public Verify**

Use `scanBatches` and a Verify-duration seen store. Per batch, call `AddBatch`, filter globally first candidates, stage, and call `missingDigest`. Preserve `MergeError{Code: ErrVerification, Table: ..., Row: ...}` on absence. Increment `verified` by globally first candidates and emit one verifying progress update per batch.

- [ ] **Step 4: Remove redundant CLI verification and old defaults**

In `internal/api/import_jobs.go`:

```go
BatchSize: dbmerge.MaxBatchSize,
```

In `import_global/main.go`, change help/default from 500 to 10000, remove the immediate second `dbmerge.Verify`, and print `result.VerifiedIdentities`. Keep `verify_merge`'s call because independent verification is its purpose and now uses batched `Verify`.

Replace every old 500-row requirement in the original design and Codex-retention plan with: at most 10,000 homogeneous rows or approximately 128 MiB payload, preserving atomic detail/aggregate/ledger commit.

- [ ] **Step 5: Run package, CLI, docs, and race checks**

```powershell
go test ./internal/dbmerge ./internal/api ./tools/merge_bin_global/import_global ./tools/merge_bin_global/verify_merge -count=1
go test -race ./internal/dbmerge ./internal/api -count=1
rg -n 'MaxBatchSize = 500|BatchSize: 500|最多 500|500 行批次' internal docs tools
```

Expected: Go commands PASS; search has no stale batch-limit claim. Review unrelated numeric test values rather than changing them mechanically.

- [ ] **Step 6: Commit Task 5**

```powershell
git add internal/dbmerge/verify.go internal/dbmerge/verify_test.go internal/api/import_jobs.go tools/merge_bin_global/import_global/main.go docs
git commit -m "perf(dbmerge): batch verification paths"
```

---

### Task 6: Add Performance Gates, Verify, Squash, and Deploy 18899

**Files:**
- Create: `internal/dbmerge/performance_test.go`
- Build output: `bin/cc-otel.exe`

**Interfaces:**
- Consumes: `CC_OTEL_IMPORT_FIXTURE`, optimized preview/import.
- Produces: real 550k timing evidence, one final feature commit, development binary on 18899/14317.

- [ ] **Step 1: Add deterministic benchmarks and opt-in real fixture test**

Add 20,000-row preview, all-new, and all-duplicate benchmarks. Add this opt-in gate, always using a `t.TempDir` target:

```go
func TestLargeFixturePerformance(t *testing.T) {
	path := os.Getenv("CC_OTEL_IMPORT_FIXTURE")
	if path == "" { t.Skip("CC_OTEL_IMPORT_FIXTURE is not set") }
	target := importTarget(t)
	previewStart := time.Now()
	preview, err := InspectSQLite(context.Background(), path, target, nil)
	if err != nil { t.Fatal(err) }
	previewElapsed := time.Since(previewStart)
	if previewElapsed > 2*time.Minute {
		t.Fatalf("preview %d rows took %s", preview.SourceRows, previewElapsed)
	}
	importStart := time.Now()
	result, err := MergeSQLite(context.Background(), target, path, Options{
		BatchSize: MaxBatchSize, SourceID: "performance", Location: time.Local, TotalRows: preview.SourceRows,
	})
	if err != nil { t.Fatal(err) }
	importElapsed := time.Since(importStart)
	if importElapsed > 5*time.Minute {
		t.Fatalf("import %d rows took %s", result.ScannedRows, importElapsed)
	}
	retryStart := time.Now()
	retry, err := MergeSQLite(context.Background(), target, path, Options{
		BatchSize: MaxBatchSize, SourceID: "performance-retry", Location: time.Local, TotalRows: preview.SourceRows,
	})
	if err != nil || retry.InsertedRows != 0 {
		t.Fatalf("duplicate retry=%+v err=%v", retry, err)
	}
	t.Logf("rows=%d preview=%s import=%s duplicate=%s", preview.SourceRows, previewElapsed, importElapsed, time.Since(retryStart))
}
```

- [ ] **Step 2: Run benchmarks and real 550k gate**

```powershell
go test ./internal/dbmerge -run '^$' -bench 'Benchmark(Inspect|Import)' -benchtime=1x -count=1
$env:CC_OTEL_IMPORT_FIXTURE = 'D:\goProject\cc-otel\.artifacts\global-db-analysis-20260716.db'
go test ./internal/dbmerge -run TestLargeFixturePerformance -count=1 -v -timeout 15m
Remove-Item Env:CC_OTEL_IMPORT_FIXTURE
```

Expected: preview <=2 minutes, import with integrated verification <=5 minutes, duplicate retry inserts zero. Record exact timings.

- [ ] **Step 3: Run complete verification**

```powershell
go test ./... -count=1
go test -race ./... -count=1
node --test internal/web/static/tests/*.test.mjs
go vet ./...
git diff --check
```

Expected: every command exits 0.

- [ ] **Step 4: Squash without changing the tree**

```powershell
$before = git rev-parse 'HEAD^{tree}'
git reset --soft origin/main
git commit -m "feat: add efficient online database import"
$after = git rev-parse 'HEAD^{tree}'
if ($before -ne $after) { throw 'tree changed during squash' }
if ((git rev-list --count origin/main..HEAD) -ne 1) { throw 'feature is not one commit' }
```

- [ ] **Step 5: Rebuild and restart only development**

Capture PID/executable ownership for all four ports first and require production to remain the executable under `C:\Users\Administrator\.claude\cc-otel\`.

```powershell
.\bin\cc-otel.exe stop -config .\bin\cc-otel-dev.yaml
$version = git rev-parse --short HEAD
go build -ldflags "-s -w -X main.version=$version" -o .\bin\cc-otel.exe .\cmd\cc-otel\
.\bin\cc-otel.exe start -config .\bin\cc-otel-dev.yaml
.\bin\cc-otel.exe version
```

- [ ] **Step 6: Verify endpoints and isolation**

```powershell
Invoke-RestMethod http://localhost:18899/api/health
Invoke-RestMethod http://localhost:18899/api/status
Invoke-RestMethod http://localhost:18899/api/import/status
Get-NetTCPConnection -State Listen |
  Where-Object LocalPort -In 14317,18899,4317,8899 |
  Sort-Object LocalPort |
  Select-Object LocalPort,OwningProcess
git status --short --branch
```

Expected: dev owns 14317/18899, production keeps 4317/8899, worktree is clean, and no push occurs.
