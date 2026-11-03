package api

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/young1lin/cc-otel/internal/config"
	appdb "github.com/young1lin/cc-otel/internal/db"
	"github.com/young1lin/cc-otel/internal/dbmerge"
)

type fakeImportEngine struct {
	inspect      func(context.Context, string, *sql.DB, dbmerge.ProgressFunc) (dbmerge.Inspection, error)
	merge        func(context.Context, *sql.DB, string, dbmerge.Options) (dbmerge.Result, error)
	codexMerge   func(context.Context, *sql.DB, string, dbmerge.Options) (dbmerge.Result, error)
	codexArchive func(context.Context, string) (int64, error)
}

func (f fakeImportEngine) Inspect(ctx context.Context, path string, target *sql.DB, progress dbmerge.ProgressFunc) (dbmerge.Inspection, error) {
	if f.inspect != nil {
		return f.inspect(ctx, path, target, progress)
	}
	return dbmerge.Inspection{SourceRows: 1, NewRows: 1}, nil
}

func (f fakeImportEngine) Merge(ctx context.Context, target *sql.DB, path string, options dbmerge.Options) (dbmerge.Result, error) {
	if f.merge != nil {
		return f.merge(ctx, target, path, options)
	}
	return dbmerge.Result{ScannedRows: 1, InsertedRows: 1, VerifiedIdentities: 1}, nil
}

func (f fakeImportEngine) CodexMerge(ctx context.Context, target *sql.DB, path string, options dbmerge.Options) (dbmerge.Result, error) {
	if f.codexMerge != nil {
		return f.codexMerge(ctx, target, path, options)
	}
	return dbmerge.Result{}, nil
}

func (f fakeImportEngine) CodexArchiveRows(ctx context.Context, path string) (int64, error) {
	if f.codexArchive != nil {
		return f.codexArchive(ctx, path)
	}
	return 0, nil
}

