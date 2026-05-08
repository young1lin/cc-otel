package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
)

func main() {
	dbPath := flag.String("db", filepath.Join("bin", "cc-otel.db"), "SQLite database path")
	dryRun := flag.Bool("dry-run", false, "run checks and SQL inside a rollback-only transaction")
	backup := flag.Bool("backup", true, "create a SQLite VACUUM INTO backup before mutating")
	flag.Parse()

	ctx := context.Background()
	database, err := sql.Open("sqlite3", *dbPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer database.Close()

	if _, err := database.ExecContext(ctx, `PRAGMA busy_timeout=5000`); err != nil {
		log.Fatalf("set busy_timeout: %v", err)
	}
	if _, err := database.ExecContext(ctx, `PRAGMA wal_checkpoint(FULL)`); err != nil {
		log.Printf("warning: wal checkpoint failed: %v", err)
	}

	if !*dryRun && *backup {
		backupPath := fmt.Sprintf("%s.codex-migration-%s.bak", *dbPath, time.Now().Format("20060102-150405"))
		if _, err := database.ExecContext(ctx, "VACUUM INTO "+sqlString(backupPath)); err != nil {
			log.Fatalf("backup %q: %v", backupPath, err)
		}
		log.Printf("backup created: %s", backupPath)
	}

	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		log.Fatalf("begin tx: %v", err)
	}
	defer tx.Rollback()

	stats, err := migrate(ctx, tx)
	if err != nil {
		log.Fatalf("migrate: %v", err)
	}

	if *dryRun {
		log.Printf("dry-run complete; rolling back")
		printStats(stats)
		return
	}
	if err := tx.Commit(); err != nil {
		log.Fatalf("commit: %v", err)
	}
	printStats(stats)
}

type migrationStats struct {
	APIHadToolColumn bool
	AggHadToolColumn bool
	APIBackfilled    int64
	AggBackfilled    int64
	DurationFilled   int64
	AggRows          int64
}

