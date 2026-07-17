# Ignore Codex Events Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop retaining generic `codex_events` rows and exclude that table from SQLite/JSONL merge input while preserving all Codex request, token, cost, duration, TTFT, prompt, and tool data.

**Architecture:** Keep the physical `codex_events` table as a compatibility schema object, but remove every live receiver write to it. Move the table from the import registry to the strict ignored-table registry so SQLite uploads validate it but never scan it; make legacy JSONL and CLI export paths follow the same rule.

**Tech Stack:** Go 1.24, `database/sql`, ncruces SQLite driver, OTLP protobufs, Go tests, PowerShell deployment scripts.

## Global Constraints

- `codex.sse_event` with `event.kind=response.completed` must continue updating `codex_api_requests` and `codex_daily_model_agg`.
- WebSocket request/event processing must continue updating duration and TTFT through the in-memory tracker.
- Keep `codex_events` and its indexes in the database schema for compatibility.
- Do not delete, vacuum, replace, stop, or otherwise mutate the production database under `C:\Users\Administrator\.claude\cc-otel\`.
- Existing `codex_events` rows remain until a separately approved maintenance operation.
- Database import uses homogeneous logical batches of at most 10,000 source rows or approximately 128 MiB, with atomic detail, aggregate, and ledger transactions for the remaining 14 detail tables.
- The final executable must be `bin/cc-otel.exe`, tested only on web port `18899`, OTLP port `14317`, config `bin/cc-otel-dev.yaml`, and database `bin/cc-otel-dev.db`.
- Preserve a single feature commit relative to `origin/main` when implementation is complete.

## File Map

- `internal/receiver/codex_parser.go`: process accounting events without writing generic Codex events.
- `internal/receiver/codex_parser_test.go`: prove diagnostic events are discarded and structured accounting still works.
- `cmd/cc-otel/main.go`: remove automatic cleanup of existing compatibility rows.
- `cmd/cc-otel/main_test.go`: prevent the daemon from scheduling Codex event deletion.
- `internal/db/codex_repository.go`: remove the no-longer-supported generic event insertion API.
- `internal/db/codex_repository_test.go`: retain coverage only for structured Codex tables.
- `internal/db/repository.go`: remove the obsolete `codex_events` cleanup API.
- `internal/dbmerge/schema.go`: classify `codex_events` as known but ignored.
- `internal/dbmerge/schema_test.go`: pin the remaining 14 import tables.
- `internal/dbmerge/sqlite_source_test.go`: prove strict schema validation still recognizes `codex_events`.
- `internal/dbmerge/inspect_test.go`: prove preview reports but does not count `codex_events`.
- `internal/dbmerge/jsonl_source.go`: skip recognized ignored legacy JSONL records.
- `internal/dbmerge/jsonl_source_test.go`: prove old `codex_events` JSONL lines are skipped.
- `tools/merge_bin_global/export_bin/main.go`: stop exporting `codex_events`.
- `tools/merge_bin_global/export_bin/main_test.go`: pin exporter table selection.
- `tools/merge_bin_global/verify_merge/main.go`: stop requiring ignored Codex events in CLI verification.
- `docs/otel-events.md`, `docs/MERGE_AND_RECOMPUTE.md`, and the database-import design: document retention and the 14-table scope.

---

### Task 1: Stop Persisting Generic Codex Events

**Files:**
- Modify: `internal/receiver/codex_parser_test.go`
- Modify: `internal/receiver/codex_parser.go`
- Modify: `internal/db/codex_repository_test.go`
- Modify: `internal/db/codex_repository.go`
- Modify: `cmd/cc-otel/main_test.go`
- Modify: `cmd/cc-otel/main.go`
- Modify: `internal/db/repository.go`

**Interfaces:**
- Consumes: `dispatchCodexLog(context.Context, *db.Repository, *logspb.LogRecord, *resourcepb.Resource, Notifier, *codexSpanTracker, Pricer) bool`.
- Produces: unchanged accounting and tracker behavior with zero calls to a generic `InsertCodexEvent` repository API.

- [ ] **Step 1: Write the failing receiver test**

Add a dedicated test that sends representative non-accounting events and asserts that the compatibility table remains empty:

```go
func TestDispatchCodexLog_DiagnosticEventsAreNotPersisted(t *testing.T) {
	repo := newCodexReceiverRepo(t)
	ctx := context.Background()
	res := &resourcepb.Resource{Attributes: []*commontpb.KeyValue{attr("service.name", "codex-cli")}}
	tracker := newCodexSpanTracker()

	logs := []*logspb.LogRecord{
		{Attributes: []*commontpb.KeyValue{
			attr("event.name", "codex.sse_event"),
			attr("event.kind", "response.output_text.delta"),
			attr("conversation.id", "conversation-a"),
		}},
		{SpanId: []byte{1, 2, 3, 4, 5, 6, 7, 8}, Attributes: []*commontpb.KeyValue{
			attr("event.name", "codex.websocket_request"),
			attr("conversation.id", "conversation-a"),
			attr("model", "gpt-5.5"),
		}},
		{Attributes: []*commontpb.KeyValue{
			attr("event.name", "codex.startup_phase"),
			attr("conversation.id", "conversation-a"),
		}},
	}
	for _, record := range logs {
		dispatchCodexLog(ctx, repo, record, res, nil, tracker, nil)
	}
	if got := readCount(t, repo, `SELECT COUNT(*) FROM codex_events`); got != 0 {
		t.Fatalf("codex_events rows = %d, want 0", got)
	}
}
```

Remove the `sse_event_non_completion` case from the structured-event increment table, and add `codex_events = 0` assertions to the existing WebSocket duration tests.

- [ ] **Step 2: Run the focused test and verify RED**

Run:

```powershell
go test ./internal/receiver -run 'TestDispatchCodexLog_(DiagnosticEventsAreNotPersisted|WebsocketDurationBeforeSSEUsesObservedTime|SSECompletedUpdatesTokens)' -count=1
```

Expected: `TestDispatchCodexLog_DiagnosticEventsAreNotPersisted` fails because current code inserts three generic rows; completion/accounting tests remain green.

- [ ] **Step 3: Implement the minimal receiver change**

In `dispatchCodexLog`:

```go
case "codex.sse_event":
	if eventKindFromLog(lr, attrs) == "response.completed" {
		// Keep the existing token/cost update block unchanged.
	}
	return false
