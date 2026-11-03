package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/young1lin/cc-otel/internal/dbmerge"
)

// Startup auto-import of the legacy codex_* archive into the unified pool,
// plus the 30-day auto-purge of what the auto-import brought in.
//
// Lifecycle (one-shot, per database):
//
//   - First boot with a non-empty archive: the archive is imported through
//     the standard migration engine (occurrence-aware, ledger-deduped,
//     idempotent), and the exact row-id ranges the import created in every
//     unified table are recorded in codex_auto_import_ranges. The import
//     runs BEFORE the OTLP/web servers bind, so the daemon is the single
//     writer and the ranges are exact.
//   - Every later boot: skipped while the marker is younger than 30 days.
//   - 30 days after the import: the recorded ranges are deleted from the
//     unified tables, daily_model_agg is rebuilt, and the marker flips to
//     purged. The frozen codex_* archive itself is never touched — the
//     one-click migration button can still re-import deliberately (its
//     ledger identities survive, so a manual re-run stays idempotent).
//
// Failure posture: any error logs and lets startup continue; no marker is
// written on failure, so the next boot retries the import.
const codexAutoPurgeDays = 30

// codexAutoPlan derives the (source archive table → unified target table)
// mapping from dbmerge's single exported list. A local copy here would
// silently drift when the engine adds a mapping: rows imported but never
// purged, or ranges never snapshotted (R3 review A1-F1).
var codexAutoPlan = dbmerge.CodexToAllPlans()

type autoImportMarker struct {
	id          int64
	importedAt  int64
	purged      bool
	insertedRow int64
}

type autoImportRange struct {
	table string
	minID int64
	maxID int64
}

type aggRebuilder interface {
	RebuildDailyAggregates(context.Context) error
}

// runCodexMigrate is the engine entry used by autoImportCodex. A package
// variable so the crash-resume tests can simulate an engine that commits
// batches and then dies — the state machine must recover its purge ranges.
var runCodexMigrate = func(ctx context.Context, database *sql.DB, retentionDays int) (dbmerge.Result, error) {
	lastLog := time.Now()
	return dbmerge.MigrateCodexToAll(ctx, database, dbmerge.Options{
		Location:      time.Local,
		RetentionDays: retentionDays,
		// Boot auto-import runs BEFORE the listeners bind: on a large archive
		// the import can take 15s+ with zero log output, which looks exactly
		// like a hung daemon to an operator (R4 review A4-F2). Emit a line at
		// most every 2s so progress is visible while staying quiet.
		Progress: func(p dbmerge.Progress) {
			if time.Since(lastLog) < 2*time.Second {
				return
			}
			lastLog = time.Now()
			log.Printf("codex auto-import: progress table=%s processed=%d/%d inserted=%d duplicates=%d (%.0f%%)", p.CurrentTable, p.ProcessedRows, p.TotalRows, p.InsertedRows, p.DuplicateRows, p.Percent)
		},
	})
}