func migrate(ctx context.Context, tx *sql.Tx) (*migrationStats, error) {
	apiExists, err := tableExists(ctx, tx, "codex_api_requests")
	if err != nil {
		return nil, err
	}
	aggExists, err := tableExists(ctx, tx, "codex_daily_model_agg")
	if err != nil {
		return nil, err
	}
	if !apiExists || !aggExists {
		return nil, fmt.Errorf("missing codex tables: codex_api_requests=%v codex_daily_model_agg=%v", apiExists, aggExists)
	}

	stats := &migrationStats{}

	stats.APIHadToolColumn, err = columnExists(ctx, tx, "codex_api_requests", "tool_tokens")
	if err != nil {
		return nil, err
	}
	stats.AggHadToolColumn, err = columnExists(ctx, tx, "codex_daily_model_agg", "tool_tokens")
	if err != nil {
		return nil, err
	}

	if err := ensureColumn(ctx, tx, "codex_api_requests", "total_tokens", "INTEGER DEFAULT 0"); err != nil {
		return nil, err
	}
	if err := ensureColumn(ctx, tx, "codex_daily_model_agg", "total_tokens", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return nil, err
	}

	if stats.APIHadToolColumn {
		n, err := execRows(ctx, tx, `
			UPDATE codex_api_requests
			SET total_tokens = tool_tokens
			WHERE total_tokens = 0 AND tool_tokens > 0`)
		if err != nil {
			return nil, fmt.Errorf("backfill codex_api_requests.total_tokens: %w", err)
		}
		stats.APIBackfilled = n
	}
	if stats.AggHadToolColumn {
		n, err := execRows(ctx, tx, `
			UPDATE codex_daily_model_agg
			SET total_tokens = tool_tokens
			WHERE total_tokens = 0 AND tool_tokens > 0`)
		if err != nil {
			return nil, fmt.Errorf("backfill codex_daily_model_agg.total_tokens: %w", err)
		}
		stats.AggBackfilled = n
	}

	n, err := execRows(ctx, tx, `
		UPDATE codex_api_requests
		SET duration_ms = (
			SELECT (MAX(e.timestamp) - MIN(e.timestamp)) * 1000
			FROM codex_events e
			WHERE e.session_id = codex_api_requests.session_id
			  AND e.model = codex_api_requests.model
			  AND e.event_name = 'codex.websocket_event'
			  AND e.timestamp >= codex_api_requests.timestamp - 300
			  AND e.timestamp <= codex_api_requests.timestamp + 300
		)
		WHERE duration_ms = 0
		  AND (input_tokens > 0 OR output_tokens > 0)
		  AND EXISTS (
			SELECT 1
			FROM codex_events e
			WHERE e.session_id = codex_api_requests.session_id
			  AND e.model = codex_api_requests.model
			  AND e.event_name = 'codex.websocket_event'
			  AND e.timestamp >= codex_api_requests.timestamp - 300
			  AND e.timestamp <= codex_api_requests.timestamp + 300
			GROUP BY e.session_id, e.model
			HAVING MAX(e.timestamp) > MIN(e.timestamp)
		  )`)
	if err != nil {
		return nil, fmt.Errorf("backfill codex duration_ms: %w", err)
	}
	stats.DurationFilled = n

	if _, err := tx.ExecContext(ctx, `DELETE FROM codex_daily_model_agg`); err != nil {
		return nil, fmt.Errorf("clear codex_daily_model_agg: %w", err)
	}

	n, err = execRows(ctx, tx, `
		INSERT INTO codex_daily_model_agg (
			date, model, input_tokens, output_tokens,
			cache_read_tokens, cache_creation_tokens,
			reasoning_tokens, total_tokens, cost_usd, request_count
		)
		SELECT
			date(timestamp, 'unixepoch', 'localtime'),
			model,
			COALESCE(SUM(input_tokens), 0),
			COALESCE(SUM(output_tokens), 0),
			COALESCE(SUM(cache_read_tokens), 0),
			COALESCE(SUM(cache_creation_tokens), 0),
			COALESCE(SUM(reasoning_tokens), 0),
			COALESCE(SUM(total_tokens), 0),
			COALESCE(SUM(cost_usd), 0),
			COUNT(*)
		FROM codex_api_requests
		GROUP BY date(timestamp, 'unixepoch', 'localtime'), model`)
	if err != nil {
		return nil, fmt.Errorf("rebuild codex_daily_model_agg: %w", err)
	}
	stats.AggRows = n

	return stats, nil
}

func printStats(stats *migrationStats) {
	log.Printf("codex_api_requests had tool_tokens: %v", stats.APIHadToolColumn)
	log.Printf("codex_daily_model_agg had tool_tokens: %v", stats.AggHadToolColumn)
	log.Printf("codex_api_requests total_tokens backfilled rows: %d", stats.APIBackfilled)
	log.Printf("codex_daily_model_agg total_tokens backfilled rows: %d", stats.AggBackfilled)
	log.Printf("codex_api_requests duration_ms backfilled rows: %d", stats.DurationFilled)
	log.Printf("codex_daily_model_agg rebuilt rows: %d", stats.AggRows)
}

func tableExists(ctx context.Context, tx *sql.Tx, table string) (bool, error) {
	var n int
	err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table,
	).Scan(&n)
	return n > 0, err
}

func ensureColumn(ctx context.Context, tx *sql.Tx, table, column, definition string) error {
	exists, err := columnExists(ctx, tx, table, column)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	_, err = tx.ExecContext(ctx, fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, definition))
	return err
}

func columnExists(ctx context.Context, tx *sql.Tx, table, column string) (bool, error) {
	rows, err := tx.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return false, err
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue interface{}
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

func execRows(ctx context.Context, tx *sql.Tx, query string, args ...any) (int64, error) {
	res, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, nil
	}
	return n, nil
}

func sqlString(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