// TestImportLegacyCodexBackupLandsInUnifiedTables pins the import-dialog
// path for HISTORICAL Codex backups: a legacy file whose unified tables
// predate the AllClient schema (generic pipeline rejects it) but which
// carries a codex_* archive routes the whole job through the
// codex→unified mapping — token normalization, correct target tables,
// column tolerance for missing archive columns, and idempotence.
func TestImportLegacyCodexBackupLandsInUnifiedTables(t *testing.T) {
	legacyPath := filepath.Join(t.TempDir(), "legacy.db")
	lg, err := sql.Open("sqlite3", legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	// Old-shape api_requests: no service_name/prompt_id — the generic
	// registry must reject this file.
	if _, err := lg.Exec(`CREATE TABLE api_requests (
		id INTEGER PRIMARY KEY AUTOINCREMENT, timestamp INTEGER NOT NULL,
		session_id TEXT DEFAULT '', model TEXT DEFAULT '',
		input_tokens INTEGER DEFAULT 0, output_tokens INTEGER DEFAULT 0,
		request_id TEXT DEFAULT '')`); err != nil {
		t.Fatal(err)
	}
	if _, err := lg.Exec(`INSERT INTO api_requests (timestamp, session_id, model, input_tokens, output_tokens, request_id) VALUES (?, 'legacy-claude', 'claude-x', 10, 5, 'req-legacy-1')`,
		time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	// Codex archive in an ANCIENT shape: tool_tokens (no total_tokens),
	// no host_arch/os_type/error_message columns at all.
	if _, err := lg.Exec(`CREATE TABLE codex_api_requests (
		id INTEGER PRIMARY KEY AUTOINCREMENT, timestamp INTEGER NOT NULL,
		session_id TEXT DEFAULT '', user_id TEXT DEFAULT '', model TEXT DEFAULT '',
		input_tokens INTEGER DEFAULT 0, output_tokens INTEGER DEFAULT 0,
		cache_read_tokens INTEGER DEFAULT 0, tool_tokens INTEGER DEFAULT 0,
		cost_usd INTEGER DEFAULT 0, duration_ms INTEGER DEFAULT 0, ttft_ms INTEGER DEFAULT 0,
		conversation_id TEXT DEFAULT '')`); err != nil {
		t.Fatal(err)
	}
	// input 300 INCLUSIVE of cache_read 100 → unified input must be 200.
	if _, err := lg.Exec(`INSERT INTO codex_api_requests
		(timestamp, session_id, model, input_tokens, output_tokens, cache_read_tokens, tool_tokens, conversation_id)
		VALUES (?, 'legacy-codex-conv', 'gpt-5.1', 300, 40, 100, 340, 'legacy-codex-conv')`,
		time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := lg.Exec(`CREATE TABLE codex_tool_result_events (
		id INTEGER PRIMARY KEY AUTOINCREMENT, timestamp INTEGER NOT NULL,
		session_id TEXT DEFAULT '', conversation_id TEXT DEFAULT '',
		tool_name TEXT DEFAULT '', call_id TEXT DEFAULT '',
		success INTEGER DEFAULT 0, duration_ms INTEGER DEFAULT 0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := lg.Exec(`INSERT INTO codex_tool_result_events
		(timestamp, session_id, conversation_id, tool_name, call_id, success, duration_ms)
		VALUES (?, 'legacy-codex-conv', 'legacy-codex-conv', 'shell', 'call-1', 1, 250)`,
		time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	if err := lg.Close(); err != nil {
		t.Fatal(err)
	}
	fileBytes, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatal(err)
	}

	temp := t.TempDir()
	dbPath := filepath.Join(temp, "main.db")
	tdb, err := appdb.Init(&config.Config{DBPath: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	defer tdb.Close()
	manager, err := newImportManager(context.Background(), tdb, dbPath, NewBroker(), defaultImportEngine{})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.close()

	reservation, err := manager.reserveUpload("legacy-backup.db")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reservation.Path, fileBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := manager.completeUpload(reservation.JobID, int64(len(fileBytes)), "deadbeef"); err != nil {
		t.Fatal(err)
	}

	ready := waitImportState(t, manager, importReady)
	if ready.Preview == nil || len(ready.Preview.Tables) != 1 {
		t.Fatalf("legacy file must preview as codex-archive-only, got %+v", ready.Preview)
	}
	if !strings.Contains(ready.Preview.Tables[0].Name, "codex archive") {
		t.Fatalf("preview table must name the codex archive: %q", ready.Preview.Tables[0].Name)
	}
	if ready.Preview.SourceRows != 2 {
		t.Fatalf("preview must count both archive rows, got %d", ready.Preview.SourceRows)
	}
	if len(ready.Preview.Warnings) == 0 || !strings.Contains(ready.Preview.Warnings[0], "legacy") {
		t.Fatalf("preview must warn about the legacy schema: %+v", ready.Preview.Warnings)
	}

	if _, err := manager.start(reservation.JobID); err != nil {
		t.Fatal(err)
	}
	done := waitImportState(t, manager, importSucceeded)
	if done.Result == nil || done.Result.InsertedRows != 2 {
		t.Fatalf("merge must import both archive rows, got %+v", done.Result)
	}

	var in, cr, tot int64
	var svc string
	if err := tdb.QueryRow(`SELECT input_tokens, cache_read_tokens, total_tokens, service_name
		FROM api_requests WHERE conversation_id='legacy-codex-conv'`).Scan(&in, &cr, &tot, &svc); err != nil {
		t.Fatal(err)
	}
	if in != 200 || cr != 100 || tot != 340 || svc != "codex" {
		t.Fatalf("token normalization wrong: in=%d cr=%d tot=%d svc=%s (want 200/100/340/codex)", in, cr, tot, svc)
	}
	var claudeN int
	tdb.QueryRow("SELECT COUNT(*) FROM api_requests WHERE request_id='req-legacy-1'").Scan(&claudeN)
	if claudeN != 0 {
		t.Fatal("legacy claude rows must not be imported by the codex-only route")
	}
	var toolMsg string
	if err := tdb.QueryRow("SELECT error_message FROM tool_result_events WHERE conversation_id='legacy-codex-conv'").Scan(&toolMsg); err != nil {
		t.Fatal(err)
	}
	if toolMsg != "" {
		t.Fatalf("missing archive column must default to empty, got %q", toolMsg)
	}

	// Idempotence: re-upload of the same legacy file imports nothing.
	reservation2, err := manager.reserveUpload("legacy-backup.db")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reservation2.Path, fileBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := manager.completeUpload(reservation2.JobID, int64(len(fileBytes)), "deadbeef"); err != nil {
		t.Fatal(err)
	}
	waitImportState(t, manager, importReady)
	if _, err := manager.start(reservation2.JobID); err != nil {
		t.Fatal(err)
	}
	done2 := waitImportState(t, manager, importSucceeded)
	if done2.Result == nil || done2.Result.InsertedRows != 0 {
		t.Fatalf("re-import must be a no-op, got %+v", done2.Result)
	}
}

func TestImportManagerTransitionsInspectReadyImportSucceeded(t *testing.T) {
	manager := newTestImportManager(t, fakeImportEngine{})
	jobID, path := uploadForTest(t, manager)
	ready := waitImportState(t, manager, importReady)
	if ready.JobID != jobID || ready.Preview == nil || ready.Preview.NewRows != 1 {
		t.Fatalf("ready status = %+v", ready)
	}
	if _, err := manager.start(jobID); err != nil {
		t.Fatal(err)
	}
	done := waitImportState(t, manager, importSucceeded)
	if done.Result == nil || done.Result.InsertedRows != 1 {
		t.Fatalf("result = %+v", done)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("successful upload still exists: %v", err)
	}
}

func TestImportManagerUsesMaximumLogicalBatchSize(t *testing.T) {
	optionsSeen := make(chan dbmerge.Options, 1)
	manager := newTestImportManager(t, fakeImportEngine{
		merge: func(_ context.Context, _ *sql.DB, _ string, options dbmerge.Options) (dbmerge.Result, error) {
			optionsSeen <- options
			return dbmerge.Result{ScannedRows: 1, InsertedRows: 1, VerifiedIdentities: 1}, nil
		},
	})
	jobID, _ := uploadForTest(t, manager)
	waitImportState(t, manager, importReady)
	if _, err := manager.start(jobID); err != nil {
		t.Fatal(err)
	}
	waitImportState(t, manager, importSucceeded)
	if options := <-optionsSeen; options.BatchSize != dbmerge.MaxBatchSize {
		t.Fatalf("batch size=%d, want %d", options.BatchSize, dbmerge.MaxBatchSize)
	}
}

func TestImportManagerAllowsOnlyOneActiveJob(t *testing.T) {
	manager := newTestImportManager(t, fakeImportEngine{})
	if _, err := manager.reserveUpload("one.db"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.reserveUpload("two.db"); !errors.Is(err, errImportBusy) {
		t.Fatalf("second reservation error = %v", err)
	}
}

func TestImportManagerFailedImportIsRetryableForOneHour(t *testing.T) {
	now := time.Unix(2_000_000, 0)
	manager := newTestImportManager(t, fakeImportEngine{
		merge: func(context.Context, *sql.DB, string, dbmerge.Options) (dbmerge.Result, error) {
			return dbmerge.Result{InsertedRows: 3}, errors.New("merge failed")
		},
	})
	manager.now = func() time.Time { return now }
	jobID, path := uploadForTest(t, manager)
	waitImportState(t, manager, importReady)
	if _, err := manager.start(jobID); err != nil {
		t.Fatal(err)
	}
	failed := waitImportState(t, manager, importFailed)
	if !failed.Retryable || failed.ExpiresAt != now.Add(time.Hour).Unix() {
		t.Fatalf("failed status = %+v", failed)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("retryable upload missing: %v", err)
	}
}

func TestImportManagerInspectionFailureIsNotRetryable(t *testing.T) {
	manager := newTestImportManager(t, fakeImportEngine{
		inspect: func(context.Context, string, *sql.DB, dbmerge.ProgressFunc) (dbmerge.Inspection, error) {
			return dbmerge.Inspection{}, &dbmerge.MergeError{Code: dbmerge.ErrUnsupportedSchema, Err: errors.New("future table")}
		},
	})
	uploadForTest(t, manager)
	failed := waitImportState(t, manager, importFailed)
	if failed.Retryable || failed.Error == nil || failed.Error.Code != string(dbmerge.ErrUnsupportedSchema) {
		t.Fatalf("failed status = %+v", failed)
	}
}

func TestImportManagerDeleteRejectsImporting(t *testing.T) {
	release := make(chan struct{})
	manager := newTestImportManager(t, fakeImportEngine{
		merge: func(ctx context.Context, _ *sql.DB, _ string, _ dbmerge.Options) (dbmerge.Result, error) {
			select {
			case <-release:
				return dbmerge.Result{}, nil
			case <-ctx.Done():
				return dbmerge.Result{}, ctx.Err()
			}
		},
	})
	jobID, _ := uploadForTest(t, manager)
	waitImportState(t, manager, importReady)
	if _, err := manager.start(jobID); err != nil {
		t.Fatal(err)
	}
	waitImportState(t, manager, importImporting)
	if err := manager.delete(jobID); !errors.Is(err, errImportInProgress) {
		t.Fatalf("delete error = %v", err)
	}
	close(release)
}

func TestImportManagerSuccessDeletesUploadImmediately(t *testing.T) {
	manager := newTestImportManager(t, fakeImportEngine{})
	jobID, path := uploadForTest(t, manager)
	waitImportState(t, manager, importReady)
	if _, err := manager.start(jobID); err != nil {
		t.Fatal(err)
	}
	waitImportState(t, manager, importSucceeded)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("upload stat error = %v, want not exist", err)
	}
}

func TestImportManagerSweepsFailedAfterOneHourAndReadyAfterTwentyFour(t *testing.T) {
	now := time.Unix(3_000_000, 0)
	manager := newTestImportManager(t, fakeImportEngine{})
	manager.now = func() time.Time { return now }

	_, failedPath := uploadForTest(t, manager)
	waitImportState(t, manager, importReady)
	manager.mu.Lock()
	manager.job.State = importFailed
	manager.job.UpdatedAt = now.Add(-time.Hour - time.Second).Unix()
	manager.job.Retryable = true
	manager.mu.Unlock()
	if err := manager.sweep(false); err != nil {
		t.Fatal(err)
	}
	if status, ok := manager.status(""); ok || status != nil {
		t.Fatalf("failed job was not swept: %+v", status)
	}
	if _, err := os.Stat(failedPath); !os.IsNotExist(err) {
		t.Fatalf("failed upload remains: %v", err)
	}

	_, readyPath := uploadForTest(t, manager)
	waitImportState(t, manager, importReady)
	manager.mu.Lock()
	manager.job.UpdatedAt = now.Add(-24*time.Hour - time.Second).Unix()
	manager.mu.Unlock()
	if err := manager.sweep(false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(readyPath); !os.IsNotExist(err) {
		t.Fatalf("ready upload remains: %v", err)
	}
}

func TestImportManagerStartupRemovesOrphansOlderThanTwentyFourHours(t *testing.T) {
	temp := t.TempDir()
	dbPath := filepath.Join(temp, "main.db")
	d, err := appdb.Init(&config.Config{DBPath: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	dir := filepath.Join(temp, ".imports")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	old := []string{filepath.Join(dir, "orphan.db.upload"), filepath.Join(dir, ".inspect-old.db"), filepath.Join(dir, ".inspect-old.db-wal")}
	for _, path := range old {
		if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
		stamp := time.Now().Add(-25 * time.Hour)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	unrelated := filepath.Join(dir, "keep.txt")
	if err := os.WriteFile(unrelated, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err := newImportManager(context.Background(), d, dbPath, NewBroker(), fakeImportEngine{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.close)
	for _, path := range old {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("orphan remains: %s", path)
		}
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Fatalf("unrelated file removed: %v", err)
	}
}

func TestImportManagerCloseCancelsWorkWithoutReportingSuccess(t *testing.T) {
	started := make(chan struct{})
	manager := newTestImportManager(t, fakeImportEngine{
		inspect: func(ctx context.Context, _ string, _ *sql.DB, _ dbmerge.ProgressFunc) (dbmerge.Inspection, error) {
			close(started)
			<-ctx.Done()
			return dbmerge.Inspection{SourceRows: 1}, nil
		},
	})
	uploadForTest(t, manager)
	<-started
	manager.close()
	status, _ := manager.status("")
	if status != nil && (status.State == importReady || status.State == importSucceeded) {
		t.Fatalf("cancelled manager reported success: %+v", status)
	}
}

func TestImportManagerSnapshotsAreRaceSafe(t *testing.T) {
	release := make(chan struct{})
	manager := newTestImportManager(t, fakeImportEngine{
		inspect: func(ctx context.Context, _ string, _ *sql.DB, progress dbmerge.ProgressFunc) (dbmerge.Inspection, error) {
			for i := int64(0); i < 500; i++ {
				progress(dbmerge.Progress{Phase: dbmerge.PhaseInspecting, ProcessedRows: i, TotalRows: 500})
			}
			select {
			case <-release:
				return dbmerge.Inspection{SourceRows: 500}, nil
			case <-ctx.Done():
				return dbmerge.Inspection{}, ctx.Err()
			}
		},
	})
	uploadForTest(t, manager)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 500; j++ {
				status, _ := manager.status("")
				if status != nil && status.Preview != nil {
					_ = len(status.Preview.Tables)
				}
			}
		}()
	}
	wg.Wait()
	close(release)
	waitImportState(t, manager, importReady)
}

func newTestImportManager(t *testing.T, engine importEngine) *importManager {
	t.Helper()
	temp := t.TempDir()
	dbPath := filepath.Join(temp, "main.db")
	d, err := appdb.Init(&config.Config{DBPath: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	manager, err := newImportManager(context.Background(), d, dbPath, NewBroker(), engine)
	if err != nil {
		d.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		manager.close()
		d.Close()
	})
	return manager
}

func uploadForTest(t *testing.T, manager *importManager) (string, string) {
	t.Helper()
	reservation, err := manager.reserveUpload("source.db")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reservation.Path, []byte("sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := manager.completeUpload(reservation.JobID, 6, "abc123"); err != nil {
		t.Fatal(err)
	}
	return reservation.JobID, reservation.Path
}

func waitImportState(t *testing.T, manager *importManager, want importState) *ImportJobStatus {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		status, ok := manager.status("")
		if ok && status.State == want {
			return status
		}
		time.Sleep(time.Millisecond)
	}
	status, _ := manager.status("")
	t.Fatalf("job state = %+v, want %s", status, want)
	return nil
}