// autoImportCodex runs the auto-import/purge state machine for one boot.
func autoImportCodex(ctx context.Context, database *sql.DB, repo aggRebuilder, retentionDays int) error {
	if err := ensureAutoImportTables(ctx, database); err != nil {
		return fmt.Errorf("ensure marker tables: %w", err)
	}

	marker, found, err := loadAutoImportMarker(ctx, database)
	if err != nil {
		return fmt.Errorf("load marker: %w", err)
	}
	if found {
		if !marker.purged && dayStart(time.Now()) >= dayStart(time.Unix(marker.importedAt, 0))+codexAutoPurgeDays*86400 { // local-midnight alignment, matching the migration sanity check (R1 A5-5)
			return autoPurgeImported(ctx, database, repo, marker)
		}
		// Rollback contract (R3 review A5-F3): if the daemon was downgraded to
		// a pre-merge binary, that binary wrote NEW rows into the codex_*
		// archive while the marker here says "already imported" — those rows
		// are invisible on the Codex tab until a manual one-click migration
		// runs. Surface the risk instead of skipping silently.
		var archiveNew int
		// Best-effort probe: on probe failure (e.g. table missing) we
		// deliberately stay silent — the warning is advisory, and failing the
		// boot path over it would be worse than missing one warning.
		_ = database.QueryRowContext(ctx, "SELECT COUNT(*) FROM codex_api_requests WHERE timestamp > ?", marker.importedAt).Scan(&archiveNew)
		if archiveNew > 0 {
			log.Printf("codex auto-import: WARNING: %d archive row(s) newer than the imported ranges exist (rollback window or manual archive edits?) — run the in-app Codex migration to pick them up", archiveNew)
		}
		return nil // imported recently (or already purged): nothing to do
	}

	total, err := countArchiveRows(ctx, database)
	if err != nil {
		return fmt.Errorf("count archive: %w", err)
	}
	if total == 0 {
		return nil // no legacy history: no marker, nothing ever to purge
	}

	// A previous boot started the engine but never finished (crash between
	// a committed batch and the marker write). Resume against the STORED pre
	// ids: the engine dedups what already landed, imports the remainder, and
	// the resulting (pre, post] ranges cover every row any attempt committed.
	attemptPre, hasAttempt, err := loadAttemptPre(ctx, database)
	if err != nil {
		return fmt.Errorf("load attempt: %w", err)
	}

	pre := map[string]int64{}
	if !hasAttempt {
		// Already migrated deliberately (the one-click button ran before this
		// daemon upgrade)? Record the marker WITHOUT re-running the engine: a
		// re-run on a retention-pruned install would resurrect ~65k pruned
		// sub-event rows that the next retention sweep deletes again (R5
		// rollback finding). Valid ONLY while no attempt ever started — after
		// a crashed import the fast path would strand committed rows outside
		// every range and they would never be purged.
		preview, err := dbmerge.InspectCodexToAll(ctx, database)
		if err != nil {
			return fmt.Errorf("archive precheck: %w", err)
		}
		apiNew := int64(0)
		for _, counts := range preview {
			if counts.SourceTable == "codex_api_requests" {
				apiNew = counts.NewRows
			}
		}
		if apiNew == 0 {
			if err := recordAutoImport(ctx, database, time.Now().Unix(), 0, nil); err != nil {
				return fmt.Errorf("record marker: %w", err)
			}
			log.Printf("codex auto-import: archive already migrated (0 new api rows); marker recorded without re-import")
			return nil
		}

		// Snapshot per-target max ids BEFORE the engine runs and persist them
		// immediately: with the servers not yet bound the daemon is the only
		// writer, so (pre, post] is exactly what this import added.
		for _, plan := range codexAutoPlan {
			pre[plan.Target], err = maxRowID(ctx, database, plan.Target)
			if err != nil {
				return fmt.Errorf("snapshot max id %s: %w", plan.Target, err)
			}
		}
		if err := recordAttemptPre(ctx, database, pre); err != nil {
			return fmt.Errorf("persist attempt snapshot: %w", err)
		}
	} else {
		pre = attemptPre
		log.Printf("codex auto-import: resuming interrupted attempt (pre-snapshot from previous boot)")
	}

	res, err := runCodexMigrate(ctx, database, retentionDays)
	if err != nil {
		// Attempt rows stay on disk: the next boot resumes with the original
		// pre snapshot, so rows committed by this failed run remain inside the
		// eventual (pre, post] purge ranges.
		return fmt.Errorf("archive import failed after %d row(s) (will resume next boot): %w", res.InsertedRows, err)
	}

	var ranges []autoImportRange
	for _, plan := range codexAutoPlan {
		post, err := maxRowID(ctx, database, plan.Target)
		if err != nil {
			return fmt.Errorf("snapshot max id %s: %w", plan.Target, err)
		}
		if post > pre[plan.Target] {
			ranges = append(ranges, autoImportRange{table: plan.Target, minID: pre[plan.Target] + 1, maxID: post})
		}
	}
	if err := recordAutoImport(ctx, database, time.Now().Unix(), res.InsertedRows, ranges); err != nil {
		return fmt.Errorf("record marker: %w", err)
	}
	if res.InsertedRows > 0 {
		log.Printf("codex auto-import: imported %d archive row(s) into the unified pool; auto-purge scheduled in %d day(s)", res.InsertedRows, codexAutoPurgeDays)
	} else {
		log.Printf("codex auto-import: archive already migrated (%d duplicate row(s) skipped); auto-purge marker recorded", res.DuplicateRows)
	}
	return nil
}

