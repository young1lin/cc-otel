// Package recompute reprices non-Claude rows in the request tables using the
// pricing registry, then rebuilds the daily aggregates. Shared by the
// recompute_cost CLI tool and the POST /api/pricing/recompute endpoint.
//
// Claude (claude-* prefix, case-insensitive) rows are NEVER touched —
// Anthropic owns the canonical price table for those.
package recompute

import (
	"context"
	"database/sql"
	"fmt"
	"math"

	"github.com/young1lin/cc-otel/internal/pricing"
)

// CostScale matches internal/db/repository.go: cost is stored as int64
// 0.00001-USD units (1 USD = 100000).
const CostScale = 100000.0

// Options selects which tables and timestamp range to recompute.
type Options struct {
	Tables []string // subset of {"api_requests","codex_api_requests"}; empty = both
	From   int64    // unix seconds, inclusive; 0 = no lower bound
	To     int64    // unix seconds, exclusive; 0 = no upper bound
}

// Progress is reported during Run (Total fixed up-front via COUNT(*);
// Scanned/Changed grow as the scan proceeds). May be nil.
type Progress struct {
	Table   string
	Total   int
	Scanned int
	Changed int
}

// Result is the aggregate outcome across all selected tables.
type Result struct {
	Scanned       int            `json:"scanned"`
	SkippedClaude int            `json:"skipped_claude"`
	Missing       int            `json:"missing"` // non-Claude rows the registry couldn't price
	MissByModel   map[string]int `json:"miss_by_model"`
	// Detected counts rows whose recomputed cost differs from stored —
	// detected during scan, independent of apply. Dry-run reports this.
	Detected int `json:"detected"`
	// Updated counts rows actually written (apply=true only).
	Updated int `json:"updated"`
}

type rowChange struct {
	id        int64
	model     string
	oldUnits  int64
	newUnits  int64
	deltaUSD  float64
	tsUnix    int64
	hadTokens bool
}

type tableSummary struct {
	scanned     int
	skippedCl   int
	missing     int
	missByModel map[string]int
	changes     []rowChange
}

// Run reprices cost_usd for non-Claude rows and rebuilds daily_model_agg /
// codex_daily_model_agg. With apply=false it is a dry run (no writes).
// progress is invoked periodically; pass nil to ignore.
func Run(ctx context.Context, db *sql.DB, reg pricing.Registry, opts Options, apply bool, progress func(Progress)) (Result, error) {
	out := Result{MissByModel: map[string]int{}}
	tables := opts.Tables
	if len(tables) == 0 {
		tables = []string{"api_requests", "codex_api_requests"}
	}
	for _, t := range tables {
		total, err := countRows(ctx, db, t, opts.From, opts.To)
		if err != nil {
			return out, fmt.Errorf("%s count: %w", t, err)
		}
		summary, err := scanTable(ctx, db, reg, t, opts.From, opts.To, total, progress)
		if err != nil {
			return out, fmt.Errorf("%s scan: %w", t, err)
		}
		out.Scanned += summary.scanned
		out.SkippedClaude += summary.skippedCl
		out.Missing += summary.missing
		for k, v := range summary.missByModel {
			out.MissByModel[k] += v
		}
		out.Detected += len(summary.changes)

		if !apply || len(summary.changes) == 0 {
			continue
		}
		if err := applyChanges(ctx, db, t, summary.changes); err != nil {
			return out, fmt.Errorf("%s apply: %w", t, err)
		}
		if err := rebuildAgg(ctx, db, t); err != nil {
			return out, fmt.Errorf("%s rebuild agg: %w", t, err)
		}
		out.Updated += len(summary.changes)
	}
	return out, nil
}

func countRows(ctx context.Context, db *sql.DB, table string, fromUnix, toUnix int64) (int, error) {
	if toUnix == 0 {
		toUnix = math.MaxInt64
	}
	var n int
	err := db.QueryRowContext(ctx, fmt.Sprintf(
		`SELECT COUNT(*) FROM %s WHERE timestamp >= ? AND timestamp < ?`, table),
		fromUnix, toUnix,
	).Scan(&n)
	return n, err
}

