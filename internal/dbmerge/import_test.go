package dbmerge

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"
)

type sliceSource []Row

func (s sliceSource) Scan(ctx context.Context, yield func(Row) error) error {
	for _, row := range s {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := yield(row); err != nil {
			return err
		}
	}
	return nil
}

func TestImportKeepsMainRowOnIdentityConflict(t *testing.T) {
	target := importTarget(t)
	main := requestRow("same-request", 100)
	insertTestRow(t, target, main)
	source := requestRow("same-request", 999)
	source.Values["model"] = "changed-model"

	result, err := Import(context.Background(), target, sliceSource{source}, Options{SourceID: "upload:test", Location: time.UTC})
	if err != nil {
		t.Fatal(err)
	}
	if result.InsertedRows != 0 || result.DuplicateRows != 1 {
		t.Fatalf("result = %+v", result)
	}
	var cost int64
	var model string
	if err := target.QueryRow(`SELECT cost_usd, model FROM api_requests WHERE request_id='same-request'`).Scan(&cost, &model); err != nil {
		t.Fatal(err)
	}
	if cost != 100 || model != "claude" {
		t.Fatalf("main row changed: cost=%d model=%q", cost, model)
	}
}

func TestImportSameSourceTwiceIsIdempotent(t *testing.T) {
	target := importTarget(t)
	row := Row{Table: "events", Values: map[string]any{"timestamp": int64(100), "event_name": "once", "cost_usd": float64(1.5)}}
	for pass := 0; pass < 2; pass++ {
		result, err := Import(context.Background(), target, sliceSource{row}, Options{SourceID: "upload:test", Location: time.UTC})
		if err != nil {
			t.Fatal(err)
		}
		if pass == 0 && result.InsertedRows != 1 {
			t.Fatalf("first result = %+v", result)
		}
		if pass == 1 && (result.InsertedRows != 0 || result.DuplicateRows != 1) {
			t.Fatalf("second result = %+v", result)
		}
	}
	assertCounts(t, target, "events", 1)
	assertCounts(t, target, "import_ledger", 1)
}

func TestImportStaleLedgerDoesNotSkipMissingDetail(t *testing.T) {
	target := importTarget(t)
	row := requestRow("stale", 12)
	id, _ := LedgerID(row)
	if _, err := target.Exec(`INSERT INTO import_ledger(uuid, imported_at, source_db, table_name) VALUES(?, 1, 'old', ?)`, id, row.Table); err != nil {
		t.Fatal(err)
	}
	result, err := Import(context.Background(), target, sliceSource{row}, Options{SourceID: "upload:new", Location: time.UTC})
	if err != nil {
		t.Fatal(err)
	}
	if result.InsertedRows != 1 {
		t.Fatalf("result = %+v", result)
	}
	assertCounts(t, target, "api_requests", 1)
}

