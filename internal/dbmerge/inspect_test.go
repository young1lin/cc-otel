package dbmerge

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestInspectSQLiteCountsMainAndSourceDuplicates(t *testing.T) {
	sourcePath, target := duplicateInspectionFixture(t)
	defer target.Close()

	got, err := InspectSQLite(context.Background(), sourcePath, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.SourceRows != 5 || got.NewRows != 2 || got.DuplicateRows != 3 {
		t.Fatalf("preview = %+v, want source=5 new=2 duplicate=3", got)
	}
}

func TestInspectSQLiteReportsIgnoredTablesAndLegacyWarnings(t *testing.T) {
	sourcePath := currentFixture(t)
	source := openWritable(t, sourcePath)
	mustExec(t, source, `DROP INDEX idx_pending_ttft_request`)
	mustExec(t, source, `ALTER TABLE pending_ttft_spans DROP COLUMN request_id`)
	mustExec(t, source, `INSERT INTO codex_events(timestamp, event_name, event_kind) VALUES(1, 'codex.sse_event', 'response.output_text.delta')`)
	source.Close()
	target := openWritable(t, currentFixture(t))
	defer target.Close()

	got, err := InspectSQLite(context.Background(), sourcePath, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !containsText(got.IgnoredTables, "daily_model_agg") || !containsText(got.IgnoredTables, "model_pricing") || !containsText(got.IgnoredTables, "codex_events") {
		t.Fatalf("ignored tables = %v", got.IgnoredTables)
	}
	if got.SourceRows != 0 {
		t.Fatalf("source rows = %d, want 0 after ignoring codex_events", got.SourceRows)
	}
	if !containsText(got.Warnings, "Legacy pending_ttft_spans has no request_id; empty values will be used.") {
		t.Fatalf("warnings = %v", got.Warnings)
	}
}

func TestInspectSQLiteDoesNotMutateEitherDatabase(t *testing.T) {
	sourcePath, target := duplicateInspectionFixture(t)
	defer target.Close()
	beforeSource := tableCount(t, openAndClose(t, sourcePath), "api_requests")
	beforeTarget := tableCount(t, target, "api_requests")

	if _, err := InspectSQLite(context.Background(), sourcePath, target, nil); err != nil {
		t.Fatal(err)
	}
	source := openWritable(t, sourcePath)
	defer source.Close()
	if got := tableCount(t, source, "api_requests"); got != beforeSource {
		t.Fatalf("source requests = %d, want %d", got, beforeSource)
	}
	if got := tableCount(t, target, "api_requests"); got != beforeTarget {
		t.Fatalf("target requests = %d, want %d", got, beforeTarget)
	}
}

func TestInspectSQLiteRemovesSeenScratchFilesOnSuccessAndError(t *testing.T) {
	sourcePath, target := duplicateInspectionFixture(t)
	if _, err := InspectSQLite(context.Background(), sourcePath, target, nil); err != nil {
		t.Fatal(err)
	}
	assertNoInspectScratch(t, filepath.Dir(sourcePath))
	target.Close()
	if _, err := InspectSQLite(context.Background(), sourcePath, target, nil); err == nil {
		t.Fatal("expected inspection failure with closed target")
	}
	assertNoInspectScratch(t, filepath.Dir(sourcePath))
}

func TestInspectSQLiteProgressIsMonotonic(t *testing.T) {
	sourcePath, target := duplicateInspectionFixture(t)
	defer target.Close()
	var progress []Progress
	if _, err := InspectSQLite(context.Background(), sourcePath, target, func(value Progress) {
		progress = append(progress, value)
	}); err != nil {
		t.Fatal(err)
	}
	if len(progress) == 0 {
		t.Fatal("no progress emitted")
	}
	for i, value := range progress {
		if value.Percent < 0 || value.Percent > 100 {
			t.Fatalf("progress %d percent = %v", i, value.Percent)
		}
		if i > 0 && value.ProcessedRows < progress[i-1].ProcessedRows {
			t.Fatalf("progress regressed: %+v then %+v", progress[i-1], value)
		}
	}
	if progress[len(progress)-1].Percent != 100 {
		t.Fatalf("final progress = %+v", progress[len(progress)-1])
	}
}

func TestInspectSQLiteBatchesCrossBatchDuplicates(t *testing.T) {
	sourcePath := currentFixture(t)
	source := openWritable(t, sourcePath)
	tx, err := source.Begin()
	if err != nil {
		t.Fatal(err)
	}
	stmt, err := tx.Prepare(`INSERT INTO events(timestamp, event_name) VALUES(?, ?)`)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < MaxBatchSize; i++ {
		if _, err := stmt.Exec(int64(i+1), "batched-event"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := stmt.Exec(int64(1), "batched-event"); err != nil {
		t.Fatal(err)
	}
	if err := stmt.Close(); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}

	target := openWritable(t, currentFixture(t))
	defer target.Close()
	var targetStatements int
	got, err := inspectSQLite(context.Background(), sourcePath, target, nil, func(metrics batchMetrics) {
		targetStatements += metrics.TargetStatements
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.SourceRows != 10_001 || got.NewRows != 10_000 || got.DuplicateRows != 1 {
		t.Fatalf("preview=%+v", got)
	}
	if targetStatements > 8 {
		t.Fatalf("target statements=%d, want at most 8 for two logical batches", targetStatements)
	}
}

func duplicateInspectionFixture(t *testing.T) (string, *sql.DB) {
	t.Helper()
	target := openWritable(t, currentFixture(t))
	mainByID := requestRow("same-id", 100)
	mainFallback := requestRow("", 100)
	mainFallback.Values["timestamp"] = int64(200)
	insertTestRow(t, target, mainByID)
	insertTestRow(t, target, mainFallback)

	sourcePath := currentFixture(t)
	source := openWritable(t, sourcePath)
	repriced := requestRow("same-id", 999)
	fallback := requestRow("", 999)
	fallback.Values["timestamp"] = int64(200)
	newRequest := requestRow("new-id", 50)
	newRequest.Values["timestamp"] = int64(300)
	insertTestRow(t, source, repriced)
	insertTestRow(t, source, fallback)
	insertTestRow(t, source, newRequest)
	event := Row{Table: "events", Values: map[string]any{"timestamp": int64(400), "event_name": "duplicate-event"}}
	insertTestRow(t, source, event)
	insertTestRow(t, source, event)
	source.Close()
	return sourcePath, target
}

func tableCount(t *testing.T, d *sql.DB, table string) int64 {
	t.Helper()
	var count int64
	if err := d.QueryRow(`SELECT COUNT(*) FROM "` + table + `"`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func openAndClose(t *testing.T, path string) *sql.DB {
	t.Helper()
	d := openWritable(t, path)
	t.Cleanup(func() { d.Close() })
	return d
}

func assertNoInspectScratch(t *testing.T, directory string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(directory, ".inspect-*"))
	if err != nil {
		t.Fatal(err)
	}
	for _, match := range matches {
		if _, err := os.Stat(match); err == nil {
			t.Errorf("scratch file was not removed: %s", match)
		}
	}
}