// scanTable walks the table applying the same single rule the receiver does:
// skip Claude, recompute everything else. Rows with zero tokens are skipped.
// Only rows whose recomputed cost differs end up in summary.changes. progress
// is called every 500 rows.
func scanTable(ctx context.Context, db *sql.DB, reg pricing.Registry, table string, fromUnix, toUnix int64, total int, progress func(Progress)) (*tableSummary, error) {
	if toUnix == 0 {
		toUnix = math.MaxInt64
	}
	summary := &tableSummary{missByModel: map[string]int{}}

	rows, err := db.QueryContext(ctx, fmt.Sprintf(`
		SELECT id, timestamp, model,
		       COALESCE(input_tokens, 0), COALESCE(output_tokens, 0),
		       COALESCE(cache_read_tokens, 0), COALESCE(cache_creation_tokens, 0),
		       COALESCE(cost_usd, 0)
		  FROM %s
		 WHERE timestamp >= ? AND timestamp < ?`, table),
		fromUnix, toUnix,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	emit := func() {
		if progress != nil {
			progress(Progress{Table: table, Total: total, Scanned: summary.scanned, Changed: len(summary.changes)})
		}
	}

	for rows.Next() {
		var (
			id                                    int64
			ts                                    int64
			model                                 string
			input, output, cacheRead, cacheCreate int64
			oldUnits                              int64
		)
		if err := rows.Scan(&id, &ts, &model, &input, &output, &cacheRead, &cacheCreate, &oldUnits); err != nil {
			return nil, err
		}
		summary.scanned++

		if pricing.IsClaudeModel(model) {
			summary.skippedCl++
			if summary.scanned%500 == 0 {
				emit()
			}
			continue
		}
		if input == 0 && output == 0 && cacheRead == 0 && cacheCreate == 0 {
			if summary.scanned%500 == 0 {
				emit()
			}
			continue
		}

		// Codex stores input_tokens inclusive of cached tokens; Calc wants uncached.
		uncachedInput := input
		if table == "codex_api_requests" {
			uncachedInput = input - cacheRead
			if uncachedInput < 0 {
				uncachedInput = 0
			}
		}
		newUSD := reg.Calc(ctx, model, uncachedInput, output, cacheRead, cacheCreate)
		if newUSD <= 0 {
			summary.missing++
			summary.missByModel[model]++
			if summary.scanned%500 == 0 {
				emit()
			}
			continue
		}
		newUnits := int64(math.Round(newUSD * CostScale))
		if newUnits == oldUnits {
			if summary.scanned%500 == 0 {
				emit()
			}
			continue
		}
		summary.changes = append(summary.changes, rowChange{
			id:        id,
			model:     model,
			oldUnits:  oldUnits,
			newUnits:  newUnits,
			deltaUSD:  float64(newUnits-oldUnits) / CostScale,
			tsUnix:    ts,
			hadTokens: true,
		})
		if summary.scanned%500 == 0 {
			emit()
		}
	}
	emit() // final tick
	return summary, rows.Err()
}

// applyChanges UPDATEs cost_usd one row at a time within a single transaction.
func applyChanges(ctx context.Context, db *sql.DB, table string, changes []rowChange) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, fmt.Sprintf(`UPDATE %s SET cost_usd = ? WHERE id = ?`, table))
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, c := range changes {
		if _, err := stmt.ExecContext(ctx, c.newUnits, c.id); err != nil {
			return fmt.Errorf("update id=%d: %w", c.id, err)
		}
	}
	return tx.Commit()
}

// rebuildAgg recomputes the daily model aggregate via full rebuild.
// The DELETE + INSERT run in one transaction so a crash or ctx cancel between
// them cannot leave the aggregate empty (which would zero the dashboard).
// Mirrors Repository.RebuildDailyAggregates (internal/db/repository.go).
func rebuildAgg(ctx context.Context, db *sql.DB, parent string) error {
	aggTable := ""
	switch parent {
	case "api_requests":
		aggTable = "daily_model_agg"
	case "codex_api_requests":
		aggTable = "codex_daily_model_agg"
	default:
		return fmt.Errorf("unknown parent table %q", parent)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin rebuild %s tx: %w", aggTable, err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s`, aggTable)); err != nil {
		return fmt.Errorf("clear %s: %w", aggTable, err)
	}

	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (
			date, model, input_tokens, output_tokens,
			cache_read_tokens, cache_creation_tokens,
			%s
			cost_usd, request_count
		)
		SELECT
			date(timestamp, 'unixepoch', 'localtime'),
			model,
			COALESCE(SUM(input_tokens), 0),
			COALESCE(SUM(output_tokens), 0),
			COALESCE(SUM(cache_read_tokens), 0),
			COALESCE(SUM(cache_creation_tokens), 0),
			%s
			COALESCE(SUM(cost_usd), 0),
			COUNT(*)
		FROM %s
		GROUP BY date(timestamp, 'unixepoch', 'localtime'), model`,
		aggTable, extraAggColumns(parent), extraAggSelects(parent), parent,
	)); err != nil {
		return fmt.Errorf("rebuild %s: %w", aggTable, err)
	}

	return tx.Commit()
}

// extraAggColumns/Selects exist because codex_daily_model_agg carries
// reasoning_tokens and total_tokens that the Claude agg does not.
func extraAggColumns(parent string) string {
	if parent == "codex_api_requests" {
		return "reasoning_tokens, total_tokens,"
	}
	return ""
}
func extraAggSelects(parent string) string {
	if parent == "codex_api_requests" {
		return "COALESCE(SUM(reasoning_tokens), 0), COALESCE(SUM(total_tokens), 0),"
	}
	return ""
}