func TestImportClampsBatchSizeToTenThousand(t *testing.T) {
	target := importTarget(t)
	rows := makeRequests(10_001)
	var commits int
	result, err := Import(context.Background(), target, sliceSource(rows), Options{
		BatchSize: 20_000,
		SourceID:  "upload:test",
		Location:  time.UTC,
		beforeCommit: func(int) error {
			commits++
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.InsertedRows != 10_001 || result.VerifiedIdentities != 10_001 || commits != 2 {
		t.Fatalf("result=%+v commit hooks=%d, want 10001 rows in two batches", result, commits)
	}
}

func TestImportBatchCommitsDetailAggregateAndLedgerTogether(t *testing.T) {
	target := importTarget(t)
	row := importRequest("atomic", 100)
	_, err := Import(context.Background(), target, sliceSource{row}, Options{
		SourceID: "upload:test", Location: time.UTC,
		beforeCommit: func(int) error { return errors.New("rollback") },
	})
	if err == nil {
		t.Fatal("expected rollback")
	}
	assertCounts(t, target, "api_requests", 0)
	assertCounts(t, target, "daily_model_agg", 0)
	assertCounts(t, target, "import_ledger", 0)
}

func TestImportBatchFailureRollsBackOnlyCurrentBatch(t *testing.T) {
	target := importTarget(t)
	options := Options{
		BatchSize: MaxBatchSize,
		SourceID:  "upload:test",
		Location:  time.UTC,
		beforeCommit: func(batch int) error {
			if batch == 2 {
				return errors.New("injected batch failure")
			}
			return nil
		},
	}
	result, err := Import(context.Background(), target, sliceSource(makeRequests(10_001)), options)
	if err == nil {
		t.Fatal("expected injected failure")
	}
	if result.InsertedRows != 10_000 || result.VerifiedIdentities != 10_000 {
		t.Fatalf("partial result=%+v, want first 10000 rows committed and verified", result)
	}
	assertCounts(t, target, "api_requests", 10_000)
	assertAggRequestCount(t, target, "daily_model_agg", 10_000)
	assertCounts(t, target, "import_ledger", 10_000)
}

func TestImportCountsWithinBatchDuplicateOnceForVerification(t *testing.T) {
	target := importTarget(t)
	row := Row{Table: "events", Values: map[string]any{"timestamp": int64(1), "event_name": "same"}}
	result, err := Import(context.Background(), target, sliceSource{row, row}, Options{SourceID: "upload:test"})
	if err != nil {
		t.Fatal(err)
	}
	if result.ScannedRows != 2 || result.InsertedRows != 1 || result.DuplicateRows != 1 || result.VerifiedIdentities != 1 {
		t.Fatalf("result=%+v", result)
	}
	assertCounts(t, target, "events", 1)
	assertCounts(t, target, "import_ledger", 1)
}

func TestImportCountsCrossBatchDuplicateOnceForVerification(t *testing.T) {
	target := importTarget(t)
	first := Row{Table: "events", Values: map[string]any{"timestamp": int64(1), "event_name": "first"}}
	second := Row{Table: "events", Values: map[string]any{"timestamp": int64(2), "event_name": "second"}}
	result, err := Import(context.Background(), target, sliceSource{first, second, first}, Options{
		BatchSize: 2, SourceID: "upload:test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ScannedRows != 3 || result.InsertedRows != 2 || result.DuplicateRows != 1 || result.VerifiedIdentities != 2 {
		t.Fatalf("result=%+v", result)
	}
}

func TestImportScansSourceExactlyOnce(t *testing.T) {
	target := importTarget(t)
	source := &countingSource{rows: sliceSource{requestRow("once", 1)}}
	result, err := Import(context.Background(), target, source, Options{SourceID: "upload:test", Location: time.UTC})
	if err != nil {
		t.Fatal(err)
	}
	if source.scans != 1 || result.VerifiedIdentities != 1 {
		t.Fatalf("scans=%d result=%+v", source.scans, result)
	}
}

func TestImportEmitsIntegratedVerificationProgress(t *testing.T) {
	target := importTarget(t)
	var phases []Phase
	_, err := Import(context.Background(), target, sliceSource{requestRow("progress", 1)}, Options{
		SourceID: "upload:test",
		Progress: func(value Progress) {
			phases = append(phases, value.Phase)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(phases) < 2 || phases[len(phases)-2] != PhaseImporting || phases[len(phases)-1] != PhaseVerifying {
		t.Fatalf("phases=%v, want importing followed by integrated verifying", phases)
	}
}

func TestImportCopiesCostWithoutRecompute(t *testing.T) {
	target := importTarget(t)
	row := importRequest("cost", 100)
	row.Values["cost_usd"] = int64(12345)
	if _, err := Import(context.Background(), target, sliceSource{row}, Options{SourceID: "upload:test", Location: time.UTC}); err != nil {
		t.Fatal(err)
	}
	var detail, aggregate int64
	if err := target.QueryRow(`SELECT cost_usd FROM api_requests WHERE request_id='cost'`).Scan(&detail); err != nil {
		t.Fatal(err)
	}
	if err := target.QueryRow(`SELECT cost_usd FROM daily_model_agg WHERE model='claude'`).Scan(&aggregate); err != nil {
		t.Fatal(err)
	}
	if detail != 12345 || aggregate != 12345 {
		t.Fatalf("cost detail=%d aggregate=%d", detail, aggregate)
	}
}

func TestImportPendingKeepsMainProcessedState(t *testing.T) {
	target := importTarget(t)
	main := pendingRow("pending-request", 1)
	main.Values["raw_json"] = `{"main":true}`
	insertTestRow(t, target, main)
	source := pendingRow("pending-request", 0)
	source.Values["raw_json"] = `{"source":true}`
	result, err := Import(context.Background(), target, sliceSource{source}, Options{SourceID: "upload:test"})
	if err != nil {
		t.Fatal(err)
	}
	if result.DuplicateRows != 1 {
		t.Fatalf("result = %+v", result)
	}
	var processed int64
	var raw string
	if err := target.QueryRow(`SELECT processed, raw_json FROM pending_ttft_spans WHERE request_id='pending-request'`).Scan(&processed, &raw); err != nil {
		t.Fatal(err)
	}
	if processed != 1 || raw != `{"main":true}` {
		t.Fatalf("pending row changed: processed=%d raw=%s", processed, raw)
	}
}

func TestImportCodexToolTokensFeedsTotalTokenAggregate(t *testing.T) {
	target := importTarget(t)
	row := Row{Table: "codex_api_requests", Values: map[string]any{
		"timestamp": int64(100), "session_id": "codex", "model": "gpt",
		"input_tokens": int64(10), "output_tokens": int64(20), "duration_ms": int64(30),
		"total_tokens": int64(42), "cost_usd": int64(7),
	}}
	if _, err := Import(context.Background(), target, sliceSource{row}, Options{SourceID: "upload:test", Location: time.UTC}); err != nil {
		t.Fatal(err)
	}
	var total int64
	if err := target.QueryRow(`SELECT total_tokens FROM codex_daily_model_agg WHERE model='gpt'`).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total != 42 {
		t.Fatalf("aggregate total_tokens = %d, want 42", total)
	}
}

func importTarget(t testing.TB) *sql.DB {
	t.Helper()
	d := openWritable(t, currentFixture(t))
	t.Cleanup(func() { d.Close() })
	return d
}

func importRequest(id string, timestamp int64) Row {
	row := requestRow(id, 10)
	row.Values["timestamp"] = timestamp
	row.Values["model"] = "claude"
	row.Values["input_tokens"] = int64(1)
	row.Values["output_tokens"] = int64(2)
	row.Values["cache_read_tokens"] = int64(3)
	row.Values["cache_creation_tokens"] = int64(4)
	return row
}

func makeRequests(count int) []Row {
	rows := make([]Row, count)
	for i := range rows {
		rows[i] = importRequest(fmt.Sprintf("request-%04d", i), int64(100+i))
	}
	return rows
}

type countingSource struct {
	rows  sliceSource
	scans int
}

func (s *countingSource) Scan(ctx context.Context, yield func(Row) error) error {
	s.scans++
	return s.rows.Scan(ctx, yield)
}

func assertCounts(t *testing.T, target *sql.DB, table string, want int64) {
	t.Helper()
	if got := tableCount(t, target, table); got != want {
		t.Fatalf("%s count = %d, want %d", table, got, want)
	}
}

func assertAggRequestCount(t *testing.T, target *sql.DB, table string, want int64) {
	t.Helper()
	var got int64
	if err := target.QueryRow(`SELECT COALESCE(SUM(request_count), 0) FROM "` + table + `"`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("%s request count = %d, want %d", table, got, want)
	}
}
