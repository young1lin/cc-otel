// prune_before deletes all rows from cc-otel.db whose timestamp is strictly before
// the given local-date cutoff. Intended as a one-shot cleanup for fake/seed data.
//
// Usage:
//
//	go run ./tools/prune_before -db ./bin/cc-otel.db -cutoff 2026-04-08            # dry-run
//	go run ./tools/prune_before -db ./bin/cc-otel.db -cutoff 2026-04-08 -apply     # actually delete
//
// The cutoff is interpreted as 00:00:00 in the machine's local time zone.
// A row is deleted iff its timestamp < cutoff (i.e. cutoff day is preserved).
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
)

// Tables keyed by Unix-seconds "timestamp" column.
var tsTables = []string{
	"api_requests",
	"user_prompt_events",
	"tool_decision_events",
	"tool_result_events",
	"api_error_events",
	"otel_metric_points",
	"events",
	"raw_otlp_events",
}

func main() {
	var dbPath, cutoffStr string
	var apply, doVacuum bool

	flag.StringVar(&dbPath, "db", "", "path to cc-otel.db")
	flag.StringVar(&cutoffStr, "cutoff", "", "local date YYYY-MM-DD; rows with timestamp < cutoff 00:00 local are deleted")
	flag.BoolVar(&apply, "apply", false, "actually delete (default is dry-run)")
	flag.BoolVar(&doVacuum, "vacuum", true, "VACUUM after a successful apply")
	flag.Parse()

	if dbPath == "" || cutoffStr == "" {
		fmt.Fprintln(os.Stderr, "usage: prune_before -db <path> -cutoff YYYY-MM-DD [-apply] [-vacuum=true]")
		os.Exit(2)
	}

	cutoffT, err := time.ParseInLocation("2006-01-02", cutoffStr, time.Local)
	if err != nil {
		fmt.Fprintf(os.Stderr, "parse cutoff %q: %v\n", cutoffStr, err)
		os.Exit(2)
	}
	cutoffUnix := cutoffT.Unix()
	cutoffDate := cutoffT.Format("2006-01-02")

	fmt.Printf("DB:         %s\n", dbPath)
	fmt.Printf("Cutoff:     %s 00:00:00 %s  (unix=%d)\n", cutoffDate, cutoffT.Location(), cutoffUnix)
	fmt.Printf("Mode:       %s\n", modeLabel(apply))
	fmt.Println()

	ctx := context.Background()
	db, err := sql.Open("sqlite3", fmt.Sprintf("file:%s?_busy_timeout=5000", filepath.ToSlash(filepath.Clean(dbPath))))
	if err != nil {
		panic(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	if err := plan(ctx, db, cutoffUnix, cutoffDate); err != nil {
		fmt.Fprintln(os.Stderr, "plan:", err)
		os.Exit(1)
	}

	if !apply {
		fmt.Println("\nDry-run. Re-run with -apply to actually delete.")
		return
	}

	deleted, err := execute(ctx, db, cutoffUnix, cutoffDate)
	if err != nil {
		fmt.Fprintln(os.Stderr, "apply:", err)
		os.Exit(1)
	}
	fmt.Printf("\nTotal rows deleted: %d\n", deleted)

	if doVacuum {
		fmt.Println("Running VACUUM...")
		if _, err := db.ExecContext(ctx, "VACUUM"); err != nil {
			fmt.Fprintln(os.Stderr, "vacuum:", err)
			os.Exit(1)
		}
		fmt.Println("VACUUM done.")
	}

	if err := verify(ctx, db, cutoffUnix, cutoffDate); err != nil {
		fmt.Fprintln(os.Stderr, "verify:", err)
		os.Exit(1)
	}
}

func modeLabel(apply bool) string {
	if apply {
		return "APPLY (rows will be deleted)"
	}
	return "DRY-RUN (no changes)"
}

// plan prints, per table, how many rows match the delete predicate vs total.
func plan(ctx context.Context, db *sql.DB, cutoffUnix int64, cutoffDate string) error {
	fmt.Printf("%-22s %12s %12s\n", "table", "to_delete", "total")
	fmt.Printf("%-22s %12s %12s\n", "----------------------", "------------", "------------")

	for _, t := range tsTables {
		toDel, total, err := countTs(ctx, db, t, cutoffUnix)
		if err != nil {
			return fmt.Errorf("count %s: %w", t, err)
		}
		fmt.Printf("%-22s %12d %12d\n", t, toDel, total)
	}

	toDel, total, err := countCol(ctx, db, "pending_ttft_spans", "created_unix < ?", cutoffUnix)
	if err != nil {
		return fmt.Errorf("count pending_ttft_spans: %w", err)
	}
	fmt.Printf("%-22s %12d %12d\n", "pending_ttft_spans", toDel, total)

	toDel, total, err = countCol(ctx, db, "daily_model_agg", "date < ?", cutoffDate)
	if err != nil {
		return fmt.Errorf("count daily_model_agg: %w", err)
	}
	fmt.Printf("%-22s %12d %12d\n", "daily_model_agg", toDel, total)

	return nil
}

func countTs(ctx context.Context, db *sql.DB, table string, cutoff int64) (int64, int64, error) {
	return countCol(ctx, db, table, "timestamp < ?", cutoff)
}

func countCol(ctx context.Context, db *sql.DB, table, where string, arg any) (int64, int64, error) {
	var toDel, total int64
	if err := db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE %s`, table, where), arg).Scan(&toDel); err != nil {
		return 0, 0, err
	}
	if err := db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT COUNT(*) FROM %s`, table)).Scan(&total); err != nil {
		return 0, 0, err
	}
	return toDel, total, nil
}