// autoPurgeImported deletes the recorded ranges, rebuilds the aggregate, then
// flips the marker. Crash-safe: ranges already deleted simply no-op on a
// retry, and the marker only flips after a successful rebuild.
func autoPurgeImported(ctx context.Context, database *sql.DB, repo aggRebuilder, marker autoImportMarker) error {
	ranges, err := loadAutoImportRanges(ctx, database, marker.id)
	if err != nil {
		return fmt.Errorf("load ranges: %w", err)
	}
	// Defense in depth: table names read back from the ranges table are
	// spliced into DELETE statements — only the closed plan set may pass
	// (R2 review A1-F2). And a wall-clock jump FORWARD past the 30-day
	// boundary (manual clock set, RTC fault, VM restore) must not fire the
	// purge early: importedAt in the future means the clock moved, not that
	// a month passed (R2 review A5-F3).
	allowed := map[string]bool{}
	for _, plan := range codexAutoPlan {
		allowed[plan.Target] = true
	}
	if marker.importedAt > time.Now().Unix() {
		return fmt.Errorf("marker imported_at %d is in the future (clock jumped?); purge deferred", marker.importedAt)
	}
	for _, r := range ranges {
		if !allowed[r.table] {
			return fmt.Errorf("purge range references unexpected table %q; refusing", r.table)
		}
	}
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var deleted int64
	for _, r := range ranges {
		// The purge must NEVER take live rows. Ranges are recorded under a
		// single-writer assumption (import runs before listeners bind); if that
		// invariant is ever broken — two daemon instances sharing a DB, or the
		// import moved after bind — live traffic rows can land inside a
		// (min,max] range. The service guard makes that scenario fail safe:
		// only rows tagged codex are deletable, so Claude/live rows in the
		// range survive even when the bookkeeping is wrong (R3 review A5-F1).
		query := fmt.Sprintf("DELETE FROM %s WHERE id BETWEEN ? AND ?", r.table)
		switch r.table {
		case "api_requests":
			query += " AND service_name = 'codex'"
		}
		res, err := tx.ExecContext(ctx, query, r.minID, r.maxID)
		if err != nil {
			return fmt.Errorf("purge %s [%d,%d]: %w", r.table, r.minID, r.maxID, err)
		}
		n, _ := res.RowsAffected()
		deleted += n
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	// Rebuild AFTER the deletes commit and BEFORE the marker flips, so a
	// crash in between retries the whole purge idempotently.
	if err := repo.RebuildDailyAggregates(ctx); err != nil {
		return fmt.Errorf("agg rebuild after purge (deleted rows stay deleted; retry next boot): %w", err)
	}
	if _, err := database.ExecContext(ctx, "UPDATE codex_auto_import SET purged = 1 WHERE id = ?", marker.id); err != nil {
		return err
	}
	log.Printf("codex auto-import: purged %d auto-imported row(s) %d day(s) after import (archive untouched)", deleted, codexAutoPurgeDays)
	return nil
}

func ensureAutoImportTables(ctx context.Context, database *sql.DB) error {
	_, err := database.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS codex_auto_import (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			imported_at INTEGER NOT NULL,
			purged INTEGER NOT NULL DEFAULT 0,
			inserted_rows INTEGER NOT NULL DEFAULT 0
		);
		CREATE TABLE IF NOT EXISTS codex_auto_import_ranges (
			import_id INTEGER NOT NULL,
			table_name TEXT NOT NULL,
			min_id INTEGER NOT NULL,
			max_id INTEGER NOT NULL
		);
	`)
	// One boot's persistent pre-snapshot. Recorded BEFORE the engine runs;
	// any crash between the first committed batch and the final marker write
	// leaves these rows behind, and the next boot resumes against the ORIGINAL
	// pre ids instead of a fresh snapshot — so (pre, post] covers every row
	// the interrupted import(s) committed, across any number of retries.
	// Cleared in the same transaction that writes the final marker.
	_, err = database.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS codex_auto_import_attempt (
			table_name TEXT PRIMARY KEY,
			pre_max_id INTEGER NOT NULL DEFAULT 0
		);
	`)
	return err
}

