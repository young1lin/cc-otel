package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/young1lin/cc-otel/internal/config"
	"github.com/young1lin/cc-otel/internal/db"
)

func newAutoTestDB(t *testing.T) *db.Repository {
	t.Helper()
	d, err := db.Init(&config.Config{DBPath: filepath.Join(t.TempDir(), "auto.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	repo := db.NewRepository(d)
	t.Cleanup(func() { repo.Close() })
	return repo
}

func seedArchive(t *testing.T, repo *db.Repository, conv string, when time.Time) {
	t.Helper()
	if _, err := repo.DB().Exec(`INSERT INTO codex_api_requests
		(timestamp, session_id, model, input_tokens, output_tokens, total_tokens) VALUES (?, ?, 'glm-5.3', 100, 40, 140)`,
		when.Unix(), conv); err != nil {
		t.Fatal(err)
	}
}

func codexUnifiedCount(t *testing.T, repo *db.Repository) int {
	t.Helper()
	var n int
	if err := repo.DB().QueryRow("SELECT COUNT(*) FROM api_requests WHERE service_name='codex'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// T1: no archive - no marker, nothing imported.
func TestAutoImport_SkipsEmptyArchive(t *testing.T) {
	repo := newAutoTestDB(t)
	ctx := context.Background()
	if err := autoImportCodex(ctx, repo.DB(), repo, 90); err != nil {
		t.Fatal(err)
	}
	_, found, err := loadAutoImportMarker(ctx, repo.DB())
	if err != nil || found {
		t.Fatalf("no marker expected for empty archive: found=%v err=%v", found, err)
	}
}

// T2: archive present - import once with marker+ranges; second run skips.
func TestAutoImport_ImportsOnceThenSkips(t *testing.T) {
	repo := newAutoTestDB(t)
	ctx := context.Background()
	seedArchive(t, repo, "auto-conv-1", time.Now().Add(-48*time.Hour))

	if err := autoImportCodex(ctx, repo.DB(), repo, 90); err != nil {
		t.Fatal(err)
	}
	if got := codexUnifiedCount(t, repo); got != 1 {
		t.Fatalf("auto-import must land the archive row: got %d", got)
	}
	m, found, err := loadAutoImportMarker(ctx, repo.DB())
	if err != nil || !found {
		t.Fatalf("marker expected: found=%v err=%v", found, err)
	}
	if m.purged {
		t.Fatal("fresh marker must not be purged")
	}
	ranges, err := loadAutoImportRanges(ctx, repo.DB(), m.id)
	if err != nil {
		t.Fatal(err)
	}
	if len(ranges) == 0 {
		t.Fatal("id ranges expected for the api_requests import")
	}

	// Second boot: skipped entirely; a NEW archive row is NOT imported
	// (the auto phase is over - the manual button covers deliberate top-ups).
	seedArchive(t, repo, "auto-conv-2", time.Now().Add(-24*time.Hour))
	if err := autoImportCodex(ctx, repo.DB(), repo, 90); err != nil {
		t.Fatal(err)
	}
	if got := codexUnifiedCount(t, repo); got != 1 {
		t.Fatalf("second auto run must skip: got %d rows", got)
	}
}

// T3: 30 days elapsed - exactly the auto-imported ranges are purged, agg is
// rebuilt, marker flips purged; later boots no-op.
func TestAutoImport_PurgesAfter30Days(t *testing.T) {
	repo := newAutoTestDB(t)
	ctx := context.Background()
	seedArchive(t, repo, "auto-purge-conv", time.Now().Add(-40*24*time.Hour))
	if err := autoImportCodex(ctx, repo.DB(), repo, 90); err != nil {
		t.Fatal(err)
	}
	// A LIVE codex row after the import - must survive the purge.
	if _, err := repo.DB().Exec(`INSERT INTO api_requests
		(timestamp, session_id, conversation_id, model, service_name, event_name, input_tokens, output_tokens, total_tokens)
		VALUES (?, 'live-conv', 'live-conv', 'glm-5.3', 'codex', 'codex.api_request', 5, 5, 10)`,
		time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	if got := codexUnifiedCount(t, repo); got != 2 {
		t.Fatalf("setup: want 2 codex rows (imported + live), got %d", got)
	}

	// Age the marker past the purge deadline.
	if _, err := repo.DB().Exec("UPDATE codex_auto_import SET imported_at = ?", time.Now().Add(-31*24*time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	if err := autoImportCodex(ctx, repo.DB(), repo, 90); err != nil {
		t.Fatal(err)
	}
	if got := codexUnifiedCount(t, repo); got != 1 {
		t.Fatalf("purge must remove only the auto-imported row: got %d rows left", got)
	}
	var live string
	if err := repo.DB().QueryRow("SELECT session_id FROM api_requests WHERE service_name='codex'").Scan(&live); err != nil {
		t.Fatal(err)
	}
	if live != "live-conv" {
		t.Fatalf("purge removed the wrong row: %s", live)
	}
	var aggN int
	if err := repo.DB().QueryRow("SELECT COALESCE(SUM(request_count),0) FROM daily_model_agg WHERE service_name='codex'").Scan(&aggN); err != nil {
		t.Fatal(err)
	}
	if aggN != 1 {
		t.Fatalf("agg must be rebuilt to the surviving live row: got %d", aggN)
	}
	m, _, err := loadAutoImportMarker(ctx, repo.DB())
	if err != nil {
		t.Fatal(err)
	}
	if !m.purged {
		t.Fatal("marker must be purged=1")
	}

	// Later boots: full no-op (marker purged), even if new archive rows appear.
	seedArchive(t, repo, "auto-after-purge", time.Now())
	if err := autoImportCodex(ctx, repo.DB(), repo, 90); err != nil {
		t.Fatal(err)
	}
	if got := codexUnifiedCount(t, repo); got != 1 {
		t.Fatalf("post-purge boot must not re-import: got %d", got)
	}
}

// T4: marker younger than 30 days - nothing happens even if the archive grew.
func TestAutoImport_HoldsBefore30Days(t *testing.T) {
	repo := newAutoTestDB(t)
	ctx := context.Background()
	seedArchive(t, repo, "auto-hold-1", time.Now().Add(-48*time.Hour))
	if err := autoImportCodex(ctx, repo.DB(), repo, 90); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.DB().Exec("UPDATE codex_auto_import SET imported_at = ?", time.Now().Add(-29*24*time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	seedArchive(t, repo, "auto-hold-2", time.Now())
	if err := autoImportCodex(ctx, repo.DB(), repo, 90); err != nil {
		t.Fatal(err)
	}
	if got := codexUnifiedCount(t, repo); got != 1 {
		t.Fatalf("day-29 boot must hold: got %d rows", got)
	}
}
