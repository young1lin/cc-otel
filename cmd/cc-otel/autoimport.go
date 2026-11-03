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

// codexAutoPlan mirrors dbmerge's codexToAllPlans (source archive table →
// unified target table). Kept local so the id-range bookkeeping cannot drift
// silently from the engine's plan list.
var codexAutoPlan = []struct{ src, dst string }{
	{"codex_api_requests", "api_requests"},
	{"codex_user_prompt_events", "user_prompt_events"},
	{"codex_tool_decision_events", "tool_decision_events"},
	{"codex_tool_result_events", "tool_result_events"},
	{"codex_raw_otlp_events", "raw_otlp_events"},
}

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
		if !marker.purged && time.Now().Unix() >= marker.importedAt+codexAutoPurgeDays*86400 {
			return autoPurgeImported(ctx, database, repo, marker)
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
	// Already migrated deliberately (the one-click button ran before this
	// daemon upgrade)? Record the marker WITHOUT re-running the engine: a
	// re-run on a retention-pruned install would resurrect ~65k pruned
	// sub-event rows that the next retention sweep deletes again (R5
	// rollback finding). The api-request identity check is exact; when it
	// reports zero new rows the archive is fully represented in the pool.
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

	// Snapshot per-target max ids, import, snapshot again: with the servers
	// not yet bound the daemon is the only writer, so (pre, post] is exactly
	// what this import added.
	pre := map[string]int64{}
	for _, plan := range codexAutoPlan {
		pre[plan.dst], err = maxRowID(ctx, database, plan.dst)
		if err != nil {
			return fmt.Errorf("snapshot max id %s: %w", plan.dst, err)
		}
	}

	res, err := dbmerge.MigrateCodexToAll(ctx, database, dbmerge.Options{
		Location:      time.Local,
		RetentionDays: retentionDays,
	})
	if err != nil {
		return fmt.Errorf("archive import failed after %d row(s) (will retry next boot): %w", res.InsertedRows, err)
	}

	var ranges []autoImportRange
	for _, plan := range codexAutoPlan {
		post, err := maxRowID(ctx, database, plan.dst)
		if err != nil {
			return fmt.Errorf("snapshot max id %s: %w", plan.dst, err)
		}
		if post > pre[plan.dst] {
			ranges = append(ranges, autoImportRange{table: plan.dst, minID: pre[plan.dst] + 1, maxID: post})
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
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var deleted int64
	for _, r := range ranges {
		res, err := tx.ExecContext(ctx, fmt.Sprintf("DELETE FROM %s WHERE id BETWEEN ? AND ?", r.table), r.minID, r.maxID)
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

func recordAutoImport(ctx context.Context, database *sql.DB, importedAt int64, inserted int64, ranges []autoImportRange) error {
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
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
		err := database.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+plan.src).Scan(&n)
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