func loadAutoImportMarker(ctx context.Context, database *sql.DB) (autoImportMarker, bool, error) {
	var m autoImportMarker
	var purged int
	err := database.QueryRowContext(ctx,
		"SELECT id, imported_at, purged, inserted_rows FROM codex_auto_import ORDER BY id DESC LIMIT 1",
	).Scan(&m.id, &m.importedAt, &purged, &m.insertedRow)
	if err == sql.ErrNoRows {
		return m, false, nil
	}
	if err != nil {
		return m, false, err
	}
	m.purged = purged == 1
	return m, true, nil
}

func loadAutoImportRanges(ctx context.Context, database *sql.DB, importID int64) ([]autoImportRange, error) {
	rows, err := database.QueryContext(ctx,
		"SELECT table_name, min_id, max_id FROM codex_auto_import_ranges WHERE import_id = ?", importID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []autoImportRange
	for rows.Next() {
		var r autoImportRange
		if err := rows.Scan(&r.table, &r.minID, &r.maxID); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func loadAttemptPre(ctx context.Context, database *sql.DB) (map[string]int64, bool, error) {
	rows, err := database.QueryContext(ctx, "SELECT table_name, pre_max_id FROM codex_auto_import_attempt")
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var name string
		var id int64
		if err := rows.Scan(&name, &id); err != nil {
			return nil, false, err
		}
		out[name] = id
	}
	return out, len(out) > 0, rows.Err()
}

func recordAttemptPre(ctx context.Context, database *sql.DB, pre map[string]int64) error {
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM codex_auto_import_attempt"); err != nil {
		return err
	}
	for _, plan := range codexAutoPlan {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO codex_auto_import_attempt (table_name, pre_max_id) VALUES (?, ?)",
			plan.Target, pre[plan.Target]); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func recordAutoImport(ctx context.Context, database *sql.DB, importedAt int64, inserted int64, ranges []autoImportRange) error {
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// The attempt rows and the marker must flip in ONE transaction: a marker
	// without a cleared attempt would make the next boot re-run the engine
	// against a finished import, and an attempt surviving its marker would
	// shadow the fast path forever.
	if _, err := tx.ExecContext(ctx, "DELETE FROM codex_auto_import_attempt"); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx,
		"INSERT INTO codex_auto_import (imported_at, purged, inserted_rows) VALUES (?, 0, ?)",
		importedAt, inserted)
	if err != nil {
		return err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	for _, r := range ranges {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO codex_auto_import_ranges (import_id, table_name, min_id, max_id) VALUES (?, ?, ?, ?)",
			id, r.table, r.minID, r.maxID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func countArchiveRows(ctx context.Context, database *sql.DB) (int64, error) {
	var total int64
	for _, plan := range codexAutoPlan {
		var n int64
		err := database.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+plan.Source).Scan(&n)
		if err != nil {
			// A missing source table means no history for that stream.
			if isNoTableErr(err) {
				continue
			}
			return 0, err
		}
		total += n
	}
	return total, nil
}

func maxRowID(ctx context.Context, database *sql.DB, table string) (int64, error) {
	var max int64
	err := database.QueryRowContext(ctx, "SELECT COALESCE(MAX(id), 0) FROM "+table).Scan(&max)
	return max, err
}

// isNoTableErr reports whether err is SQLite's "no such table".
func isNoTableErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "no such table")
}

// dayStart truncates t to its local calendar midnight (the same day
// alignment the migration sanity check uses).
func dayStart(t time.Time) int64 {
	y, m, d := t.Local().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location()).Unix()
}
