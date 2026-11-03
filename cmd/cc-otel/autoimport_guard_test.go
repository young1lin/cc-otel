package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/young1lin/cc-otel/internal/config"
	"github.com/young1lin/cc-otel/internal/db"
)

func newGuardTestDB(t *testing.T) *db.Repository {
	t.Helper()
	d, err := db.Init(&config.Config{DBPath: filepath.Join(t.TempDir(), "guard.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	repo := db.NewRepository(d)
	t.Cleanup(func() { repo.Close() })
	// marker/ranges tables are created by the auto-import path itself
	if err := ensureAutoImportTables(context.Background(), repo.DB()); err != nil {
		t.Fatalf("ensure tables: %v", err)
	}
	return repo
}

// T9 (R3 review A5-F1): even if the recorded (min,max] range wrongly covers
// live rows — two daemons sharing a DB, or the import moved after bind — the
// service guard on the purge DELETE must spare non-codex rows.
func TestAutoPurge_Guard_SparesNonCodexRowsInRange(t *testing.T) {
	repo := newGuardTestDB(t)
	ctx := context.Background()
	now := time.Now()
	if _, err := repo.DB().Exec(`INSERT INTO api_requests
		(timestamp, session_id, model, service_name, input_tokens, output_tokens, total_tokens)
		VALUES (?, 'k1', 'glm-5.3', 'codex', 1, 1, 2), (?, 'k2', 'claude-opus-4-7', 'claude-code', 1, 1, 2)`,
		now.Unix(), now.Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.DB().Exec(`INSERT INTO codex_auto_import (imported_at) VALUES (?)`,
		now.Add(-40*24*time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.DB().Exec(`INSERT INTO codex_auto_import_ranges (import_id, table_name, min_id, max_id)
		SELECT 1, 'api_requests', MIN(id), MAX(id) FROM api_requests`); err != nil {
		t.Fatal(err)
	}
	m, found, err := loadAutoImportMarker(ctx, repo.DB())
	if err != nil || !found {
		t.Fatalf("marker: found=%v err=%v", found, err)
	}
	if err := autoPurgeImported(ctx, repo.DB(), repo, m); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := repo.DB().QueryRow(`SELECT COUNT(*) FROM api_requests WHERE session_id='k2'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatal("guard failed: live claude-code row inside the range was deleted")
	}
	if err := repo.DB().QueryRow(`SELECT COUNT(*) FROM api_requests WHERE session_id='k1'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("codex row inside the range must still be purged")
	}
}

// T10 (R3 review A5-F3): marker imported_at in the future means the wall
// clock jumped forward — purge must defer instead of firing early.
func TestAutoPurge_Guard_RejectsFutureMarker(t *testing.T) {
	repo := newGuardTestDB(t)
	ctx := context.Background()
	if _, err := repo.DB().Exec(`INSERT INTO api_requests
		(timestamp, session_id, model, service_name, input_tokens, output_tokens, total_tokens)
		VALUES (?, 'k1', 'glm-5.3', 'codex', 1, 1, 2)`, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.DB().Exec(`INSERT INTO codex_auto_import (imported_at) VALUES (?)`,
		time.Now().Add(24*time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.DB().Exec(`INSERT INTO codex_auto_import_ranges (import_id, table_name, min_id, max_id)
		SELECT 1, 'api_requests', MIN(id), MAX(id) FROM api_requests`); err != nil {
		t.Fatal(err)
	}
	m, found, err := loadAutoImportMarker(ctx, repo.DB())
	if err != nil || !found {
		t.Fatalf("marker: found=%v err=%v", found, err)
	}
	if err := autoPurgeImported(ctx, repo.DB(), repo, m); err == nil {
		t.Fatal("future marker must be rejected, not purged")
	}
	var n int
	if err := repo.DB().QueryRow(`SELECT COUNT(*) FROM api_requests`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatal("no row may be deleted when the marker timestamp is implausible")
	}
}

// T11 (R3 review A1-F2): a range row referencing a table outside the plan
// must abort the purge — the table name is spliced into the DELETE.
func TestAutoPurge_Guard_RejectsUnexpectedTable(t *testing.T) {
	repo := newGuardTestDB(t)
	ctx := context.Background()
	if _, err := repo.DB().Exec(`INSERT INTO codex_auto_import (imported_at) VALUES (?)`,
		time.Now().Add(-40*24*time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.DB().Exec(`INSERT INTO codex_auto_import_ranges (import_id, table_name, min_id, max_id)
		VALUES (1, 'sessions', 1, 5)`); err != nil {
		t.Fatal(err)
	}
	m, found, err := loadAutoImportMarker(ctx, repo.DB())
	if err != nil || !found {
		t.Fatalf("marker: found=%v err=%v", found, err)
	}
	if err := autoPurgeImported(ctx, repo.DB(), repo, m); err == nil {
		t.Fatal("range with unexpected table must be rejected")
	}
}