// execute runs the deletes inside a single transaction and prints per-table counts.
func execute(ctx context.Context, db *sql.DB, cutoffUnix int64, cutoffDate string) (int64, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	var total int64

	for _, t := range tsTables {
		n, err := delExec(ctx, tx, fmt.Sprintf(`DELETE FROM %s WHERE timestamp < ?`, t), cutoffUnix)
		if err != nil {
			return total, fmt.Errorf("delete %s: %w", t, err)
		}
		fmt.Printf("  deleted %-22s %d\n", t, n)
		total += n
	}

	n, err := delExec(ctx, tx, `DELETE FROM pending_ttft_spans WHERE created_unix < ?`, cutoffUnix)
	if err != nil {
		return total, fmt.Errorf("delete pending_ttft_spans: %w", err)
	}
	fmt.Printf("  deleted %-22s %d\n", "pending_ttft_spans", n)
	total += n

	n, err = delExec(ctx, tx, `DELETE FROM daily_model_agg WHERE date < ?`, cutoffDate)
	if err != nil {
		return total, fmt.Errorf("delete daily_model_agg: %w", err)
	}
	fmt.Printf("  deleted %-22s %d\n", "daily_model_agg", n)
	total += n

	if err := tx.Commit(); err != nil {
		return total, fmt.Errorf("commit: %w", err)
	}
	return total, nil
}

func delExec(ctx context.Context, tx *sql.Tx, query string, arg any) (int64, error) {
	res, err := tx.ExecContext(ctx, query, arg)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// verify confirms no stale rows remain after apply.
func verify(ctx context.Context, db *sql.DB, cutoffUnix int64, cutoffDate string) error {
	fmt.Println("\nVerify (should all be 0):")
	for _, t := range tsTables {
		var n int64
		if err := db.QueryRowContext(ctx,
			fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE timestamp < ?`, t), cutoffUnix).Scan(&n); err != nil {
			return err
		}
		fmt.Printf("  %-22s %d\n", t, n)
	}
	var n int64
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pending_ttft_spans WHERE created_unix < ?`, cutoffUnix).Scan(&n); err != nil {
		return err
	}
	fmt.Printf("  %-22s %d\n", "pending_ttft_spans", n)
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM daily_model_agg WHERE date < ?`, cutoffDate).Scan(&n); err != nil {
		return err
	}
	fmt.Printf("  %-22s %d\n", "daily_model_agg", n)
	return nil
}
