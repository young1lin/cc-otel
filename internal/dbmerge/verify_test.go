package dbmerge

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/young1lin/cc-otel/internal/config"
	appdb "github.com/young1lin/cc-otel/internal/db"
)

func TestVerifyChecksEveryUniqueSourceIdentity(t *testing.T) {
	target := importTarget(t)
	request := importRequest("verified", 100)
	event := Row{Table: "events", Values: map[string]any{"timestamp": int64(101), "event_name": "same"}}
	insertTestRow(t, target, request)
	insertTestRow(t, target, event)

	verified, err := Verify(context.Background(), target, sliceSource{request, event, event}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if verified != 2 {
		t.Fatalf("verified identities = %d, want 2", verified)
	}
}

func TestVerifyAllowsRepricedRequestAndPendingProcessedChange(t *testing.T) {
	target := importTarget(t)
	mainRequest := importRequest("repriced", 100)
	mainRequest.Values["cost_usd"] = int64(1)
	mainPending := pendingRow("pending", 1)
	mainPending.Values["raw_json"] = `{"main":true}`
	insertTestRow(t, target, mainRequest)
	insertTestRow(t, target, mainPending)
	sourceRequest := importRequest("repriced", 100)
	sourceRequest.Values["cost_usd"] = int64(999)
	sourcePending := pendingRow("pending", 0)
	sourcePending.Values["raw_json"] = `{"source":true}`

	verified, err := Verify(context.Background(), target, sliceSource{sourceRequest, sourcePending}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if verified != 2 {
		t.Fatalf("verified = %d, want 2", verified)
	}
}

func TestVerifyFailsWhenAnyOrdinaryEventIsMissing(t *testing.T) {
	target := importTarget(t)
	missing := Row{Table: "events", Values: map[string]any{"timestamp": int64(1), "event_name": "missing"}}
	_, err := Verify(context.Background(), target, sliceSource{missing}, nil)
	var mergeErr *MergeError
	if !errors.As(err, &mergeErr) || mergeErr.Code != ErrVerification || mergeErr.Table != "events" {
		t.Fatalf("error = %v, want events verification error", err)
	}
}

func TestVerifyBatchesCrossBatchDuplicates(t *testing.T) {
	target := importTarget(t)
	rows := makeRequests(MaxBatchSize)
	if _, err := Import(context.Background(), target, sliceSource(rows), Options{SourceID: "setup", Location: time.UTC}); err != nil {
		t.Fatal(err)
	}
	sourceRows := append(append([]Row(nil), rows...), rows[0])
	var targetStatements int
	verified, err := verifyBatches(context.Background(), target, sliceSource(sourceRows), nil, func(metrics batchMetrics) {
		targetStatements += metrics.TargetStatements
	})
	if err != nil {
		t.Fatal(err)
	}
	if verified != MaxBatchSize {
		t.Fatalf("verified=%d, want %d", verified, MaxBatchSize)
	}
	if targetStatements > 4 {
		t.Fatalf("target statements=%d, want batch-scaled verification", targetStatements)
	}
}

func TestMergeSQLiteRetriesSafelyAfterPartialFailure(t *testing.T) {
	sourcePath := sqliteSourceWithRequests(t, 501)
	target := importTarget(t)
	first := true
	options := Options{
		BatchSize: 500, SourceID: "upload:retry", Location: time.UTC,
		beforeCommit: func(batch int) error {
			if batch == 2 && first {
				first = false
				return errors.New("injected failure")
			}
			return nil
		},
	}
	partial, err := MergeSQLite(context.Background(), target, sourcePath, options)
	if err == nil || partial.InsertedRows != 500 {
		t.Fatalf("partial result=%+v err=%v", partial, err)
	}
	options.beforeCommit = nil
	result, err := MergeSQLite(context.Background(), target, sourcePath, options)
	if err != nil {
		t.Fatal(err)
	}
	if result.InsertedRows != 1 || result.DuplicateRows != 500 || result.VerifiedIdentities != 501 {
		t.Fatalf("retry result = %+v", result)
	}
}

func TestMergeSQLiteKeepsConcurrentRepositoryWriterOnline(t *testing.T) {
	targetPath := filepath.Join(t.TempDir(), "live.db")
	target, err := appdb.Init(&config.Config{DBPath: targetPath})
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	repository := appdb.NewRepository(target)
	defer repository.Close()
	sourcePath := sqliteSourceWithRequests(t, 1001)

	writerResult := make(chan error, 1)
	startedWriter := false
	options := Options{
		BatchSize: 500, SourceID: "upload:wal", Location: time.UTC,
		beforeCommit: func(batch int) error {
			if batch == 1 && !startedWriter {
				startedWriter = true
				go func() {
					inserted, err := repository.InsertRequest(context.Background(), &appdb.APIRequest{
						Timestamp: time.Unix(5000, 0), RequestID: "concurrent-writer",
						SessionID: "live", Model: "claude", InputTokens: 7, OutputTokens: 8,
					})
					if err == nil && !inserted {
						err = fmt.Errorf("concurrent request was not inserted")
					}
					writerResult <- err
				}()
				time.Sleep(20 * time.Millisecond)
			}
			return nil
		},
	}
	result, err := MergeSQLite(context.Background(), target, sourcePath, options)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-writerResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent repository writer did not finish")
	}
	if result.InsertedRows != 1001 {
		t.Fatalf("import result = %+v", result)
	}
	assertCounts(t, target, "api_requests", 1002)
	var mode string
	if err := target.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Fatalf("journal mode = %q, want wal", mode)
	}
}

func sqliteSourceWithRequests(t *testing.T, count int) string {
	t.Helper()
	path := currentFixture(t)
	d := openWritable(t, path)
	for _, row := range makeRequests(count) {
		insertTestRow(t, d, row)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}
