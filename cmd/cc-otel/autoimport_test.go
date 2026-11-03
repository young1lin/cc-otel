package main

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/young1lin/cc-otel/internal/config"
	"github.com/young1lin/cc-otel/internal/db"
	"github.com/young1lin/cc-otel/internal/dbmerge"
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

// T7 (R1 review P1): a crash between the first committed batch and the
// marker write must NOT strand imported rows outside the purge ranges. The
// persistent attempt pre-snapshot makes the next boot resume against the
// ORIGINAL pre ids, so (pre, post] covers both runs' rows.
func TestAutoImport_ResumesAfterCrashedEngine(t *testing.T) {
	repo := newAutoTestDB(t)
	ctx := context.Background()
	seedArchive(t, repo, "crash-conv", time.Now().Add(-48*time.Hour))

	orig := runCodexMigrate
	defer func() { runCodexMigrate = orig }()

	// First boot: the real engine fully commits, then the process dies
	// before the caller records the marker (the exact P1 crash window).
	runCodexMigrate = func(ctx context.Context, database *sql.DB, retentionDays int) (dbmerge.Result, error) {
		res, err := dbmerge.MigrateCodexToAll(ctx, database, dbmerge.Options{
			Location:      time.Local,
			RetentionDays: retentionDays,
		})
		if err != nil {
			return res, err
		}
		return res, fmt.Errorf("simulated crash after commit")
	}
	if err := autoImportCodex(ctx, repo.DB(), repo, 90); err == nil {
		t.Fatal("first boot must surface the engine failure")
	}
	if got := codexUnifiedCount(t, repo); got != 1 {
		t.Fatalf("crashed engine committed its row: got %d", got)
	}
	if _, found, err := loadAutoImportMarker(ctx, repo.DB()); err != nil || found {
		t.Fatalf("no marker after crash: found=%v err=%v", found, err)
	}
	if _, hasAttempt, err := loadAttemptPre(ctx, repo.DB()); err != nil || !hasAttempt {
		t.Fatalf("attempt pre-snapshot must survive the crash: has=%v err=%v", hasAttempt, err)
	}

	// Second boot: real engine (dedup skips the already-committed row);
	// the marker's ranges must still cover it via the ORIGINAL pre ids.
	runCodexMigrate = orig
	if err := autoImportCodex(ctx, repo.DB(), repo, 90); err != nil {
		t.Fatal(err)
	}
	m, found, err := loadAutoImportMarker(ctx, repo.DB())
	if err != nil || !found {
		t.Fatalf("marker expected after resume: found=%v err=%v", found, err)
	}
	if _, hasAttempt, err := loadAttemptPre(ctx, repo.DB()); err != nil || hasAttempt {
		t.Fatalf("attempt must be cleared with the marker: has=%v err=%v", hasAttempt, err)
	}
	ranges, err := loadAutoImportRanges(ctx, repo.DB(), m.id)
	if err != nil {
		t.Fatal(err)
	}
	var covered bool
	var rid int64
	if err := repo.DB().QueryRow("SELECT id FROM api_requests WHERE service_name='codex' AND session_id='crash-conv'").Scan(&rid); err != nil {
		t.Fatal(err)
	}
	for _, r := range ranges {
		if r.table == "api_requests" && rid >= r.minID && rid <= r.maxID {
			covered = true
		}
	}
	if !covered {
		t.Fatalf("row committed by the crashed engine (id=%d) must be inside the resume ranges: %+v", rid, ranges)
	}
	// And the purge built from those ranges removes it (archive untouched).
	if err := autoPurgeImported(ctx, repo.DB(), repo, m); err != nil {
		t.Fatal(err)
	}
	if got := codexUnifiedCount(t, repo); got != 0 {
		t.Fatalf("purge after resume must delete both runs' rows: got %d", got)
	}
}

// T8: the 0-new-rows fast path must NOT fire while an attempt is pending,
// even when the crashed run already imported every api row.
func TestAutoImport_FastPathSuppressedDuringAttempt(t *testing.T) {
	repo := newAutoTestDB(t)
	ctx := context.Background()
	seedArchive(t, repo, "fast-conv", time.Now().Add(-48*time.Hour))

	orig := runCodexMigrate
	defer func() { runCodexMigrate = orig }()

	// Boot 1: real engine imports everything, then crashes pre-marker.
	runCodexMigrate = func(ctx context.Context, database *sql.DB, retentionDays int) (dbmerge.Result, error) {
		res, err := dbmerge.MigrateCodexToAll(ctx, database, dbmerge.Options{
			Location:      time.Local,
			RetentionDays: retentionDays,
		})
		if err != nil {
			return res, err
		}
		return res, fmt.Errorf("crash")
	}
	if err := autoImportCodex(ctx, repo.DB(), repo, 90); err == nil {
		t.Fatal("boot 1 fails")
	}

	// Boot 2: real engine restored. With 0 new api rows the fast path WOULD record a
	// 0-range marker if it fired; instead the resume path must call the
	// engine and produce ranges covering the crashed run's row.
	runCodexMigrate = orig
	if err := autoImportCodex(ctx, repo.DB(), repo, 90); err != nil {
		t.Fatal(err)
	}
	m, found, err := loadAutoImportMarker(ctx, repo.DB())
	if err != nil || !found {
		t.Fatalf("marker expected: found=%v err=%v", found, err)
	}
	ranges, _ := loadAutoImportRanges(ctx, repo.DB(), m.id)
	var rid int64
	if err := repo.DB().QueryRow("SELECT id FROM api_requests WHERE service_name='codex' AND session_id='fast-conv'").Scan(&rid); err != nil {
		t.Fatal(err)
	}
	covered := false
	for _, r := range ranges {
		if r.table == "api_requests" && rid >= r.minID && rid <= r.maxID {
			covered = true
		}
	}
	if !covered {
		t.Fatalf("fast path must not strand the crashed run's row (id=%d): ranges=%+v", rid, ranges)
	}
}
