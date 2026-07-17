package dbmerge

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

const benchmarkRows = 20_000

func BenchmarkInspect20K(b *testing.B) {
	path := benchmarkSQLiteSource(b, benchmarkRows)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		target := importTarget(b)
		b.StartTimer()
		inspection, err := InspectSQLite(context.Background(), path, target, nil)
		if err != nil {
			b.Fatal(err)
		}
		if inspection.SourceRows != benchmarkRows || inspection.NewRows != benchmarkRows {
			b.Fatalf("inspection=%+v", inspection)
		}
	}
}

func BenchmarkImportAllNew20K(b *testing.B) {
	path := benchmarkSQLiteSource(b, benchmarkRows)
	schema, err := ValidateSQLite(context.Background(), path)
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		target := importTarget(b)
		b.StartTimer()
		result, err := Import(context.Background(), target, NewSQLiteSource(path, schema, nil), Options{
			BatchSize: MaxBatchSize, SourceID: fmt.Sprintf("benchmark-new-%d", i), Location: time.UTC,
		})
		if err != nil {
			b.Fatal(err)
		}
		if result.InsertedRows != benchmarkRows || result.VerifiedIdentities != benchmarkRows {
			b.Fatalf("result=%+v", result)
		}
	}
}

func BenchmarkImportAllDuplicate20K(b *testing.B) {
	path := benchmarkSQLiteSource(b, benchmarkRows)
	target := importTarget(b)
	if _, err := MergeSQLite(context.Background(), target, path, Options{
		BatchSize: MaxBatchSize, SourceID: "benchmark-seed", Location: time.UTC,
	}); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result, err := MergeSQLite(context.Background(), target, path, Options{
			BatchSize: MaxBatchSize, SourceID: fmt.Sprintf("benchmark-duplicate-%d", i), Location: time.UTC,
		})
		if err != nil {
			b.Fatal(err)
		}
		if result.InsertedRows != 0 || result.VerifiedIdentities != benchmarkRows {
			b.Fatalf("result=%+v", result)
		}
	}
}

func TestLargeFixturePerformance(t *testing.T) {
	path := os.Getenv("CC_OTEL_IMPORT_FIXTURE")
	if path == "" {
		t.Skip("CC_OTEL_IMPORT_FIXTURE is not set")
	}
	target := importTarget(t)
	previewStart := time.Now()
	preview, err := InspectSQLite(context.Background(), path, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	previewElapsed := time.Since(previewStart)
	if previewElapsed > 2*time.Minute {
		t.Fatalf("preview %d rows took %s", preview.SourceRows, previewElapsed)
	}
	t.Logf("preview rows=%d elapsed=%s", preview.SourceRows, previewElapsed)

	importStart := time.Now()
	var currentTable string
	result, err := MergeSQLite(context.Background(), target, path, Options{
		BatchSize: MaxBatchSize, SourceID: "performance", Location: time.Local, TotalRows: preview.SourceRows,
		Progress: func(progress Progress) {
			if progress.Phase == PhaseImporting && progress.CurrentTable != currentTable {
				currentTable = progress.CurrentTable
				t.Logf("import table=%s processed=%d elapsed=%s", currentTable, progress.ProcessedRows, time.Since(importStart))
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	importElapsed := time.Since(importStart)
	if importElapsed > 5*time.Minute {
		t.Fatalf("import %d rows took %s", result.ScannedRows, importElapsed)
	}
	t.Logf("import rows=%d elapsed=%s", result.ScannedRows, importElapsed)

	retryStart := time.Now()
	retry, err := MergeSQLite(context.Background(), target, path, Options{
		BatchSize: MaxBatchSize, SourceID: "performance-retry", Location: time.Local, TotalRows: preview.SourceRows,
	})
	if err != nil || retry.InsertedRows != 0 {
		t.Fatalf("duplicate retry=%+v err=%v", retry, err)
	}
	t.Logf("rows=%d preview=%s import=%s duplicate=%s", preview.SourceRows, previewElapsed, importElapsed, time.Since(retryStart))
}

func benchmarkSQLiteSource(t testing.TB, count int) string {
	t.Helper()
	path := currentFixture(t)
	d := openWritable(t, path)
	defer d.Close()
	tx, err := d.Begin()
	if err != nil {
		t.Fatal(err)
	}
	stmt, err := tx.Prepare(`
		INSERT INTO api_requests(timestamp, session_id, model, input_tokens, output_tokens, duration_ms, request_id)
		VALUES(?, 'benchmark', 'claude', 1, 2, 3, ?)`)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < count; i++ {
		if _, err := stmt.Exec(int64(i+1), fmt.Sprintf("benchmark-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := stmt.Close(); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return path
}