```

Keep `tracker.recordRequest(...)` in `codex.websocket_request`, then return without constructing or inserting `db.CodexEvent`. Replace the generic `default` persistence block with `return false`.

Remove `Repository.InsertCodexEvent`, remove its direct repository test call, and remove `codex_events` from the table list expected to gain a row. Do not remove the `CodexEvent` type because the structured prompt/tool insert methods still use it.

Remove the daemon's periodic `CleanupCodexWebsocketEvents` task and the now-unused repository method. Existing compatibility rows must remain until a separately approved maintenance operation.

- [ ] **Step 4: Run receiver and repository tests and verify GREEN**

Run:

```powershell
go test ./internal/receiver ./internal/db -count=1
go test -race ./internal/receiver ./internal/db -count=1
```

Expected: both commands exit 0; completion events still populate tokens/cost, and WebSocket tests still populate duration/TTFT without generic rows.

- [ ] **Step 5: Commit the receiver change**

```powershell
git add internal/receiver/codex_parser.go internal/receiver/codex_parser_test.go internal/db/codex_repository.go internal/db/codex_repository_test.go
git commit -m "fix(codex): stop retaining diagnostic events"
```

---

### Task 2: Ignore `codex_events` in SQLite and JSONL Imports

**Files:**
- Modify: `internal/dbmerge/schema_test.go`
- Modify: `internal/dbmerge/schema.go`
- Modify: `internal/dbmerge/sqlite_source_test.go`
- Modify: `internal/dbmerge/inspect_test.go`
- Modify: `internal/dbmerge/jsonl_source_test.go`
- Modify: `internal/dbmerge/jsonl_source.go`

**Interfaces:**
- Consumes: `ImportSpecs() []TableSpec`, `ignoredColumns(string) ([]string, bool)`, `ValidateSQLite`, `InspectSQLite`, and `JSONLSource.Scan`.
- Produces: exactly 14 imported detail specs; `codex_events` remains strictly validated and appears in `SchemaInfo.IgnoredTables`.

- [ ] **Step 1: Write failing registry and inspection tests**

Rename the registry test to `TestImportSpecsContainExactlyTheFourteenImportTables` and remove `codex_events` from its expected slice. Extend ignored-table coverage:

```go
if !containsText(got.IgnoredTables, "codex_events") {
	t.Fatalf("ignored tables = %v, want codex_events", got.IgnoredTables)
}
```

Seed one `codex_events` row in the inspection fixture and assert it does not increase `Inspection.SourceRows` or appear in `Inspection.Tables`. Keep `TestValidateSQLiteRejectsPartialCodexGroup` unchanged so the compatibility table remains required in a complete Codex schema.

- [ ] **Step 2: Write the failing legacy JSONL test**

Add a `codex_events` line before a valid request line and assert only the request reaches the callback:

```go
func TestJSONLSourceSkipsRecognizedIgnoredCodexEvents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "merge.jsonl")
	request := completeValues("api_requests")
	request["request_id"] = "request-a"
	ignored := map[string]any{"timestamp": float64(100), "event_name": "codex.sse_event"}
	data := encodeLegacyLine(t, "ignored", "codex_events", ignored) +
		encodeLegacyLine(t, "request", "api_requests", request)
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	var tables []string
	err := NewJSONLSource(path).Scan(context.Background(), func(row Row) error {
		tables = append(tables, row.Table)
		return nil
	})
	if err != nil || !slices.Equal(tables, []string{"api_requests"}) {
		t.Fatalf("tables=%v err=%v", tables, err)
	}
}
```

- [ ] **Step 3: Run focused tests and verify RED**

Run:

```powershell
go test ./internal/dbmerge -run 'TestImportSpecs|TestInspectSQLiteReportsIgnored|TestJSONLSourceSkips' -count=1
```

Expected: failures show that `codex_events` is still importable and legacy JSONL still yields or rejects it.

- [ ] **Step 4: Implement the strict ignored-table classification**

Remove this entry from `registry`:

```go
{Name: "codex_events", Kind: KindAllColumns, Group: "codex", Columns: cols("timestamp session_id conversation_id event_name event_kind model duration_ms error_message raw_attrs_json")},
```

Add the same column definition to `ignoredRegistry`:

```go
"codex_events": cols("timestamp session_id conversation_id event_name event_kind model duration_ms error_message raw_attrs_json"),
```

After decoding a legacy JSONL record and before calling `LookupSpec`, skip recognized ignored records:

```go
if _, ignored := ignoredColumns(record.Table); ignored {
	continue
}
```

Unknown tables must still fail. SQLite validation continues using `ignoredColumns`, and the existing Codex group presence check continues requiring `codex_events` for full-schema compatibility.

- [ ] **Step 5: Run dbmerge tests and verify GREEN**

Run:

```powershell
go test ./internal/dbmerge -count=1
go test -race ./internal/dbmerge -count=1
```

Expected: both commands exit 0; current and legacy schemas validate, preview lists `codex_events` as ignored, and all identity/import/verification tests remain green.

- [ ] **Step 6: Commit the merge classification**

```powershell
git add internal/dbmerge
git commit -m "fix(dbmerge): ignore codex diagnostic events"
```

---

### Task 3: Align the Legacy CLI and Documentation

**Files:**
- Modify: `tools/merge_bin_global/export_bin/main_test.go`
- Modify: `tools/merge_bin_global/export_bin/main.go`
- Modify: `tools/merge_bin_global/verify_merge/main.go`
- Modify: `docs/otel-events.md`
- Modify: `docs/MERGE_AND_RECOMPUTE.md`
- Modify: `docs/superpowers/specs/2026-07-16-database-import-design.md`

**Interfaces:**
- Consumes: exporter `tableConfigs()` and verifier table configuration.
- Produces: CLI artifacts containing only the same 14 detail tables as web import.

- [ ] **Step 1: Write the failing exporter selection test**

```go
func TestTableConfigsExcludeCodexEvents(t *testing.T) {
	for _, cfg := range tableConfigs() {
		if cfg.Name == "codex_events" {
			t.Fatal("codex_events must not be exported")
		}
	}
}
```

- [ ] **Step 2: Run the exporter test and verify RED**

Run:

```powershell
go test ./tools/merge_bin_global/export_bin -run TestTableConfigsExcludeCodexEvents -count=1
```

Expected: FAIL because `tableConfigs()` still includes `codex_events`.

- [ ] **Step 3: Remove the CLI table paths**

Delete the `codex_events` `tableCfg` block from `export_bin/main.go`. Remove the `codex_events` verification entry from `verify_merge/main.go`. Keep the schema creation/configuration in `import_global/main.go`; the compatibility table must still exist even though no incoming record is imported.

- [ ] **Step 4: Update documentation with exact retention behavior**

In `docs/otel-events.md`, replace generic storage claims with:

```markdown
| non-completion SSE events | not persisted | Parsed only when relevant; streaming deltas are discarded |
| `response.completed` | `codex_api_requests` | Updates tokens, cache, cost, and duration |
| WebSocket request/event | in-memory tracker | Updates TTFT/duration without generic persistence |
| other Codex logs | not persisted | Diagnostic fallback events are discarded |
```

Change `docs/MERGE_AND_RECOMPUTE.md` from “15 imported tables” to “14 imported tables; `codex_events` is recognized but ignored.” Update the original database-import design’s imported and ignored table lists to match the approved retention design; do not alter the historical evidence in the new retention design.

- [ ] **Step 5: Run CLI and documentation checks**

Run:

```powershell
go test ./tools/merge_bin_global/export_bin ./tools/merge_bin_global/import_global ./tools/merge_bin_global/run_merge ./tools/merge_bin_global/verify_merge -count=1
rg -n "all 15 imported tables|Most SSE events are stored|codex_events.*Imported" docs
```

Expected: Go tests exit 0; the search returns no stale behavior claims.

- [ ] **Step 6: Commit CLI and documentation changes**

```powershell
git add tools/merge_bin_global/export_bin tools/merge_bin_global/verify_merge docs
git commit -m "docs(codex): document diagnostic event retention"
```

---

### Task 4: Verify, Squash, Build, and Deploy the Development Instance

**Files:**
- Build output: `bin/cc-otel.exe`
- Runtime config: `bin/cc-otel-dev.yaml`

**Interfaces:**
- Consumes: completed receiver, dbmerge, CLI, and documentation changes.
- Produces: one verified feature commit and a development executable matching that commit.

- [ ] **Step 1: Run the full verification gate**

Run each command separately and require exit code 0:

```powershell
go test ./... -count=1
go test -race ./... -count=1
node --test internal/web/static/tests/*.test.mjs
go vet ./...
git diff --check
```

Expected: all Go packages pass, race reports no races, all 49 frontend tests pass, vet is clean, and diff check emits nothing.

- [ ] **Step 2: Verify the production process before deployment**

```powershell
Get-NetTCPConnection -State Listen |
  Where-Object LocalPort -In 14317,18899,4317,8899 |
  Sort-Object LocalPort |
  Select-Object LocalPort,OwningProcess
```

Expected: production remains on `4317/8899`; only the project development process owns `14317/18899`.

- [ ] **Step 3: Squash the complete feature relative to the remote base**

Preserve and compare the tree hash:

```powershell
$before = git rev-parse 'HEAD^{tree}'
git reset --soft origin/main
git commit -m "feat: add efficient online database import"
$after = git rev-parse 'HEAD^{tree}'
if ($before -ne $after) { throw "tree changed during squash" }
if ((git rev-list --count origin/main..HEAD) -ne 1) { throw "feature is not one commit" }
```

Expected: exactly one commit relative to `origin/main`, with an unchanged tree.

- [ ] **Step 4: Rebuild and restart only the development instance**

```powershell
.\bin\cc-otel.exe stop -config .\bin\cc-otel-dev.yaml
$version = git rev-parse --short HEAD
go build -ldflags "-s -w -X main.version=$version" -o .\bin\cc-otel.exe .\cmd\cc-otel\
.\bin\cc-otel.exe start -config .\bin\cc-otel-dev.yaml
.\bin\cc-otel.exe version
```

Expected: the binary reports the new commit hash and the service reports `Web UI: http://localhost:18899`.

- [ ] **Step 5: Verify live development endpoints and port isolation**

```powershell
Invoke-RestMethod http://localhost:18899/api/health
Invoke-RestMethod http://localhost:18899/api/status
Invoke-RestMethod http://localhost:18899/api/import/status
Get-NetTCPConnection -State Listen |
  Where-Object LocalPort -In 14317,18899,4317,8899 |
  Sort-Object LocalPort |
  Select-Object LocalPort,OwningProcess
```

Expected: health is `ok`, import status is available, development owns `14317/18899`, and the original production PID still owns `4317/8899`.

- [ ] **Step 6: Report the measured scope reduction without cleaning production**

Report the already measured consistent-snapshot evidence:

- `codex_events`: 2,043,251 ignored rows.
- Import candidate rows: 2,586,395 → 543,144.
- Compacted snapshot: 664.85 MiB → 194.95 MiB.
- Codex request/token/cost/duration/TTFT/aggregate checksums unchanged.

Do not execute deletion or vacuum commands against production.
